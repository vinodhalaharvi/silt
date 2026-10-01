package plan

import (
	"errors"
	"fmt"
	"strings"
)

// V is a result that carries every failure rather than the first.
//
// This is the difference between an error and a report. `silt check` over 56
// images should say what is wrong with all of them, not stop at the first
// missing symbol and make the reader run it again - and today that behaviour
// is achieved by a shell script collecting exit codes, which is why `silt
// check` and `silt why` can disagree about what counts as a problem. Here it
// is the fold, so every command that folds this way agrees by construction.
//
// Go has no sum types, so a V holds a value and a list of problems; a V with
// problems is failed whatever its value is.
type V[A any] struct {
	Value    A
	Problems []Problem
}

// Problem is one failure, with enough to point at the thing that caused it.
type Problem struct {
	Ask Ask
	Err error
}

func (p Problem) Error() string {
	s := p.Err.Error()
	if p.Ask.Pos != "" {
		s = p.Ask.Pos + ": " + s
	}
	if p.Ask.Hint != "" {
		s += "\n  " + p.Ask.Hint
	}
	return s
}

// Good is a V with no problems.
func Good[A any](a A) V[A] { return V[A]{Value: a} }

// Bad is a V carrying one problem.
func Bad[A any](k Ask, err error) V[A] {
	return V[A]{Problems: []Problem{{Ask: k, Err: err}}}
}

// Failed reports whether anything went wrong.
func (v V[A]) Failed() bool { return len(v.Problems) > 0 }

// Err collapses a V into a single error, for a caller that wants one. Every
// problem is kept in the message: collapsing to the first here would undo the
// accumulation this type exists for.
func (v V[A]) Err() error {
	if !v.Failed() {
		return nil
	}
	var b strings.Builder
	for i, p := range v.Problems {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(p.Error())
	}
	return errors.New(b.String())
}

// Unit is the result of a check that produces nothing but its problems.
type Unit struct{}

// Both combines two validations, keeping every problem from each. This is the
// applicative's combining step, and the reason a failing left side does not
// hide the right side's failures.
func Both[A, B, C any](a V[A], b V[B], f func(A, B) C) V[C] {
	out := V[C]{Value: f(a.Value, b.Value)}
	out.Problems = append(append([]Problem{}, a.Problems...), b.Problems...)
	return out
}

// All collects a slice of validations into one.
func All[A any](vs []V[A]) V[[]A] {
	out := V[[]A]{}
	for _, v := range vs {
		out.Value = append(out.Value, v.Value)
		out.Problems = append(out.Problems, v.Problems...)
	}
	return out
}

// Checking is the standard fold for a claim: an answer becomes a problem or
// nothing, attributed to the ask that produced it.
func Checking(k Ask) func(Answer) V[Unit] {
	return func(a Answer) V[Unit] {
		switch {
		case a.Unknown:
			// A tree that cannot answer this kind of question is not a
			// failure. `silt check` without --linux reports CONFIG_ claims as
			// unchecked rather than as broken, and this is where that comes
			// from - uniformly, for every tree, rather than per command.
			return Good(Unit{})
		case a.Failed():
			return Bad[Unit](k, a.Err)
		default:
			return Good(Unit{})
		}
	}
}

// Unchecked counts the asks a run could not answer, per tree, so a command can
// say "43 symbols not checked: no linux tree given" without each command
// working it out again.
func Unchecked(asks []Ask, answers []Answer) map[string]int {
	out := map[string]int{}
	for i, a := range answers {
		if i < len(asks) && a.Unknown {
			out[asks[i].Tree]++
		}
	}
	return out
}

// Claim is the usual Lift for a claim: ask whether it holds, accumulate.
func Claim(k Ask) Plan[V[Unit]] { return Lift(k, Checking(k)) }

// SameValue is the cross-tree check that needs no tree to know about the
// other: settle a name in each, compare the results as strings.
//
// This is the whole heterogeneous-system argument in one function. The shared
// memory base an M7's Zephyr config states and the address a device tree gives
// remoteproc are one fact written twice today, in two files, checked by a
// logic analyser when it is wrong.
func SameValue(a, b Ask, why string) Plan[V[Unit]] {
	a.Op, b.Op = Settled, Settled
	return Map2(
		Lift(a, func(x Answer) Answer { return x }),
		Lift(b, func(y Answer) Answer { return y }),
		func(x, y Answer) V[Unit] {
			if x.Unknown || y.Unknown {
				return Good(Unit{})
			}
			if x.Value == y.Value {
				return Good(Unit{})
			}
			return Bad[Unit](a, fmt.Errorf("%s:%s is %q and %s:%s is %q; %s",
				a.Tree, a.Name, x.Value, b.Tree, b.Name, y.Value, why))
		})
}
