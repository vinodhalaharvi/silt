package lang

import "testing"

func parse(t *testing.T, src string) *File {
	t.Helper()
	f, err := ParseFile(src, "t.sx")
	if err != nil {
		t.Fatalf("parse: %v\nsrc: %s", err, src)
	}
	return f
}

func rejects(t *testing.T, src, want string) {
	t.Helper()
	_, err := ParseFile(src, "t.sx")
	if err == nil {
		t.Fatalf("expected rejection of: %s", src)
	}
	if want != "" && !contains(err.Error(), want) {
		t.Errorf("error %q does not mention %q", err, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestFragmentRoundTrip(t *testing.T) {
	f := parse(t, `
(fragment target:x
  (doc "a target")
  (buildroot (y BR2_aarch64) (value BR2_S "v"))
  (linux (at-least m CONFIG_MAC80211) (prefer n CONFIG_DEBUG_KERNEL))
  (provides (capability mmu)))`)
	fr := f.Fragments[0]
	if fr.ID.String() != "target:x" || fr.Doc != "a target" {
		t.Fatalf("got %v %q", fr.ID, fr.Doc)
	}
	if len(fr.Constraints[Buildroot]) != 2 || len(fr.Constraints[Linux]) != 2 {
		t.Fatalf("constraint counts: %v", fr.Constraints)
	}
	lx := fr.Constraints[Linux]
	if !lx[0].AtLeast || lx[0].Want != M {
		t.Errorf("at-least not parsed: %+v", lx[0])
	}
	if !lx[1].Soft {
		t.Errorf("prefer not marked soft: %+v", lx[1])
	}
}

// Symbols must live in the right tree. A BR2_ symbol in a linux scope is
// always a mistake and is far cheaper to catch here than as a fixpoint diff.
func TestScopeEnforcement(t *testing.T) {
	rejects(t, `(fragment target:x (linux (y BR2_aarch64)))`, "Buildroot symbol")
	rejects(t, `(fragment target:x (buildroot (y CONFIG_ARM64)))`, "Linux symbol")
	rejects(t, `(fragment target:x (buildroot (y NOT_A_SYMBOL)))`, "BR2_ or CONFIG_")
}

// Targets and profiles provide; features only require.
//
// A feature that could provide would let one feature satisfy another's
// requirement, making composition order-dependent — which is the property the
// whole merge model depends on not having.
func TestCapabilityDirection(t *testing.T) {
	rejects(t, `(fragment feature:f (provides (capability mmu)))`, "a feature may not provide")
	rejects(t, `(fragment target:t (requires (capability mmu)))`, "does not require")
	parse(t, `(fragment profile:p (provides (capability dhcp-client)))`)
}

// The two ambiguities GRAMMAR.md resolved must stay resolved.
func TestResolvedAmbiguities(t *testing.T) {
	rejects(t, `(image i (compose target:t profile:p) (repair-policy (prefer preserve-target)))`,
		"(keep target)")
	rejects(t, `(fragment feature:f (buildroot (require BR2_X)))`, "unknown form")
}

// Profiles are exclusive, features are additive. Two profiles is not a solver
// problem to discover later; it is a shape error now.
func TestComposeShape(t *testing.T) {
	rejects(t, `(image i (compose target:t profile:a profile:b))`, "mutually exclusive")
	rejects(t, `(image i (compose profile:p))`, "exactly one target")
	rejects(t, `(image i (compose target:t))`, "exactly one profile")
	parse(t, `(image i (compose target:t profile:p feature:a feature:b feature:c))`)
}

func TestRulesHoldOnlyGuards(t *testing.T) {
	rejects(t, `(rules r (y BR2_X))`, "only (when")
	f := parse(t, `(rules cross-tree (when (y BR2_PACKAGE_DHCPCD) (y CONFIG_PACKET)))`)
	g := f.Rules[0].Guards[0]
	if g.Cond.C.Symbol != "BR2_PACKAGE_DHCPCD" || g.Then[0].Symbol != "CONFIG_PACKET" {
		t.Fatalf("cross-tree guard not parsed: %+v", g)
	}
}

func TestConditions(t *testing.T) {
	f := parse(t, `(rules r
	  (when (and (y BR2_A) (or (set? BR2_S) (not (n BR2_B)))) (y CONFIG_X)))`)
	c := f.Rules[0].Guards[0].Cond
	if c.Op != "and" || len(c.Args) != 2 || c.Args[1].Op != "or" {
		t.Fatalf("condition tree wrong: %+v", c)
	}
	if c.Args[1].Args[0].Op != "set?" || c.Args[1].Args[1].Op != "not" {
		t.Fatalf("nested condition wrong: %+v", c.Args[1])
	}
}

func TestSoftCannotGuard(t *testing.T) {
	rejects(t, `(rules r (when (prefer y BR2_A) (y CONFIG_X)))`, "soft constraint cannot be a condition")
}

func TestImageDeclarations(t *testing.T) {
	f := parse(t, `
(image i
  (compose target:t profile:p)
  (linux (custom-version "6.18.7"))
  (opaque (value BR2_GLOBAL_PATCH_DIR "board/x"))
  (unmanaged BR2_TARGET_UBOOT_*)
  (delegate uboot (custom-config-file "board/x/uboot.config"))
  (environment (value BR2_HOST_GCC_VERSION "13 2"))
  (repair-policy (minimize changed-symbols) (keep target) (baseline "b")))`)
	im := f.Images[0]
	if im.Version != "6.18.7" || len(im.Opaque) != 1 || len(im.Unmanaged) != 1 ||
		len(im.Delegate) != 1 || len(im.Environment) != 1 {
		t.Fatalf("image clauses: %+v", im)
	}
	if im.Policy.Baseline != "b" || len(im.Policy.Keep) != 1 {
		t.Fatalf("policy: %+v", im.Policy)
	}
}

func TestCustomVersionOnlyInLinux(t *testing.T) {
	rejects(t, `(fragment target:t (buildroot (custom-version "6.1")))`, "only in the linux scope")
}

func TestUnknownFormsRejected(t *testing.T) {
	rejects(t, `(build (make "-j16"))`, "unknown top-level form")
	rejects(t, `(fragment target:t (recipe (configure "x")))`, "unknown clause")
	rejects(t, `(image i (compose target:t profile:p) (node (cpu 12)))`, "unknown clause")
}

// Diagnostics must name a file and line: Invariant 3 applied to errors.
func TestErrorsCarryPosition(t *testing.T) {
	_, err := ParseFile("\n\n(fragment target:x\n  (linux (y BR2_aarch64)))", "f.sx")
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "f.sx:4") {
		t.Errorf("error lacks position: %v", err)
	}
}

func TestCapabilityDeclarations(t *testing.T) {
	f := parse(t, `
(capabilities
  (capability mmu (doc "has an MMU") (symbol BR2_USE_MMU))
  (capability dhcp-client (doc "something does DHCP")))`)
	d := f.Capabilities[0].Decls
	if len(d) != 2 {
		t.Fatalf("got %d decls", len(d))
	}
	if d[0].Symbol != "BR2_USE_MMU" || d[0].Doc != "has an MMU" {
		t.Errorf("mmu: %+v", d[0])
	}
	// A capability need not correspond to any symbol. dhcp-client is an
	// outcome two profiles satisfy by different means.
	if d[1].Symbol != "" {
		t.Errorf("dhcp-client should have no symbol: %+v", d[1])
	}
	rejects(t, `(capabilities (capability x (nonsense "y")))`, "unknown capability clause")
}
