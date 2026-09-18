package compose

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
)

const vocab = `(capabilities
  (capability mmu)
  (capability remote-shell
    (doc "Something in the image accepts an interactive login over the network.")
    (symbol buildroot:BR2_PACKAGE_DROPBEAR)
    (symbol buildroot:BR2_PACKAGE_OPENSSH))
  (capability unbound-thing))`

const tgt2 = `(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`

// A composition that both forbids and provides a capability is an error naming
// both ends, not a silent win for whichever came last.
func TestForbidsVersusProvides(t *testing.T) {
	l := lib(t, vocab, tgt2,
		`(fragment profile:hardened (requires (capability mmu))
		   (forbids (capability remote-shell)))`,
		// A profile provides it — features may not provide, so the collision
		// a hardened profile actually faces is with another profile or a
		// target, and compose already forbids two profiles.
		`(fragment target:shell (buildroot (y BR2_aarch64))
		   (provides (capability mmu) (capability remote-shell)))`)
	_, err := composeSrc(t, l, `(image i (compose target:shell profile:hardened))`)
	if err == nil {
		t.Fatal("expected a forbids violation")
	}
	for _, want := range []string{"remote-shell", "profile:hardened", "target:shell", "forbids"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q:\n%s", want, err)
		}
	}
}

// Requiring what the composition forbids is named as the contradiction it is,
// rather than surfacing as a missing provider.
func TestForbidsVersusRequires(t *testing.T) {
	l := lib(t, vocab, tgt2,
		`(fragment profile:hardened (requires (capability mmu))
		   (forbids (capability remote-shell)))`,
		`(fragment feature:admin (requires (capability remote-shell)))`)
	_, err := composeSrc(t, l, `(image i (compose target:t profile:hardened feature:admin))`)
	if err == nil || !strings.Contains(err.Error(), "which profile:hardened forbids") {
		t.Fatalf("got %v", err)
	}
}

// The check that protects the customer: a forbidden capability's symbol on in
// the *predicted* configuration, whether or not any fragment declared it.
//
// A package pulled in by somebody else's select provides the capability just as
// effectively as one a fragment named, and says nothing.
func TestForbiddenSymbolInPrediction(t *testing.T) {
	l := lib(t, vocab, tgt2,
		`(fragment profile:hardened (requires (capability mmu))
		   (forbids (capability remote-shell)))`)
	r, err := composeSrc(t, l, `(image i (compose target:t profile:hardened))`)
	if err != nil {
		t.Fatal(err)
	}

	// Nothing on: clean.
	if v := r.CheckForbidden(l.Declared, func(lang.SymbolID) bool { return false }); len(v) != 0 {
		t.Fatalf("unexpected violations: %v", v)
	}

	// openssh on, which no fragment mentioned.
	on := func(s lang.SymbolID) bool { return s.Name == "BR2_PACKAGE_OPENSSH" }
	v := r.CheckForbidden(l.Declared, on)
	if len(v) != 1 || v[0].Symbol.Name != "BR2_PACKAGE_OPENSSH" {
		t.Fatalf("got %+v", v)
	}
	if !strings.Contains(ExplainViolations(v), "profile:hardened") {
		t.Errorf("explanation should name the forbidder:\n%s", ExplainViolations(v))
	}
}

// Every bound symbol is checked, not just the first. A capability with one
// symbol bound and three implementations gives an absence claim that reads as
// verified and is not.
func TestAllBoundSymbolsAreChecked(t *testing.T) {
	l := lib(t, vocab, tgt2,
		`(fragment profile:hardened (requires (capability mmu))
		   (forbids (capability remote-shell)))`)
	r, _ := composeSrc(t, l, `(image i (compose target:t profile:hardened))`)
	on := func(s lang.SymbolID) bool { return s.Name == "BR2_PACKAGE_DROPBEAR" }
	if v := r.CheckForbidden(l.Declared, on); len(v) != 1 {
		t.Fatalf("the second bound symbol must be checked too: %+v", v)
	}
}

// A forbidden capability that names no symbols cannot be checked against a
// configuration. Saying so is the difference between verified and merely
// looking it.
func TestUnboundForbidIsReported(t *testing.T) {
	l := lib(t, vocab, tgt2,
		`(fragment profile:p (requires (capability mmu))
		   (forbids (capability unbound-thing)))`)
	r, err := composeSrc(t, l, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	u := r.Unbound(l.Declared)
	if len(u) != 1 || u[0] != "unbound-thing" {
		t.Fatalf("got %v", u)
	}
}

// Forbidding an undeclared capability is a typo, caught like any other.
func TestForbidsMustBeDeclared(t *testing.T) {
	l := lib(t, vocab, tgt2,
		`(fragment profile:p (requires (capability mmu)) (forbids (capability remote-shel)))`)
	_, err := composeSrc(t, l, `(image i (compose target:t profile:p))`)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("got %v", err)
	}
}
