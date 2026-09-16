package solve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/cnf"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/solver"
)

// Repair is one way to make an unsatisfiable composition satisfiable.
//
// Repairs are expressed in fragments, not symbols. "Drop feature:camera" is
// something a person can act on; "set BR2_PACKAGE_LIBCAMERA=n" is the same fact
// in a form that leaves them to work out which fragment said it and why.
type Repair struct {
	// Drop is the fragment to remove, empty for a symbol-level repair.
	Drop lang.ID
	// Flip is the symbol to change when no single fragment is responsible.
	Flip string
	From string // fragment that stated Flip
	Pos  string
	// Changes is how many stated symbols this repair alters, which is what
	// (minimize changed-symbols) ranks on.
	Changes int
	// Cost is the policy score; lower is better.
	Cost int
	Note string
}

func (r Repair) String() string {
	if r.Drop.Name != "" {
		return fmt.Sprintf("drop %s", r.Drop)
	}
	return fmt.Sprintf("%s: stated by %s at %s", r.Flip, r.From, r.Pos)
}

// Repairs proposes ways out of an unsatisfiable composition, ranked by policy.
//
// Every element of a minimal unsatisfiable set is, by definition, a single
// change that breaks that set — so each culprit yields a candidate. Each is then
// verified rather than assumed: a candidate is kept only if the rest actually
// solves without it, because one MUS being broken does not mean the whole
// formula is satisfiable when others overlap.
func Repairs(r *Result, f *cnf.Formula, frags []fragmentOf, policy lang.Policy) []Repair {
	if r.Status != solver.UNSAT {
		return nil
	}
	owner := map[string]lang.ID{}
	for _, fr := range frags {
		owner[fr.Symbol] = fr.ID
	}

	var out []Repair
	for _, c := range r.Culprits {
		// Everything except the assumptions this fragment (or symbol) supplies.
		drop := owner[c.Symbol]
		var trial []cnf.Lit
		var changes int
		for _, a := range r.Assumptions {
			if a.Symbol == c.Symbol || (drop.Name != "" && owner[a.Symbol] == drop) {
				changes++
				continue
			}
			trial = append(trial, a.Lit)
		}
		if solver.New(f).Solve(trial...).Status != solver.SAT {
			continue // breaking this MUS is not enough; another overlaps
		}
		rep := Repair{Drop: drop, Flip: c.Symbol, From: c.From, Pos: c.Pos, Changes: changes}
		if drop.Name == "" {
			rep.Note = "no single fragment owns this symbol"
		}
		rep.Cost = cost(rep, policy)
		out = append(out, rep)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost < out[j].Cost
		}
		return out[i].Changes < out[j].Changes
	})
	return out
}

// cost scores a repair against the declared policy. Only statically known
// quantities appear: changed-symbol counts and explicit keeps. Image size and
// boot time are observed outputs and are not eligible (DESIGN.md §9).
func cost(r Repair, p lang.Policy) int {
	c := r.Changes
	for _, k := range p.Keep {
		if (k == "target" && r.Drop.Kind == lang.Target) ||
			(k == "profile" && r.Drop.Kind == lang.Profile) {
			c += 1000 // keep means strongly prefer not to touch, not forbid
		}
	}
	// Dropping an optional feature is cheaper than rebuilding userspace.
	if r.Drop.Kind == lang.Feature {
		c -= 1
	}
	return c
}

// fragmentOf ties a stated symbol to the fragment that stated it.
type fragmentOf struct {
	Symbol string
	ID     lang.ID
}

// ExplainRepairs renders the ranked list.
func ExplainRepairs(reps []Repair) string {
	if len(reps) == 0 {
		return "\n  No single change found. More than one conflict overlaps, so the\n" +
			"  repair needs a combination — minimal hitting sets over several\n" +
			"  unsatisfiable subsets, which is not implemented.\n"
	}
	var b strings.Builder
	b.WriteString("\nRepairs, ranked:\n")
	for i, r := range reps {
		fmt.Fprintf(&b, "  %d. %-34s %d change(s)", i+1, r.String(), r.Changes)
		if r.Note != "" {
			fmt.Fprintf(&b, "  — %s", r.Note)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
