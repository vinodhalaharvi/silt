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
// Repairs are expressed in fragments where possible, not symbols. "Drop
// feature:camera" is something a person can act on; "set
// BR2_PACKAGE_LIBCAMERA=n" is the same fact in a form that leaves them to work
// out which fragment said it and why.
//
// A repair may name more than one thing. When two conflicts overlap, no single
// change is enough, and saying "a combination is needed" without saying which
// combination is correct and useless.
type Repair struct {
	// Drop lists the fragments to remove, in composition order.
	Drop []lang.ID
	// Flip lists stated symbols to change that no dropped fragment covers:
	// image overrides, opaque values, anything with no single owner.
	Flip []Assumption
	// Changes is how many stated symbols this repair alters, which is what
	// (minimize changed-symbols) ranks on.
	Changes int
	// Cost is the policy score; lower is better.
	Cost int
	Note string
}

func (r Repair) String() string {
	var parts []string
	for _, d := range r.Drop {
		parts = append(parts, "drop "+d.String())
	}
	for _, f := range r.Flip {
		parts = append(parts, "unset "+f.Symbol)
	}
	if len(parts) == 0 {
		return "(nothing)"
	}
	return strings.Join(parts, " + ")
}

// Repairs proposes ways out of an unsatisfiable composition, ranked by policy.
//
// Each repair is a minimal correction set: a set of stated constraints whose
// removal makes the composition satisfiable, and which has no satisfiable
// proper subset left out. By the MUS/MCS duality in DESIGN.md §11.3, the
// minimal correction sets are exactly the minimal hitting sets of the minimal
// unsatisfiable subsets — so enumerating them enumerates the hitting sets,
// without enumerating the MUSes first.
//
// They are found the other way round, by growing maximal satisfiable subsets.
// Each assumption gets a relaxation variable r with the clause (!r or a), so
// assuming r means "this constraint holds" and leaving it out means "this one
// may be dropped". Growing the assumed set one at a time, keeping whatever
// stays satisfiable, yields a maximal satisfiable subset; what is left out is
// a minimal correction set, satisfiable by construction rather than by a
// verification step afterwards. A blocking clause requiring one of its members
// back forces the next round to find a different one.
//
// The growth order is the policy's: what the policy wants to keep is offered
// first, so it ends up inside the satisfiable subset and outside the repair.
func Repairs(r *Result, f *cnf.Formula, frags []fragmentOf, policy lang.Policy) []Repair {
	if r.Status != solver.UNSAT || len(r.Assumptions) == 0 {
		return nil
	}
	owner := map[string]lang.ID{}
	for _, fr := range frags {
		owner[fr.Symbol] = fr.ID
	}

	// Offer the assumptions in the order the policy would rather keep them.
	order := make([]int, len(r.Assumptions))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return keepRank(owner[r.Assumptions[order[a]].Symbol], policy) <
			keepRank(owner[r.Assumptions[order[b]].Symbol], policy)
	})

	relax := make([]cnf.Lit, len(r.Assumptions))
	for i := range relax {
		relax[i] = f.Fresh("relax")
	}
	s := solver.New(f)
	for i, a := range r.Assumptions {
		s.AddClause(relax[i].Neg(), a.Lit)
	}

	const maxRepairs = 8
	var out []Repair
	for len(out) < maxRepairs {
		// Nothing left to find once the blocking clauses are unsatisfiable.
		if s.Solve().Status != solver.SAT {
			break
		}
		var keep []cnf.Lit
		dropped := map[int]bool{}
		for _, i := range order {
			if s.Solve(append(keep, relax[i])...).Status == solver.SAT {
				keep = append(keep, relax[i])
				continue
			}
			dropped[i] = true
		}
		if len(dropped) == 0 {
			break // should not happen: the whole set is unsatisfiable
		}
		out = append(out, repairFor(r.Assumptions, dropped, owner, policy))
		// Block this correction set: at least one of its members must be
		// kept next time, so the next round finds a different one.
		block := make([]cnf.Lit, 0, len(dropped))
		for i := range dropped {
			block = append(block, relax[i])
		}
		if !s.AddClause(block...) {
			break
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost < out[j].Cost
		}
		return out[i].Changes < out[j].Changes
	})
	return out
}

// repairFor turns a correction set into fragment-level advice. A fragment
// every one of whose stated symbols is in the set is dropped; anything else is
// named symbol by symbol, because dropping the fragment that states it would
// change more than the repair needs.
func repairFor(all []Assumption, dropped map[int]bool, owner map[string]lang.ID, policy lang.Policy) Repair {
	stated := map[lang.ID]int{}
	hit := map[lang.ID]int{}
	for i, a := range all {
		id, ok := owner[a.Symbol]
		if !ok {
			continue
		}
		stated[id]++
		if dropped[i] {
			hit[id]++
		}
	}
	rep := Repair{Changes: len(dropped)}
	var drops []lang.ID
	for id, n := range hit {
		if n == stated[id] {
			drops = append(drops, id)
		}
	}
	sort.Slice(drops, func(i, j int) bool { return drops[i].String() < drops[j].String() })
	isDropped := map[lang.ID]bool{}
	for _, id := range drops {
		isDropped[id] = true
	}
	for i, a := range all {
		if !dropped[i] {
			continue
		}
		if id, ok := owner[a.Symbol]; ok && isDropped[id] {
			continue
		}
		rep.Flip = append(rep.Flip, a)
		if _, ok := owner[a.Symbol]; !ok {
			rep.Note = "no single fragment owns this symbol"
		}
	}
	rep.Drop = drops
	rep.Cost = cost(rep, policy)
	return rep
}

// keepRank orders what a repair should try hardest not to touch. Lower is
// offered first, and what is offered first is kept.
func keepRank(id lang.ID, p lang.Policy) int {
	rank := 2
	switch id.Kind {
	case lang.Target:
		rank = 1
	case lang.Profile:
		rank = 1
	case lang.Feature:
		// Dropping an optional feature is cheaper than rebuilding userspace.
		rank = 3
	}
	for _, k := range p.Keep {
		if (k == "target" && id.Kind == lang.Target) ||
			(k == "profile" && id.Kind == lang.Profile) ||
			(k == "toolchain" && id.Kind == lang.Profile) {
			rank = 0
		}
	}
	return rank
}

// cost scores a repair against the declared policy. Only statically known
// quantities appear: changed-symbol counts and explicit keeps. Image size and
// boot time are observed outputs and are not eligible (DESIGN.md §9).
func cost(r Repair, p lang.Policy) int {
	c := r.Changes
	for _, d := range r.Drop {
		for _, k := range p.Keep {
			if (k == "target" && d.Kind == lang.Target) ||
				(k == "profile" && d.Kind == lang.Profile) {
				c += 1000 // keep means strongly prefer not to touch, not forbid
			}
		}
		if d.Kind == lang.Feature {
			c--
		}
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
		return "\n  No repair found: the tree rejects this composition for reasons no\n" +
			"  stated constraint can undo.\n"
	}
	var b strings.Builder
	b.WriteString("\nRepairs, ranked:\n")
	for i, r := range reps {
		fmt.Fprintf(&b, "  %d. %-44s %d change(s)", i+1, r.String(), r.Changes)
		if r.Note != "" {
			fmt.Fprintf(&b, "  — %s", r.Note)
		}
		b.WriteByte('\n')
		for _, f := range r.Flip {
			fmt.Fprintf(&b, "       %s = %s  stated by %s at %s\n", f.Symbol, f.Shown(), f.From, f.Pos)
		}
	}
	return b.String()
}
