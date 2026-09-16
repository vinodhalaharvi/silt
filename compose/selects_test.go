package compose

import (
	"os"
	"testing"

	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

func treeFrom(t *testing.T, src string) *kconfig.Tree {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/Config.in", []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	tr, err := kconfig.Load("Config.in", kconfig.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// The gap this closes: a rule whose antecedent is a symbol nothing states, but
// which Kconfig's select machinery will enable anyway. BR2_INIT_SYSTEMD selects
// BR2_PACKAGE_SYSTEMD, so a rule on the latter was true of every built image
// and invisible to Silt.
func TestRuleFiresOnSelectedSymbol(t *testing.T) {
	tr := treeFrom(t, `
config BR2_ARCH
	bool "arch"
config BR2_INIT_SYSTEMD
	bool "systemd"
	select BR2_PACKAGE_SYSTEMD
config BR2_PACKAGE_SYSTEMD
	bool "systemd pkg"
`)
	l := lib(t,
		`(fragment target:t (buildroot (y BR2_ARCH)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_INIT_SYSTEMD)))`,
		`(rules x (when (y BR2_PACKAGE_SYSTEMD) (n CONFIG_SYSFS_DEPRECATED)))`)

	if r, err := composeSrc(t, l, `(image i (compose target:t profile:p))`); err != nil {
		t.Fatal(err)
	} else if len(r.Derived) != 0 {
		t.Fatalf("without a tree the rule must not fire: %+v", r.Derived)
	}

	l.Tree = tr
	r, err := composeSrc(t, l, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Derived) != 1 || r.Derived[0].Constraint.Symbol != "CONFIG_SYSFS_DEPRECATED" {
		t.Fatalf("rule did not fire on the selected symbol: %+v", r.Derived)
	}
	if r.Selected["BR2_PACKAGE_SYSTEMD"] != "BR2_INIT_SYSTEMD" {
		t.Errorf("selector not recorded: %+v", r.Selected)
	}
}

// Selects are transitive, and Kconfig follows them without visiting the
// target's own dependencies.
func TestSelectClosureIsTransitive(t *testing.T) {
	tr := treeFrom(t, `
config BR2_A
	bool "a"
	select BR2_B
config BR2_B
	bool "b"
	select BR2_C
config BR2_C
	bool "c"
	depends on BR2_NEVER_SET
`)
	got := selectClosure(tr, map[string]bool{"BR2_A": true})
	if got["BR2_B"] != "BR2_A" || got["BR2_C"] != "BR2_B" {
		t.Fatalf("closure wrong: %+v", got)
	}
}

// A select guarded by a condition the composition refutes does not fire.
func TestRefutedSelectDoesNotFire(t *testing.T) {
	tr := treeFrom(t, `
config BR2_A
	bool "a"
	select BR2_B if BR2_GUARD
config BR2_B
	bool "b"
config BR2_GUARD
	bool "guard"
`)
	if got := selectClosure(tr, map[string]bool{"BR2_A": true, "BR2_GUARD": false}); len(got) != 0 {
		t.Fatalf("refuted select fired: %+v", got)
	}
	if got := selectClosure(tr, map[string]bool{"BR2_A": true, "BR2_GUARD": true}); got["BR2_B"] == "" {
		t.Fatalf("satisfied select did not fire: %+v", got)
	}
}

// A selected symbol must not be emitted: kbuild produces it, and restating a
// select is what the derivation rule forbids.
func TestSelectedSymbolsAreNotEmitted(t *testing.T) {
	tr := treeFrom(t, `
config BR2_ARCH
	bool "arch"
config BR2_A
	bool "a"
	select BR2_B
config BR2_B
	bool "b"
`)
	l := lib(t,
		`(fragment target:t (buildroot (y BR2_ARCH)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_A)))`,
		`(rules x (when (y BR2_B) (y BR2_B)))`)
	l.Tree = tr
	r, err := composeSrc(t, l, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Constraints[lang.Buildroot] {
		if c.Symbol == "BR2_B" {
			t.Fatalf("select-implied symbol was emitted: %+v", c)
		}
	}
}
