// Package plan is the shape of a question silt asks about an image, separated
// from the answering of it.
//
// Everything silt does is some composition of "is this claim true in that
// tree", "what does this symbol settle to", "write this tree's file". Today
// each command walks the composition itself, decides which trees to open, and
// calls into whichever package knows that tree. That works, and it means
// every command re-derives the same facts, each slightly differently - which
// is how `silt check` and `silt why` came to disagree about what counts as a
// problem, and how the same Buildroot tree is read once per command rather
// than once.
//
// A Plan is the questions as data. Building one reads no files and opens no
// trees: it produces a list of asks and a function that folds their answers
// into a result. So a plan can be inspected before it is run -
//
//	Trees(p)        every tree this will need, before opening any
//	Symbols(p, t)   every name it will ask that tree about
//	len(p.Asks())   how much work this is
//
// and that is what makes the rest possible: open each tree exactly once for
// every image at once, answer independent questions in parallel, hash the plan
// as the cache key, and fold the same answers several ways - all failures for
// `check`, one derivation for `why` - from one description.
//
// # Applicative, deliberately
//
// No ask may depend on an earlier answer. That is a real restriction and it is
// the whole point: it is what makes the question list knowable in advance. A
// Kconfig fixpoint - where each round's defaults decide the next round's
// questions - genuinely needs more than this, and is not a Plan. It runs
// afterwards, on settled input, and loses nothing by being opaque.
//
// Rules are the interesting middle: a rule's consequent applies only if its
// condition holds, which looks like dependence. Branch keeps both arms in the
// ask list and folds only one, so `silt why` can say a rule was considered and
// its condition was false, and a cache key covers a rule whose untaken branch
// changed.
package plan

import (
	"fmt"
	"sort"
	"strings"
)

// Op is what is being asked.
type Op string

const (
	// Exists: is this a name the tree knows at all. The cheapest question,
	// and the one that catches a symbol deleted between releases.
	Exists Op = "exists"
	// Holds: does this claim hold - the symbol exists, its type admits the
	// value, and its dependencies allow it.
	Holds Op = "holds"
	// Settled: what value does this name end up with. Across trees this is
	// how one fact written twice is compared: a Zephyr symbol's value against
	// a device tree property's, as values, with no tree-specific comparison.
	Settled Op = "settled"
	// Solvable: can an image with these claims exist in this tree at all.
	// A tree with no solver answers Unknown, which reads as "not checked"
	// rather than as a failure.
	Solvable Op = "solvable"
	// EmitFile: write this tree's configuration.
	EmitFile Op = "emit"
)

// Ask is one question, as data. It is a struct rather than an interface so a
// plan can be printed, diffed and hashed without knowing what answers it.
type Ask struct {
	Op   Op
	Tree string
	Name string
	// Claim carries the constraint for Holds, and the constraint set for
	// Solvable and EmitFile, as an opaque payload the answering tree
	// understands. Core never reads it.
	Claim any
	// Where the claim was written, for the message when it fails.
	Pos string
	// Why this was asked, when a rule or a derivation explains it better than
	// the position does.
	Hint string
}

func (k Ask) String() string {
	s := fmt.Sprintf("%s %s:%s", k.Op, k.Tree, k.Name)
	if k.Pos != "" {
		s += " (" + k.Pos + ")"
	}
	return s
}

// Answer is what a tree says. Unknown is not a failure: it is a tree that
// cannot answer this kind of question - a device tree asked to solve, an
// ESPHome schema asked to predict - and the fold decides what that means.
type Answer struct {
	Bool    bool
	Value   string
	Err     error
	Unknown bool
}

func OK() Answer              { return Answer{} }
func Yes(b bool) Answer       { return Answer{Bool: b} }
func Val(v string) Answer     { return Answer{Value: v} }
func Fail(err error) Answer   { return Answer{Err: err} }
func NotAnswerable() Answer   { return Answer{Unknown: true} }
func (a Answer) Failed() bool { return a.Err != nil }

// Plan is a set of asks and a fold from their answers to a result.
//
// The two halves are kept apart on purpose: asks can be read without running
// anything, and the fold is a pure function of the answers.
type Plan[A any] struct {
	asks []Ask
	fold func([]Answer) A
}

