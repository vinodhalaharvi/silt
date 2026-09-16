package solve

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
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
	if reps[0].Drop.Kind != lang.Feature {
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
	if reps[len(reps)-1].Drop.Kind != lang.Profile {
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
	for _, rep := range reps {
		if rep.Drop.Kind == lang.Feature {
			t.Errorf("dropping one feature leaves the other conflicting: %+v", rep)
		}
	}
	if len(reps) == 0 && !strings.Contains(ExplainRepairs(reps), "hitting sets") {
		t.Error("with no single repair, say why rather than nothing")
	}
}
