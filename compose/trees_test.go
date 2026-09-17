package compose

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
)

const busyboxTree = `(tree busybox (kind kconfig) (prefix "CONFIG_")
  (consumed-by BR2_PACKAGE_BUSYBOX_CONFIG_FRAGMENT_FILES))`

// The bug this whole change exists for. Linux and BusyBox both spell symbols
// CONFIG_, and keyed by name they were one symbol: a linux y and a busybox n
// for CONFIG_DESKTOP reported a conflict, and a rule about one fired on the
// other. Keyed by SymbolID they are unrelated.
func TestSameNameInTwoTreesIsTwoSymbols(t *testing.T) {
	r, err := run(t, []string{busyboxTree, tgt,
		`(fragment profile:p (requires (capability mmu))
		   (linux (y CONFIG_DESKTOP))
		   (scope busybox (n CONFIG_DESKTOP)))`,
		`(rules r (when (y linux:CONFIG_DESKTOP) (y busybox:CONFIG_FEATURE_X)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatalf("two trees' CONFIG_DESKTOP must not conflict: %v", err)
	}
	if c, ok := find(r, lang.Linux, "CONFIG_DESKTOP"); !ok || c.Want != lang.Y {
		t.Errorf("linux: %+v %v", c, ok)
	}
	if c, ok := find(r, "busybox", "CONFIG_DESKTOP"); !ok || c.Want != lang.N {
		t.Errorf("busybox: %+v %v", c, ok)
	}
	// The rule fired on linux:CONFIG_DESKTOP and derived into busybox only.
	if _, ok := find(r, "busybox", "CONFIG_FEATURE_X"); !ok {
		t.Error("cross-tree rule did not derive into busybox")
	}
	if _, ok := find(r, lang.Linux, "CONFIG_FEATURE_X"); ok {
		t.Error("derived constraint leaked into linux")
	}

	// And a rule keyed on the busybox symbol must not fire on the linux one.
	r, err = run(t, []string{busyboxTree, tgt,
		`(fragment profile:p (requires (capability mmu)) (linux (y CONFIG_DESKTOP)))`,
		`(rules r (when (y busybox:CONFIG_DESKTOP) (y linux:CONFIG_WRONG)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Derived) != 0 {
		t.Errorf("a rule on busybox:CONFIG_DESKTOP fired on linux:CONFIG_DESKTOP: %+v", r.Derived)
	}
}

func TestUndeclaredTreeIsAnError(t *testing.T) {
	_, err := run(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (scope uboot (y CONFIG_FIT)))`,
	}, `(image i (compose target:t profile:p))`)
	if err == nil || !strings.Contains(err.Error(), "no tree uboot is declared") {
		t.Fatalf("got %v", err)
	}
	_, err = run(t, []string{tgt, prof,
		`(rules r (when (y buildroot:BR2_aarch64) (y uboot:CONFIG_FIT)))`,
	}, `(image i (compose target:t profile:p))`)
	if err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("got %v", err)
	}
}

// Spelling is checked against the tree the symbol was written for, which is
// the check the parser used to do by inferring the tree from the prefix.
func TestPrefixIsCheckedAgainstTheRegistry(t *testing.T) {
	for _, c := range []struct{ frag, want string }{
		{`(fragment profile:p (requires (capability mmu)) (linux (y BR2_aarch64)))`, "CONFIG_*"},
		{`(fragment profile:p (requires (capability mmu)) (buildroot (y CONFIG_ARM64)))`, "BR2_*"},
	} {
		_, err := run(t, []string{tgt, c.frag}, `(image i (compose target:t profile:p))`)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v", c.frag, err)
		}
	}
	// A tree with no prefix accepts any name: uClibc spells UCLIBC_, and a
	// tree that declares nothing has nothing to check.
	_, err := run(t, []string{`(tree uclibc (kind kconfig) (consumed-by BR2_UCLIBC_CONFIG_FRAGMENT_FILES))`, tgt,
		`(fragment profile:p (requires (capability mmu)) (scope uclibc (y UCLIBC_HAS_THREADS)))`,
	}, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestTreesCannotBeRedeclared(t *testing.T) {
	l := NewLibrary()
	for i, src := range []string{busyboxTree, busyboxTree} {
		f, err := lang.ParseFile(src, "t.sx")
		if err != nil {
			t.Fatal(err)
		}
		err = l.Add(f)
		if i == 1 && (err == nil || !strings.Contains(err.Error(), "redeclared")) {
			t.Fatalf("got %v", err)
		}
	}
	f, _ := lang.ParseFile(`(tree linux (kind kconfig))`, "t.sx")
	if err := l.Add(f); err == nil || !strings.Contains(err.Error(), "built in") {
		t.Fatalf("redeclaring a built-in: %v", err)
	}
}
