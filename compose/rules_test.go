package compose

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
)

const tgt = `(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`
const prof = `(fragment profile:p (requires (capability mmu)))`

func run(t *testing.T, srcs []string, image string) (*Result, error) {
	t.Helper()
	l := NewLibrary()
	for i, s := range srcs {
		f, err := lang.ParseFile(s, "f"+string(rune('0'+i))+".sx")
		if err != nil {
			t.Fatalf("parse f%d: %v", i, err)
		}
		if err := l.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	f, err := lang.ParseFile(image, "img.sx")
	if err != nil {
		t.Fatal(err)
	}
	return l.Compose(f.Images[0])
}

func find(r *Result, sc lang.Scope, sym string) (lang.Constraint, bool) {
	for _, c := range r.Constraints[sc] {
		if c.Symbol == sym && !c.Soft {
			return c, true
		}
	}
	return lang.Constraint{}, false
}

// The central case: a Buildroot package implies a Linux symbol. Neither
// Kconfig tree can state this, which is why Buildroot puts it in help text and
// checks it nowhere.
func TestCrossTreeRuleFires(t *testing.T) {
	r, err := run(t, []string{tgt, prof,
		`(fragment feature:net (buildroot (y BR2_PACKAGE_DHCPCD)))`,
		`(rules x (when (y BR2_PACKAGE_DHCPCD) (y CONFIG_PACKET)))`,
	}, `(image i (compose target:t profile:p feature:net))`)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := find(r, lang.Linux, "CONFIG_PACKET")
	if !ok || c.Want != lang.Y {
		t.Fatalf("rule did not fire: %+v", r.Constraints[lang.Linux])
	}
	if len(r.Derived) != 1 || r.Derived[0].Constraint.Symbol != "CONFIG_PACKET" {
		t.Fatalf("derivation not recorded: %+v", r.Derived)
	}
}

// A rule must not fire on a symbol nobody asked for.
func TestRuleDoesNotFireWithoutAntecedent(t *testing.T) {
	r, err := run(t, []string{tgt, prof,
		`(rules x (when (y BR2_PACKAGE_DHCPCD) (y CONFIG_PACKET)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(r, lang.Linux, "CONFIG_PACKET"); ok {
		t.Fatal("rule fired with no antecedent")
	}
}

// One rule satisfying another's condition must cascade.
func TestRulesCascade(t *testing.T) {
	r, err := run(t, []string{tgt, prof,
		`(fragment feature:a (buildroot (y BR2_PACKAGE_DHCPCD)))`,
		// deliberately reversed, so the cascade needs a second pass
		`(rules x
		   (when (y BR2_PACKAGE_IW)     (y CONFIG_MAC80211))
		   (when (y BR2_PACKAGE_DHCPCD) (y BR2_PACKAGE_IW)))`,
	}, `(image i (compose target:t profile:p feature:a))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(r, lang.Linux, "CONFIG_MAC80211"); !ok {
		t.Fatalf("second-order rule did not fire: %+v", r.Derived)
	}
	if len(r.Derived) != 2 {
		t.Fatalf("want 2 derivations, got %d", len(r.Derived))
	}
	// Both may land in round 1: a rule's output is visible to later rules
	// within the same pass, so a favourably ordered cascade converges in one.
	// Reverse the rule order and it needs two.
	if _, ok := find(r, lang.Buildroot, "BR2_PACKAGE_IW"); !ok {
		t.Error("first-order derivation missing")
	}
}

// A rule contradicting stated intent is an error naming both, not a silent
// override. Silently winning would be merge_config.sh in different syntax.
func TestRuleConflictIsAnError(t *testing.T) {
	_, err := run(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (n BR2_PACKAGE_SYSTEMD)))`,
		`(fragment feature:a (buildroot (y BR2_INIT_SYSTEMD)))`,
		`(rules x (when (y BR2_INIT_SYSTEMD) (y BR2_PACKAGE_SYSTEMD)))`,
	}, `(image i (compose target:t profile:p feature:a))`)
	if err == nil {
		t.Fatal("expected a rule conflict")
	}
	for _, want := range []string{"BR2_PACKAGE_SYSTEMD", "stated n", "f1.sx:1", "f3.sx:1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q:\n%s", want, err)
		}
	}
}

// Conditions are three-valued. An unstated symbol is unknown, never false, so
// a negated condition must not fire on absence alone — deciding that needs the
// completed model.
func TestNegationDoesNotFireOnAbsence(t *testing.T) {
	r, err := run(t, []string{tgt, prof,
		`(rules x (when (not (y BR2_STATIC_LIBS)) (y CONFIG_PACKET)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(r, lang.Linux, "CONFIG_PACKET"); ok {
		t.Fatal("negated condition fired on an unstated symbol")
	}
}

func TestNegationFiresOnContradiction(t *testing.T) {
	r, err := run(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (n BR2_STATIC_LIBS)))`,
		`(rules x (when (not (y BR2_STATIC_LIBS)) (y CONFIG_PACKET)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(r, lang.Linux, "CONFIG_PACKET"); !ok {
		t.Fatal("negation should hold when the symbol is stated n")
	}
}

func TestSetPredicate(t *testing.T) {
	r, err := run(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu))
		   (buildroot (value BR2_TARGET_OPENSBI_PLAT "generic")))`,
		`(rules x (when (set? BR2_TARGET_OPENSBI_PLAT) (y CONFIG_PACKET)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(r, lang.Linux, "CONFIG_PACKET"); !ok {
		t.Fatal("set? did not hold for a non-empty value")
	}
}

func TestAndOr(t *testing.T) {
	r, err := run(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_INIT_BUSYBOX)))`,
		`(rules x
		   (when (and (y BR2_aarch64) (y BR2_INIT_BUSYBOX)) (y CONFIG_PACKET))
		   (when (or  (y BR2_PACKAGE_DHCPCD) (y BR2_INIT_BUSYBOX)) (y CONFIG_INET)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := find(r, lang.Linux, "CONFIG_PACKET"); !ok {
		t.Error("and-condition did not fire")
	}
	if _, ok := find(r, lang.Linux, "CONFIG_INET"); !ok {
		t.Error("or-condition did not fire on its satisfied branch")
	}
}

// A cycle must be reported, not hang the tool.
func TestCycleTerminates(t *testing.T) {
	_, err := run(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_INIT_BUSYBOX)))`,
		`(rules x
		   (when (y BR2_INIT_BUSYBOX)  (at-least m CONFIG_A))
		   (when (at-least m CONFIG_A) (y CONFIG_A)))`,
	}, `(image i (compose target:t profile:p))`)
	// Strengthening m -> y terminates; the point is that it does terminate.
	if err != nil && !strings.Contains(err.Error(), "fixed point") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExplainDerived(t *testing.T) {
	r, err := run(t, []string{tgt, prof,
		`(fragment feature:net (buildroot (y BR2_PACKAGE_DHCPCD)))`,
		`(rules cross-tree (when (y BR2_PACKAGE_DHCPCD) (y CONFIG_PACKET)))`,
	}, `(image i (compose target:t profile:p feature:net))`)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := r.ExplainDerived("CONFIG_PACKET")
	if !ok {
		t.Fatal("no explanation")
	}
	for _, want := range []string{"CONFIG_PACKET", "rules:cross-tree", "(y BR2_PACKAGE_DHCPCD)"} {
		if !strings.Contains(s, want) {
			t.Errorf("explanation missing %q:\n%s", want, s)
		}
	}
}
