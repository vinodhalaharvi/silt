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

// Completion is a total assignment: every solvable symbol decided, not just the
// ones a fragment states.
//
// This is what replaces `make olddefconfig`. olddefconfig walks symbols in
// order taking the first default whose condition holds, which works and is why
// the vertical slice could use it from the start. Doing it with the model buys
// three things olddefconfig cannot give: it fails loudly instead of quietly
// dropping what it cannot satisfy, it reports which preferences yielded, and
// the result is globally consistent rather than order-dependent.
type Completion struct {
	Status solver.Status
	// Values is every solvable symbol's decided value.
	Values map[string]bool
	// Yielded lists soft constraints that could not hold. A preference that
	// silently vanished would be the defect this project exists to catch.
	Yielded []lang.Constraint
	// DefaultsHonored and DefaultsOverridden count how closely the result
	// tracks what Kconfig would have chosen on its own.
	DefaultsHonored    int
	DefaultsOverridden []string
	Stated             int
	Err                error
}

// Complete solves for a total assignment honouring stated constraints, then
// preferences, then Kconfig's own defaults.
func Complete(res *compose.Result, tree *kconfig.Tree) *Completion {
	m := cnf.Lower(tree)
	c := &Completion{Values: map[string]bool{}}

	var hard []cnf.Lit
	var softs []lang.Constraint
	for _, k := range res.Constraints[lang.Buildroot] {
		if k.IsValue {
			continue
		}
		if _, ok := tree.Symbols[k.Sym.Name]; !ok {
			continue
		}
		lit := m.F.Var(k.Sym.Name)
		if k.Want == lang.N {
			lit = lit.Neg()
		}
		if k.Soft {
			softs = append(softs, k)
			continue
		}
		hard = append(hard, lit)
	}
	c.Stated = len(hard)

	// Preferences are added one at a time and kept only while the result stays
	// satisfiable. That is a greedy approximation of MaxSAT, not the optimum:
	// with few soft constraints the difference is nil, and it keeps the cost at
	// one solve per preference rather than one per symbol. The probes share one
	// solver; the final solve below gets its own, so the model it lands on does
	// not depend on which preferences happened to be probed first.
	assumed := append([]cnf.Lit(nil), hard...)
	probe := solver.New(m.F)
	for _, s := range softs {
		lit := m.F.Var(s.Sym.Name)
		if s.Want == lang.N {
			lit = lit.Neg()
		}
		trial := append(append([]cnf.Lit(nil), assumed...), lit)
		if probe.Solve(trial...).Status == solver.SAT {
			assumed = trial
			continue
		}
		c.Yielded = append(c.Yielded, s)
	}

	// Kconfig's defaults become the decision polarity, so one solve lands close
	// to what kbuild would have chosen without asserting nine thousand
	// constraints and minimising the conflicting subset.
	pol := defaultPolarity(m.F, tree)
	s := solver.New(m.F)
	s.SetPolarity(pol)
	r := s.Solve(assumed...)
	c.Status, c.Err = r.Status, r.Err
	if r.Status != solver.SAT {
		return c
	}

	for name, sym := range tree.Symbols {
		if !sym.Type.Solvable() {
			continue
		}
		v := m.F.Var(name).Var()
		c.Values[name] = r.Value(v)
		if want, ok := pol[v]; ok {
			if want == r.Value(v) {
				c.DefaultsHonored++
			} else {
				c.DefaultsOverridden = append(c.DefaultsOverridden, name)
			}
		}
	}
	sort.Strings(c.DefaultsOverridden)
	return c
}

// defaultPolarity reads each symbol's first default, which is what Kconfig's
// own first-match-wins evaluation would reach for.
//
// Conditions are not evaluated here: a default guarded by a condition is taken
// as a hint rather than a fact, because a hint that turns out wrong costs
// nothing — the solver decides otherwise and the symbol is reported as
// overridden.
func defaultPolarity(f *cnf.Formula, tree *kconfig.Tree) map[int]bool {
	pol := map[int]bool{}
	for name, sym := range tree.Symbols {
		if !sym.Type.Solvable() || len(sym.Defaults) == 0 {
			continue
		}
		switch strings.TrimSpace(sym.Defaults[0].Value) {
		case "y", "m":
			pol[f.Var(name).Var()] = true
		case "n":
			pol[f.Var(name).Var()] = false
		}
	}
	return pol
}

// Report renders the completion.
func (c *Completion) Report() string {
	var b strings.Builder
	if c.Status != solver.SAT {
		fmt.Fprintf(&b, "could not complete: %v\n", c.Status)
		return b.String()
	}
	on := 0
	for _, v := range c.Values {
		if v {
			on++
		}
	}
	fmt.Fprintf(&b, "completed  %d symbols decided, %d on\n", len(c.Values), on)
	fmt.Fprintf(&b, "  stated     %d\n", c.Stated)
	fmt.Fprintf(&b, "  defaults   %d honored, %d overridden by constraints\n",
		c.DefaultsHonored, len(c.DefaultsOverridden))
	if len(c.Yielded) > 0 {
		fmt.Fprintf(&b, "  yielded    %d preference(s) could not hold:\n", len(c.Yielded))
		for _, y := range c.Yielded {
			fmt.Fprintf(&b, "               %s = %s  (%s at %s)\n",
				y.Sym, y.Want, y.From, y.Pos.Short())
		}
	}
	return b.String()
}
