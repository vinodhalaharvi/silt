// Package plugin is how one kind of tree answers the questions in a plan.
//
// A kind supplies a value rather than implements an interface:
//
//	Open     config → whatever that kind needs loaded
//	Answer   loaded, ask → answer
//	Emit     loaded, claims → a file its builder consumes      nil: configures nothing
//	Settle   loaded, claims → what each name ends up as        nil: cannot predict
//	Solve    loaded, claims → can this exist at all            nil: no solver
//	Read     a file → claims                                   nil: cannot import
//
// Nil says "this kind cannot do that" with no second interface to assert
// against: a device tree has no Emit because the kernel builds it, and an
// ESPHome schema has no Settle because it has no defaults to propagate.
// Functions can be shared outright, which is why a second Kconfig-shaped
// system costs an Open and an Emit and reuses Answer and Settle unchanged.
// And a struct of funcs can be wrapped, which is what the middleware below is.
//
// # No erasure
//
// Interp carries its claim type and its loaded type as parameters, and so does
// everything that runs or wraps it. There is no `any` and no type assertion
// anywhere in this package. Go has no existential, so a heterogeneous list of
// interpreters is not expressible - and the answer is not to erase, it is to
// keep each kind's run separate and let the results meet. Results are V[Unit],
// which is one type.
//
// The set of kinds is closed and small. A runner over all of them is a struct
// with one field per kind, so a kind added without being handled is a compile
// error rather than a tree whose questions nobody answers.
package plugin

import (
	"fmt"
	"io"
	"sync"

	"github.com/vinodhalaharvi/silt/plan"
)

// Interp is a kind of tree. C is its claim type, Cfg its configuration, D what
// Open loads.
type Interp[C any, Cfg any, D any] struct {
	Open    func(Cfg) (D, error)
	Answer  func(D, plan.Ask[C]) plan.Answer
	Emit    func(D, []C) (File, error)
	Settle  func(D, []C) (map[string]string, error)
	Solve   func(D, []C) error
	Read    func([]byte) ([]C, error)
	Affects func(string) bool
}

// File is an emitted configuration: a name its builder expects and the bytes.
type File struct {
	Name string
	Body []byte
	// ConsumedBy is the Buildroot symbol that hands this file to the build,
	// empty for a tree Buildroot does not consume - which is a fact about the
	// kind rather than an omission.
	ConsumedBy string
}

// Bound is an interpreter with its configuration, ready to answer. Still fully
// typed: binding a config is not erasing one.
type Bound[C any, Cfg any, D any] struct {
	Interp Interp[C, Cfg, D]
	Config Cfg

	// Version is what this tree reports itself as, for checking an image's
	// (verified-against ...) against what actually answered.
	Version string
}

// Bind pairs an interpreter with a configuration.
func Bind[C, Cfg, D any](i Interp[C, Cfg, D], cfg Cfg) *Bound[C, Cfg, D] {
	return &Bound[C, Cfg, D]{Interp: i, Config: cfg}
}

// Run answers a plan's asks and folds them.
//
// The tree is opened once for the whole plan rather than once per ask, which
// is the first thing the plan's up-front ask list buys.
func Run[C, Cfg, D, A any](p plan.Plan[C, A], b *Bound[C, Cfg, D]) (A, error) {
	var zero A
	if len(p.Asks()) == 0 {
		// Nothing to ask, so nothing to open. An image that states no CONFIG_
		// should not pay for reading a kernel's Kconfig, and that is a
		// property of the plan being data rather than of any wrapper.
		return p.Fold(nil), nil
	}
	if b == nil {
		// No interpreter for this kind: every question is unanswerable, which
		// reads as "not checked" rather than as a failure - the same meaning
		// a missing --linux has always had.
		answers := make([]plan.Answer, len(p.Asks()))
		for i := range answers {
			answers[i] = plan.NotAnswerable()
		}
		return p.Fold(answers), nil
	}

	d, err := b.Interp.Open(b.Config)
	if err != nil {
		return zero, err
	}
	answers := make([]plan.Answer, len(p.Asks()))
	for i, k := range p.Asks() {
		answers[i] = b.Interp.Answer(d, k)
	}
	return p.Fold(answers), nil
}

// Affects reports whether changing a setting changes what gets built. A kind
// that does not say assumes everything does, which is the safe answer:
// wrongly reusing a cached tree is worse than wrongly rebuilding one.
func (b *Bound[C, Cfg, D]) Affects(name string) bool {
	if b == nil || b.Interp.Affects == nil {
		return true
	}
	return b.Interp.Affects(name)
}

