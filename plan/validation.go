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
//
// The ask is flattened to the parts a reader needs - which tree, which name,
// where it was written - rather than carrying the claim's type. That is what
// lets several kinds' validations be combined: a Kconfig failure and an
// ESPHome failure are the same type of problem even though they came from
// different types of question, and the place for kinds to meet is their
// results rather than their questions.
type Problem struct {
	Tree string
	Name string
	Pos  string
	Hint string
	Err  error
}

func (p Problem) Error() string {
	s := p.Err.Error()
	if p.Pos != "" {
		s = p.Pos + ": " + s
	}
	if p.Hint != "" {
		s += "\n  " + p.Hint
	}
	return s
}

// From flattens an ask into the parts a problem needs.
func From[C any](k Ask[C]) Problem {
	return Problem{Tree: k.Tree, Name: k.Name, Pos: k.Pos, Hint: k.Hint}
}

// Good is a V with no problems.
func Good[A any](a A) V[A] { return V[A]{Value: a} }

// Bad is a V carrying one problem.
func Bad[C, A any](k Ask[C], err error) V[A] {
	p := From(k)
	p.Err = err
	return V[A]{Problems: []Problem{p}}
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
func Checking[C any](k Ask[C]) func(Answer) V[Unit] {
	return func(a Answer) V[Unit] {
		switch {
		case a.Unknown:
			// A tree that cannot answer this kind of question is not a
			// failure. `silt check` without --linux reports CONFIG_ claims as
			// unchecked rather than as broken, and this is where that comes
			// from - uniformly, for every tree, rather than per command.
			return Good(Unit{})
		case a.Failed():
			return Bad[C, Unit](k, a.Err)
		default:
			return Good(Unit{})
		}
	}
}

// Unchecked counts the asks a run could not answer, per tree, so a command can
// say "43 symbols not checked: no linux tree given" without each command
// working it out again.
func Unchecked[C any](asks []Ask[C], answers []Answer) map[string]int {
	out := map[string]int{}
	for i, a := range answers {
		if i < len(asks) && a.Unknown {
			out[asks[i].Tree]++
		}
	}
	return out
}

// Claim is the usual Lift for a claim: ask whether it holds, accumulate.
func Claim[C any](k Ask[C]) Plan[C, V[Unit]] { return Lift(k, Checking(k)) }

// Compare is the cross-tree check, and the one thing that cannot be a single
// plan: its two sides are questions of two different kinds, so they have two
// different types and Go has no list that holds both.
//
// That is not a limitation to work around - it is the design. Each side is
// asked by its own kind's runner, and the two meet here as answers, which is
// one type. A shared-memory base an RTOS states and the address a device tree
// gives remoteproc are one fact written twice; this compares them without
// either kind knowing the other exists.
func Compare(a, b Answer, what, why string) V[Unit] {
	if a.Unknown || b.Unknown {
		// A tree that was not given cannot contradict anything.
		return Good(Unit{})
	}
	if a.Value == b.Value {
		return Good(Unit{})
	}
	return V[Unit]{Problems: []Problem{{
		Err: fmt.Errorf("%s: %q and %q; %s", what, a.Value, b.Value, why),
	}}}
}
