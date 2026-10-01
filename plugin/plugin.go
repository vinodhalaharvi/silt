// Package plugin is how a tree answers the questions in a plan.
//
// A kind of tree - Kconfig, a device tree, an ESPHome schema, a FreeRTOS
// header - supplies a value rather than implements an interface:
//
//	Open     config → whatever that tree needs loaded
//	Answer   loaded, ask → answer
//	Emit     loaded, claims → a file its builder consumes      nil: configures nothing
//	Settle   loaded, claims → what each name ends up as        nil: cannot predict
//	Solve    loaded, claims → can this exist at all            nil: no solver
//	Affects  name → does changing this change a build          nil: everything does
//	Read     a file → claims                                   nil: cannot import
//
// A value rather than an interface for three reasons. Nil says "this kind
// cannot do that" with no second interface to assert against: a device tree
// has no Emit because the kernel builds it, and an ESPHome schema has no
// Settle because it has no defaults to propagate. Functions can be shared
// outright, which is why Zephyr costs an Open and an Emit and reuses Kconfig's
// Answer and Settle unchanged. And a struct of funcs can be wrapped, which is
// what Memo, Lazy, Trace and Pinned below are - written once, applying to
// every kind including ones added later.
//
// The type parameters are the kind's own config and loaded state, so a plugin
// is written against its own types with no assertions. Erase takes one
// assertion, in one function, at the registry boundary.
package plugin

import (
	"fmt"
	"io"
	"sync"

	"github.com/vinodhalaharvi/silt/plan"
)

// Interp is a kind of tree: C is its configuration, D is what Open loads.
type Interp[C any, D any] struct {
	Open    func(C) (D, error)
	Answer  func(D, plan.Ask) plan.Answer
	Emit    func(D, any) (File, error)
	Settle  func(D, any) (map[string]string, error)
	Solve   func(D, any) error
	Affects func(string) bool
	Read    func([]byte) (any, error)
}

// File is an emitted configuration: a name its builder expects and the bytes.
type File struct {
	Name string
	Body []byte
}

// Opaque is an Interp with its types erased, which is what a registry can hold
// and what the middleware below wraps. One type assertion, written once.
type Opaque struct {
	open    func() (any, error)
	answer  func(any, plan.Ask) plan.Answer
	emit    func(any, any) (File, error)
	settle  func(any, any) (map[string]string, error)
	solve   func(any, any) error
	affects func(string) bool
	read    func([]byte) (any, error)

	// Version is what this tree reports itself as, for checking an image's
	// (verified-against ...) against what actually answered.
	Version string
}

// Erase binds a kind to a configuration and forgets both types.
func Erase[C, D any](i Interp[C, D], c C) Opaque {
	o := Opaque{
		open: func() (any, error) { return i.Open(c) },
		answer: func(d any, k plan.Ask) plan.Answer {
			return i.Answer(d.(D), k)
		},
		affects: i.Affects,
		read:    i.Read,
	}
	// nil stays nil through erasure: a kind that cannot emit must not look
	// like one that emits nothing, because the first is a fact about the kind
	// and the second would be a silently empty file.
	if i.Emit != nil {
		o.emit = func(d any, cs any) (File, error) { return i.Emit(d.(D), cs) }
	}
	if i.Settle != nil {
		o.settle = func(d any, cs any) (map[string]string, error) { return i.Settle(d.(D), cs) }
	}
	if i.Solve != nil {
		o.solve = func(d any, cs any) error { return i.Solve(d.(D), cs) }
	}
	return o
}

// CanEmit, CanSettle and CanSolve let a caller ask what a tree is capable of
// before asking it to do the thing.
func (o Opaque) CanEmit() bool   { return o.emit != nil }
func (o Opaque) CanSettle() bool { return o.settle != nil }
func (o Opaque) CanSolve() bool  { return o.solve != nil }

// Affects reports whether changing a setting changes what gets built. A kind
// that does not say assumes everything does, which is the safe answer: a
// cache that wrongly reuses a tree is worse than one that wrongly rebuilds.
func (o Opaque) Affects(name string) bool {
	if o.affects == nil {
		return true
	}
	return o.affects(name)
}

// Registry maps a tree's name to whatever answers for it.
type Registry map[string]Opaque