func (b *Bound[C, Cfg, D]) CanEmit() bool   { return b != nil && b.Interp.Emit != nil }
func (b *Bound[C, Cfg, D]) CanSettle() bool { return b != nil && b.Interp.Settle != nil }
func (b *Bound[C, Cfg, D]) CanSolve() bool  { return b != nil && b.Interp.Solve != nil }
func (b *Bound[C, Cfg, D]) CanRead() bool   { return b != nil && b.Interp.Read != nil }

// --- middleware --------------------------------------------------------
//
// Generic in the kind, so each is written once and applies to every kind
// including ones added later - without any of them being erased to do it.

// Wrap transforms an interpreter of one kind.
type Wrap[C, Cfg, D any] func(Interp[C, Cfg, D]) Interp[C, Cfg, D]

// Wrapped applies wraps outermost-first.
func Wrapped[C, Cfg, D any](i Interp[C, Cfg, D], ws ...Wrap[C, Cfg, D]) Interp[C, Cfg, D] {
	for n := len(ws) - 1; n >= 0; n-- {
		i = ws[n](i)
	}
	return i
}

// Lazy opens at most once, and only when something asks.
func Lazy[C, Cfg, D any]() Wrap[C, Cfg, D] {
	return func(i Interp[C, Cfg, D]) Interp[C, Cfg, D] {
		next := i.Open
		var once sync.Once
		var d D
		var err error
		i.Open = func(cfg Cfg) (D, error) {
			once.Do(func() { d, err = next(cfg) })
			return d, err
		}
		return i
	}
}

// Memo answers each distinct question once. Position and hint are about where
// a claim was written rather than what is being asked, so they must not split
// the cache: fifty-six images asking about one symbol is one question.
func Memo[C, Cfg, D any]() Wrap[C, Cfg, D] {
	type key struct {
		op         plan.Op
		tree, name string
	}
	return func(i Interp[C, Cfg, D]) Interp[C, Cfg, D] {
		next := i.Answer
		seen := map[key]plan.Answer{}
		i.Answer = func(d D, k plan.Ask[C]) plan.Answer {
			// Only questions that do not depend on the claim are cacheable;
			// a Holds carries one and two claims about a symbol can differ.
			if k.Op != plan.Exists && k.Op != plan.Settled {
				return next(d, k)
			}
			id := key{k.Op, k.Tree, k.Name}
			if a, ok := seen[id]; ok {
				return a
			}
			a := next(d, k)
			seen[id] = a
			return a
		}
		return i
	}
}

// Trace writes every question and answer: a debugger for the model rather than
// for the configuration.
func Trace[C, Cfg, D any](w io.Writer) Wrap[C, Cfg, D] {
	return func(i Interp[C, Cfg, D]) Interp[C, Cfg, D] {
		next := i.Answer
		i.Answer = func(d D, k plan.Ask[C]) plan.Answer {
			a := next(d, k)
			switch {
			case a.Unknown:
				fmt.Fprintf(w, "%-10s %-8s %-44s unanswerable\n", k.Tree, k.Op, k.Name)
			case a.Failed():
				fmt.Fprintf(w, "%-10s %-8s %-44s %v\n", k.Tree, k.Op, k.Name, a.Err)
			default:
				fmt.Fprintf(w, "%-10s %-8s %-44s ok\n", k.Tree, k.Op, k.Name)
			}
			return a
		}
		return i
	}
}

// Pinned refuses answers from a source that is not the version an image
// declared, on every question rather than once at startup.
func Pinned[C, Cfg, D any](want, have string) Wrap[C, Cfg, D] {
	return func(i Interp[C, Cfg, D]) Interp[C, Cfg, D] {
		next := i.Answer
		i.Answer = func(d D, k plan.Ask[C]) plan.Answer {
			if want != "" && have != "" && want != have {
				return plan.Fail(fmt.Errorf(
					"%s answered by %s, and the image declares %s", k.Name, have, want))
			}
			return next(d, k)
		}
		return i
	}
}

// There was an Or here - ask one interpreter, fall back to another when it
// does not know, for a board's overlays over an SoC's base tree. It was
// removed rather than fixed: two interpreters of one kind have two
// configurations and so two loaded states, and a combinator that shares one
// between them answers the second from the first's data. The version that
// works takes two bound interpreters and loads a pair, which is worth writing
// when something needs it and not before.
