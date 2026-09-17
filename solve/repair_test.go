package solve

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/cnf"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/solver"
)

// Repairs are stated in fragments, not symbols. "Drop feature:camera" is
// actionable; "set BR2_PACKAGE_LIBCAMERA=n" leaves the reader to work out which
// fragment said it.
func TestRepairsNameFragments(t *testing.T) {
	r := setup(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_STATIC)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f))`)

	reps := Repairs(r, r.Formula(), r.Owners(), lang.Policy{})
	if len(reps) != 2 {
		t.Fatalf("want 2 repairs, got %d: %+v", len(reps), reps)
	}
	// Dropping the optional feature should rank above rebuilding userspace.
	if len(reps[0].Drop) != 1 || reps[0].Drop[0].Kind != lang.Feature {
		t.Errorf("a feature should outrank a profile: %+v", reps)
	}
	s := ExplainRepairs(reps)
	if !strings.Contains(s, "drop feature:f") || !strings.Contains(s, "drop profile:p") {
		t.Errorf("explanation wrong:\n%s", s)
	}
}

// (keep profile) must push profile-dropping repairs down the list.
func TestPolicyKeepRanks(t *testing.T) {
	r := setup(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_STATIC)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f))`)

	reps := Repairs(r, r.Formula(), r.Owners(), lang.Policy{Keep: []string{"profile"}})
	last := reps[len(reps)-1]
	if len(last.Drop) != 1 || last.Drop[0].Kind != lang.Profile {
		t.Errorf("(keep profile) should rank profile-dropping last: %+v", reps)
	}
}

// A candidate is verified, not assumed: breaking one unsatisfiable subset does
// not make the formula satisfiable when others overlap.
func TestOverlappingConflictsYieldNoSingleRepair(t *testing.T) {
	kc := `
config BR2_ARCH
	bool "arch"
config BR2_A
	bool "a"
config BR2_B
	bool "b"
	depends on !BR2_A
config BR2_C
	bool "c"
	depends on !BR2_A
`
	r := setup(t, kc, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_A)))`,
		`(fragment feature:f (buildroot (y BR2_B)))`,
		`(fragment feature:g (buildroot (y BR2_C)))`,
	}, `(image i (compose target:t profile:p feature:f feature:g))`)

	reps := Repairs(r, r.Formula(), r.Owners(), lang.Policy{})
	if len(reps) < 2 {
		t.Fatalf("want both correction sets, got %d: %+v", len(reps), reps)
	}
	got := map[string]int{}
	for _, rep := range reps {
		got[rep.String()] = rep.Changes
	}
	// Two conflicts overlap on BR2_A. Dropping either feature alone leaves
	// the other, so the correction sets are the profile, or both features.
	if _, ok := got["drop profile:p"]; !ok {
		t.Errorf("dropping the profile fixes both conflicts: %v", got)
	}
	if n, ok := got["drop feature:f + drop feature:g"]; !ok || n != 2 {
		t.Errorf("want the two-feature combination as one repair: %v", got)
	}
	for k := range got {
		if k == "drop feature:f" || k == "drop feature:g" {
			t.Errorf("dropping one feature leaves the other conflicting: %v", got)
		}
	}
	s := ExplainRepairs(reps)
	if !strings.Contains(s, "drop feature:f + drop feature:g") {
		t.Errorf("the combination must be named, not described:\n%s", s)
	}
}

// A conflict with a symbol no fragment owns — an image override — is named
// symbol by symbol, since there is no fragment to drop.
func TestRepairNamesSymbolsWithNoOwner(t *testing.T) {
	r := setup(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f)
	     (override (y buildroot:BR2_STATIC)))`)

	reps := Repairs(r, r.Formula(), r.Owners(), lang.Policy{})
	if len(reps) == 0 {
		t.Fatal("no repair")
	}
	var found *Repair
	for i := range reps {
		if len(reps[i].Flip) > 0 {
			found = &reps[i]
		}
	}
	if found == nil || found.Flip[0].Symbol != "BR2_STATIC" {
		t.Fatalf("the unowned symbol should be named: %+v", reps)
	}
	if !strings.Contains(ExplainRepairs(reps), "unset BR2_STATIC") ||
		!strings.Contains(ExplainRepairs(reps), "no single fragment owns") {
		t.Errorf("explanation:\n%s", ExplainRepairs(reps))
	}
}

// Every proposed repair must actually work: with its members dropped, the
// rest of the composition solves. And it must be minimal — putting any one
// member back leaves it unsatisfiable.
func TestRepairsAreMinimalAndSufficient(t *testing.T) {
	kc := `
config BR2_ARCH
	bool "arch"
config BR2_A
	bool "a"
config BR2_B
	bool "b"
	depends on !BR2_A
config BR2_C
	bool "c"
	depends on !BR2_A
config BR2_D
	bool "d"
	depends on !BR2_B
`
	r := setup(t, kc, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_A)))`,
		`(fragment feature:f (buildroot (y BR2_B)))`,
		`(fragment feature:g (buildroot (y BR2_C)))`,
		`(fragment feature:h (buildroot (y BR2_D)))`,
	}, `(image i (compose target:t profile:p feature:f feature:g feature:h))`)

	reps := Repairs(r, r.Formula(), r.Owners(), lang.Policy{})
	if len(reps) == 0 {
		t.Fatal("no repairs")
	}
	for _, rep := range reps {
		removed := map[string]bool{}
		for _, d := range rep.Drop {
			for _, o := range r.Owners() {
				if o.ID == d {
					removed[o.Symbol] = true
				}
			}
		}
		for _, f := range rep.Flip {
			removed[f.Symbol] = true
		}
		var rest []cnf.Lit
		for _, a := range r.Assumptions {
			if !removed[a.Symbol] {
				rest = append(rest, a.Lit)
			}
		}
		if solver.New(r.Formula()).Solve(rest...).Status != solver.SAT {
			t.Errorf("%s does not fix the composition", rep)
		}
		// Minimal: put back any one dropped symbol and it fails again.
		for sym := range removed {
			back := rest
			for _, a := range r.Assumptions {
				if a.Symbol == sym {
					back = append(append([]cnf.Lit(nil), rest...), a.Lit)
				}
			}
			if solver.New(r.Formula()).Solve(back...).Status == solver.SAT {
				t.Errorf("%s is not minimal: keeping %s would have been enough", rep, sym)
			}
		}
	}
}