// Run answers a plan's asks and folds them.
//
// Each tree is opened once for the whole plan rather than once per ask or once
// per command, which is the first thing the plan's up-front ask list buys.
func Run[A any](p plan.Plan[A], reg Registry) (A, error) {
	asks := p.Asks()

	loaded := map[string]any{}
	for _, tree := range plan.Trees(p) {
		o, ok := reg[tree]
		if !ok {
			continue // no interpreter: every ask about it answers Unknown
		}
		d, err := o.open()
		if err != nil {
			var zero A
			return zero, fmt.Errorf("%s: %w", tree, err)
		}
		loaded[tree] = d
	}

	answers := make([]plan.Answer, len(asks))
	for i, k := range asks {
		o, ok := reg[k.Tree]
		if !ok {
			answers[i] = plan.NotAnswerable()
			continue
		}
		answers[i] = o.answer(loaded[k.Tree], k)
	}
	return p.Fold(answers), nil
}

// Wrap is middleware over a kind. Opaque in, Opaque out - so caching,
// laziness, tracing and version pinning are each written once and apply to
// every kind, including ones written afterwards.
type Wrap func(Opaque) Opaque

// Wrapped applies wraps outermost-first, so Wrapped(o, Lazy(), Memo()) reads
// as "lazy, then memoised".
func Wrapped(o Opaque, ws ...Wrap) Opaque {
	for i := len(ws) - 1; i >= 0; i-- {
		o = ws[i](o)
	}
	return o
}

// Each applies the same wraps to every kind in a registry.
func Each(r Registry, ws ...Wrap) Registry {
	out := make(Registry, len(r))
	for name, o := range r {
		out[name] = Wrapped(o, ws...)
	}
	return out
}

// Lazy opens a tree at most once, and only if something asks about it. An
// image that states no CONFIG_ never pays for reading a kernel's Kconfig.
func Lazy() Wrap {
	return func(o Opaque) Opaque {
		next := o.open
		var once sync.Once
		var d any
		var err error
		o.open = func() (any, error) {
			once.Do(func() { d, err = next() })
			return d, err
		}
		return o
	}
}

// Memo answers each distinct question once. Fifty-six images asking whether
// BR2_PACKAGE_BUSYBOX exists is one question.
func Memo() Wrap {
	return func(o Opaque) Opaque {
		next := o.answer
		seen := map[plan.Ask]plan.Answer{}
		o.answer = func(d any, k plan.Ask) plan.Answer {
			// Position and hint are about where a claim was written, not what
			// is being asked, so they must not split the cache.
			key := plan.Ask{Op: k.Op, Tree: k.Tree, Name: k.Name}
			if a, ok := seen[key]; ok && k.Claim == nil {
				return a
			}
			a := next(d, k)
			if k.Claim == nil {
				seen[key] = a
			}
			return a
		}
		return o
	}
}

// Trace writes every question and answer, which is a debugger for the model
// itself rather than for the configuration.
func Trace(w io.Writer) Wrap {
	return func(o Opaque) Opaque {
		next := o.answer
		o.answer = func(d any, k plan.Ask) plan.Answer {
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
		return o
	}
}

// Pinned refuses answers from a source that is not the version an image
// declared. (verified-against (buildroot "2025.02.16")) is checked on every
// question rather than once, so a tree swapped underneath a long run is
// caught where it matters.
func Pinned(want string) Wrap {
	return func(o Opaque) Opaque {
		next := o.answer
		o.answer = func(d any, k plan.Ask) plan.Answer {
			if want != "" && o.Version != "" && o.Version != want {
				return plan.Fail(fmt.Errorf(
					"%s answered by %s, and the image declares %s",
					k.Name, o.Version, want))
			}
			return next(d, k)
		}
		return o
	}
}

// Or asks the first interpreter and falls back to the second when it does not
// know. A board's overlays over an SoC's base tree are this.
func Or(a, b Opaque) Opaque {
	return Opaque{
		open: func() (any, error) {
			da, err := a.open()
			if err != nil {
				return nil, err
			}
			db, err := b.open()
			if err != nil {
				return nil, err
			}
			return [2]any{da, db}, nil
		},
		answer: func(d any, k plan.Ask) plan.Answer {
			pair := d.([2]any)
			if r := a.answer(pair[0], k); !r.Unknown {
				return r
			}
			return b.answer(pair[1], k)
		},
	}
}