// Asks is every question the plan will ask, in order.
func (p Plan[A]) Asks() []Ask { return p.asks }

// Fold applies the plan's folding function to answers. len(as) must equal
// len(p.Asks()); Run guarantees that.
func (p Plan[A]) Fold(as []Answer) A { return p.fold(as) }

// Pure is a plan that asks nothing.
func Pure[A any](a A) Plan[A] {
	return Plan[A]{fold: func([]Answer) A { return a }}
}

// Lift is one ask and what to make of its answer.
func Lift[A any](k Ask, f func(Answer) A) Plan[A] {
	return Plan[A]{asks: []Ask{k}, fold: func(as []Answer) A { return f(as[0]) }}
}

// Map transforms a plan's result without adding a question.
func Map[A, B any](p Plan[A], f func(A) B) Plan[B] {
	return Plan[B]{asks: p.asks, fold: func(as []Answer) B { return f(p.fold(as)) }}
}

// Map2 is the applicative. Both sides' asks are known before either is
// answered, which is the property everything else here depends on.
func Map2[A, B, C any](pa Plan[A], pb Plan[B], f func(A, B) C) Plan[C] {
	n := len(pa.asks)
	asks := make([]Ask, 0, n+len(pb.asks))
	asks = append(asks, pa.asks...)
	asks = append(asks, pb.asks...)
	return Plan[C]{
		asks: asks,
		fold: func(as []Answer) C { return f(pa.fold(as[:n]), pb.fold(as[n:])) },
	}
}

// Traverse runs a plan-producing function over a slice and collects the
// results, keeping every ask.
func Traverse[A, B any](xs []A, f func(A) Plan[B]) Plan[[]B] {
	out := Pure([]B{})
	for _, x := range xs {
		out = Map2(out, f(x), func(acc []B, b B) []B { return append(acc, b) })
	}
	return out
}

// Branch is the selective operator: both arms are asked, one is folded.
//
// A rule's consequent applies only when its condition holds, which is a
// dependence an applicative cannot express. Keeping both arms in the ask list
// buys two things that matter: `silt why` can report a rule that was
// considered and did not fire, and a cache key covers a rule whose untaken
// branch changed - which a key built only from the claims that fired would
// silently miss.
func Branch[A any](cond Plan[bool], then, els Plan[A]) Plan[A] {
	nc, nt := len(cond.asks), len(then.asks)
	asks := make([]Ask, 0, nc+nt+len(els.asks))
	asks = append(asks, cond.asks...)
	asks = append(asks, then.asks...)
	asks = append(asks, els.asks...)
	return Plan[A]{
		asks: asks,
		fold: func(as []Answer) A {
			if cond.fold(as[:nc]) {
				return then.fold(as[nc : nc+nt])
			}
			return els.fold(as[nc+nt:])
		},
	}
}

// Rewrite changes every ask without changing the shape of the plan: adding a
// hint, retargeting a tree, marking a region optional.
func Rewrite[A any](p Plan[A], f func(Ask) Ask) Plan[A] {
	asks := make([]Ask, len(p.asks))
	for i, k := range p.asks {
		asks[i] = f(k)
	}
	return Plan[A]{asks: asks, fold: p.fold}
}

// Trees is every tree the plan will need, sorted. Read before anything is
// opened, which is what lets each tree be opened exactly once for every image
// in a run rather than once per command.
func Trees[A any](p Plan[A]) []string {
	seen := map[string]bool{}
	for _, k := range p.asks {
		if k.Tree != "" {
			seen[k.Tree] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Symbols is every name the plan will ask one tree about, sorted and
// deduplicated.
func Symbols[A any](p Plan[A], tree string) []string {
	seen := map[string]bool{}
	for _, k := range p.asks {
		if k.Tree == tree && k.Name != "" {
			seen[k.Name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Describe renders the plan without running it, which is what makes a dry run
// possible and a cache key meaningful.
func Describe[A any](p Plan[A]) string {
	var b strings.Builder
	for _, k := range p.asks {
		b.WriteString(k.String())
		b.WriteByte('\n')
	}
	return b.String()
}
