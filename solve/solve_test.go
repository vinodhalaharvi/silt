package solve

import (
	"os"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/solver"
)

func setup(t *testing.T, kcfg string, frags []string, image string) *Result {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/Config.in", []byte(kcfg), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	l := compose.NewLibrary()
	l.Tree = tree
	for i, s := range frags {
		f, err := lang.ParseFile(s, "f"+string(rune('0'+i))+".sx")
		if err != nil {
			t.Fatal(err)
		}
		if err := l.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	f, err := lang.ParseFile(image, "img.sx")
	if err != nil {
		t.Fatal(err)
	}
	res, err := l.Compose(f.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	return Solve(res, tree)
}

const kcfg = `
config BR2_ARCH
	bool "arch"
config BR2_STATIC
	bool "static"
config BR2_PKG
	bool "pkg"
	depends on !BR2_STATIC
	select BR2_DEP
config BR2_DEP
	bool "dep"
config BR2_OTHER
	bool "other"
`

const tgt = `(fragment target:t (buildroot (y BR2_ARCH)) (provides (capability mmu)))`

func completeFor(t *testing.T, kcfg string, frags []string, image string) *Completion {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(kcfg), 0o644)
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	l := compose.NewLibrary()
	l.Tree = tree
	for i, s := range frags {
		f, err := lang.ParseFile(s, "f"+string(rune('0'+i))+".sx")
		if err != nil {
			t.Fatal(err)
		}
		l.Add(f)
	}
	f, err := lang.ParseFile(image, "img.sx")
	if err != nil {
		t.Fatal(err)
	}
	res, err := l.Compose(f.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	return Complete(res, tree)
}

func TestSatisfiable(t *testing.T) {
	r := setup(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p))`)
	if r.Status != solver.SAT {
		t.Fatalf("got %v", r.Status)
	}
	// select pulls BR2_DEP in; no fragment states it, so it is implied.
	found := false
	for _, s := range r.Implied {
		if s == "BR2_DEP" {
			found = true
		}
	}
	if !found {
		t.Errorf("selected symbol should appear as implied: %v", r.Implied)
	}
}

// The core claim: an over-constrained composition is narrowed to the few
// assumptions actually to blame, not the whole set.
func TestUnsatIsNarrowed(t *testing.T) {
	r := setup(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_STATIC) (y BR2_OTHER)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f))`)

	if r.Status != solver.UNSAT {
		t.Fatalf("got %v", r.Status)
	}
	if len(r.Culprits) != 2 {
		t.Fatalf("want 2 culprits, got %d: %+v", len(r.Culprits), r.Culprits)
	}
	got := map[string]bool{}
	for _, c := range r.Culprits {
		got[c.Symbol] = true
	}
	if !got["BR2_STATIC"] || !got["BR2_PKG"] {
		t.Errorf("wrong culprits: %+v", r.Culprits)
	}
	// Innocent assumptions must be dropped.
	if got["BR2_ARCH"] || got["BR2_OTHER"] {
		t.Errorf("uninvolved assumptions kept: %+v", r.Culprits)
	}
}

func TestExplainNamesSources(t *testing.T) {
	r := setup(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_STATIC)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f))`)
	s := r.Explain("i")
	for _, want := range []string{"UNSAT", "BR2_STATIC", "BR2_PKG", "profile:p", "feature:f"} {
		if !strings.Contains(s, want) {
			t.Errorf("explanation missing %q:\n%s", want, s)
		}
	}
}

// Symbols absent from the tree are verify's business, not the solver's:
// inventing a variable for one would make the answer meaningless.
func TestUnknownSymbolsAreSkipped(t *testing.T) {
	r := setup(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_NOT_REAL)))`,
	}, `(image i (compose target:t profile:p))`)
	if r.Status != solver.SAT {
		t.Fatalf("got %v", r.Status)
	}
	for _, a := range r.Assumptions {
		if a.Symbol == "BR2_NOT_REAL" {
			t.Error("unknown symbol should not have been assumed")
		}
	}
}

// Completion decides every solvable symbol, not just the stated ones.
func TestCompleteIsTotal(t *testing.T) {
	c := completeFor(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p))`)
	if c.Status != solver.SAT {
		t.Fatalf("got %v", c.Status)
	}
	for _, want := range []string{"BR2_ARCH", "BR2_PKG", "BR2_DEP", "BR2_STATIC", "BR2_OTHER"} {
		if _, ok := c.Values[want]; !ok {
			t.Errorf("%s left undecided", want)
		}
	}
	if !c.Values["BR2_PKG"] || c.Values["BR2_STATIC"] {
		t.Errorf("constraints not respected: %+v", c.Values)
	}
}

// A preference that cannot hold must be reported, never silently dropped —
// that is the defect this project exists to catch.
func TestYieldedPreferencesAreReported(t *testing.T) {
	c := completeFor(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_PKG) (prefer y BR2_STATIC)))`,
	}, `(image i (compose target:t profile:p))`)
	if len(c.Yielded) != 1 || c.Yielded[0].Sym.Name != "BR2_STATIC" {
		t.Fatalf("yielded preference not reported: %+v", c.Yielded)
	}
	if !strings.Contains(c.Report(), "BR2_STATIC") {
		t.Errorf("report omits it:\n%s", c.Report())
	}
}

// A preference that can hold is kept.
func TestSatisfiablePreferenceIsKept(t *testing.T) {
	c := completeFor(t, kcfg, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (prefer y BR2_OTHER)))`,
	}, `(image i (compose target:t profile:p))`)
	if len(c.Yielded) != 0 {
		t.Fatalf("should not have yielded: %+v", c.Yielded)
	}
	if !c.Values["BR2_OTHER"] {
		t.Error("satisfiable preference was not honored")
	}
}
