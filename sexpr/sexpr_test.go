package sexpr

import "testing"

func mustParse(t *testing.T, src string) *Node {
	t.Helper()
	n, err := ParseOne(src, "test.sx")
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return n
}

func TestAtomKinds(t *testing.T) {
	n := mustParse(t, `(value BR2_X "120M" 42 -7)`)
	want := []Kind{KindSymbol, KindSymbol, KindString, KindInt, KindInt}
	if len(n.Items) != len(want) {
		t.Fatalf("got %d items, want %d", len(n.Items), len(want))
	}
	for i, k := range want {
		if n.Items[i].Kind != k {
			t.Errorf("item %d: got %v, want %v", i, n.Items[i].Kind, k)
		}
	}
}

// A string-typed "42" must stay distinct from an integer 42. Collapsing atoms
// into one string type loses this, and the typed frontend depends on it.
func TestStringIntNotConflated(t *testing.T) {
	a := mustParse(t, `(v "42")`)
	b := mustParse(t, `(v 42)`)
	if Hash(a) == Hash(b) {
		t.Fatal(`(v "42") and (v 42) must not hash alike`)
	}
}

func TestProvenance(t *testing.T) {
	ns, err := Parse("(a)\n\n  (b\n    (c))", "f.sx")
	if err != nil {
		t.Fatal(err)
	}
	if got := ns[0].Pos.Short(); got != "f.sx:1" {
		t.Errorf("first form: got %s want f.sx:1", got)
	}
	if got := ns[1].Pos.Short(); got != "f.sx:3" {
		t.Errorf("second form: got %s want f.sx:3", got)
	}
	if got := ns[1].Items[1].Pos.Short(); got != "f.sx:4" {
		t.Errorf("nested form: got %s want f.sx:4", got)
	}
}

// Rung 1's stated success criterion: two differently-written but equivalent
// expressions hash identically, and canonical round-trip is stable.
func TestCanonicalEquivalence(t *testing.T) {
	cases := []struct{ a, b string }{
		{ // whitespace and comments are incidental
			"(fragment f (buildroot (y BR2_aarch64) (n BR2_ENABLE_DEBUG)))",
			`(fragment f   ; a comment
			   (buildroot
			     (y BR2_aarch64)
			     (n BR2_ENABLE_DEBUG)))`,
		},
		{ // constraint order within a scope is not meaning
			"(fragment f (buildroot (y BR2_aarch64) (n BR2_ENABLE_DEBUG)))",
			"(fragment f (buildroot (n BR2_ENABLE_DEBUG) (y BR2_aarch64)))",
		},
		{ // capability order is not meaning
			"(provides (capability mmu) (capability virtio))",
			"(provides (capability virtio) (capability mmu))",
		},
	}
	for _, c := range cases {
		ha, hb := Hash(mustParse(t, c.a)), Hash(mustParse(t, c.b))
		if ha != hb {
			t.Errorf("should hash alike:\n  %s\n  %s\n  %s\n  %s",
				c.a, c.b, Canonical(mustParse(t, c.a)), Canonical(mustParse(t, c.b)))
		}
	}
}

// Order IS meaning in some forms. Sorting everything would be wrong.
func TestOrderPreservedWhereItMatters(t *testing.T) {
	a := mustParse(t, "(when (y BR2_A) (y CONFIG_B))")
	b := mustParse(t, "(when (y CONFIG_B) (y BR2_A))")
	if Hash(a) == Hash(b) {
		t.Fatal("when: condition position is significant, must not be sorted")
	}
}

func TestCanonicalIsStable(t *testing.T) {
	src := `(fragment target:x   ; comment
	  (buildroot (value BR2_S "a\"b") (y BR2_Z) (y BR2_A)))`
	once := Canonical(mustParse(t, src))
	twice := Canonical(mustParse(t, once))
	if once != twice {
		t.Errorf("round trip not stable:\n  %s\n  %s", once, twice)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{`(a`, `a)`, `(a "unterminated`, "(a \"nl\nin string\")"} {
		if _, err := Parse(src, "t.sx"); err == nil {
			t.Errorf("expected error for %q", src)
		}
	}
}

func TestHeadAndArgs(t *testing.T) {
	n := mustParse(t, `(y BR2_X)`)
	if n.Head() != "y" {
		t.Errorf("Head = %q", n.Head())
	}
	if len(n.Args()) != 1 || n.Args()[0].Text != "BR2_X" {
		t.Errorf("Args = %v", n.Args())
	}
	if mustParse(t, `("notasymbol" x)`).Head() != "" {
		t.Error("Head must be empty when first item is not a symbol")
	}
}
