// Package solve answers whether a composed image is satisfiable against the
// real Kconfig model, and reports what the composition implies.
//
// This is what the verifier cannot do. verify is three-valued and conservative:
// it reports only explicit contradictions, because a false positive there would
// block a configuration kbuild accepts. The solver sees the whole formula at
// once, so it catches compositions that are over-constrained in combination
// rather than in any single pair.
package solve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/cnf"
	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/solver"
)

// Assumption is one stated constraint, carried as a literal with its source.
type Assumption struct {
	Lit    cnf.Lit
	Symbol string
	Want   lang.Tristate
	From   string
	Pos    string
}

// Result is the outcome of solving an image.
type Result struct {
	Status      solver.Status
	Assumptions []Assumption
	// Implied lists symbols the model forces on that no fragment stated.
	// These are what the composition costs beyond what it says.
	Implied   []string
	Conflicts int
	// Culprits, on UNSAT, are the assumptions each of which alone is
	// satisfiable but which cannot hold together.
	Culprits []Assumption
	Err      error

	f     *cnf.Formula
	owner []fragmentOf
}

// Formula exposes the lowered model, for repair search.
func (r *Result) Formula() *cnf.Formula { return r.f }

// Owners maps each stated symbol to the fragment that stated it.
func (r *Result) Owners() []fragmentOf { return r.owner }

// Solve assumes every stated Buildroot constraint and asks the model.
func Solve(res *compose.Result, tree *kconfig.Tree) *Result {
	m := cnf.Lower(tree)
	out := &Result{f: m.F}

	// Which fragment stated each symbol, so a repair can name a fragment
	// rather than a symbol.
	byFragment := map[string]lang.ID{}
	for _, fr := range res.Fragments {
		for _, cs := range fr.Constraints {
			for _, c := range cs {
				if !c.Soft {
					byFragment[c.Symbol] = fr.ID
				}
			}
		}
	}
	for sym, id := range byFragment {
		out.owner = append(out.owner, fragmentOf{Symbol: sym, ID: id})
	}

	for _, c := range res.Constraints[lang.Buildroot] {
		if c.Soft || c.IsValue {
			continue // soft needs MaxSAT; values are not in the model
		}
		if _, ok := tree.Symbols[c.Symbol]; !ok {
			continue // verify reports unknown symbols; do not guess here
		}
		lit := m.F.Var(c.Symbol)
		if c.Want == lang.N {
			lit = lit.Neg()
		}
		out.Assumptions = append(out.Assumptions, Assumption{
			Lit: lit, Symbol: c.Symbol, Want: c.Want, From: c.From, Pos: c.Pos.Short(),
		})
	}

	lits := make([]cnf.Lit, len(out.Assumptions))
	for i, a := range out.Assumptions {
		lits[i] = a.Lit
	}
	r := solver.New(m.F).Solve(lits...)
	out.Status, out.Conflicts, out.Err = r.Status, r.Conflicts, r.Err
	if r.Err != nil {
		return out
	}

	switch r.Status {
	case solver.SAT:
		stated := map[string]bool{}
		for _, a := range out.Assumptions {
			stated[a.Symbol] = true
		}
		for name := range tree.Symbols {
			if stated[name] {
				continue
			}
			v := m.F.Var(name).Var()
			if r.Model[v] {
				out.Implied = append(out.Implied, name)
			}
		}
		sort.Strings(out.Implied)

	case solver.UNSAT:
		out.Culprits = narrow(m.F, out.Assumptions)
	}
	return out
}

// narrow finds a minimal-ish subset of assumptions that is still unsatisfiable,
// by dropping each in turn and keeping it only when its removal makes the rest
// satisfiable.
//
// This is deletion-based minimisation, the simplest MUS algorithm there is. It
// is not the full rung 8 story — no MCS, no ranked repairs — but it turns "these
// forty assumptions conflict" into the two or three that actually do, which is
// the difference between a core and an explanation.
func narrow(f *cnf.Formula, all []Assumption) []Assumption {
	keep := append([]Assumption(nil), all...)
	for i := 0; i < len(keep); {
		trial := make([]cnf.Lit, 0, len(keep)-1)
		for j, a := range keep {
			if j != i {
				trial = append(trial, a.Lit)
			}
		}
		if solver.New(f).Solve(trial...).Status == solver.UNSAT {
			// Still unsatisfiable without it, so it was not to blame.
			keep = append(keep[:i], keep[i+1:]...)
			continue
		}
		i++
	}
	return keep
}

// Explain renders the outcome.
func (r *Result) Explain(name string) string {
	var b strings.Builder
	switch {
	case r.Err != nil:
		fmt.Fprintf(&b, "UNKNOWN  %s\n  %v\n", name, r.Err)
	case r.Status == solver.SAT:
		fmt.Fprintf(&b, "SAT   %s\n", name)
		fmt.Fprintf(&b, "  assumed  %d stated symbols\n", len(r.Assumptions))
		fmt.Fprintf(&b, "  implied  %d more that no fragment states\n", len(r.Implied))
	case r.Status == solver.UNSAT:
		fmt.Fprintf(&b, "UNSAT  %s — these cannot hold together:\n\n", name)
		for _, a := range r.Culprits {
			fmt.Fprintf(&b, "  %s = %s\n      stated by %s at %s\n",
				a.Symbol, a.Want, a.From, a.Pos)
		}
		fmt.Fprintf(&b, "\n  %d of %d assumptions; the rest are satisfiable without them\n",
			len(r.Culprits), len(r.Assumptions))
	}
	return b.String()
}
