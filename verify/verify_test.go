package verify

import (
	"os"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

func tree(t *testing.T, src string) *kconfig.Tree {
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

func composed(t *testing.T, frags []string, image string) *compose.Result {
	t.Helper()
	l := compose.NewLibrary()
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
	r, err := l.Compose(f.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const tgt = `(fragment target:t (buildroot (y BR2_ARCH)) (provides (capability mmu)))`

func msgs(r *Report) string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString(f.String() + "\n")
	}
	return b.String()
}

// The failure that cost a rebuild: a symbol that exists upstream but not in the
// release being built against.
func TestUnknownSymbol(t *testing.T) {
	tr := tree(t, "config BR2_ARCH\n\tbool \"arch\"\n")
	r := composed(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_NOT_IN_THIS_RELEASE)))`,
	}, `(image i (compose target:t profile:p))`)

	rep := Check(r, tr, lang.BuiltinTrees()["buildroot"])
	if rep.OK() {
		t.Fatal("expected the unknown symbol to be reported")
	}
	if !strings.Contains(msgs(rep), "does not exist in this buildroot tree") {
		t.Errorf("wrong message:\n%s", msgs(rep))
	}
}

// The checked-in UNSAT fixture. libcamera has depends on !BR2_STATIC_LIBS, the
// minimal profile sets static libs, and compose alone cannot see it because the
// dependency lives in Kconfig rather than in any fragment.
func TestRefutedDependency(t *testing.T) {
	tr := tree(t, `
config BR2_ARCH
	bool "arch"
config BR2_STATIC_LIBS
	bool "static"
config BR2_PKG
	bool "pkg"
	depends on BR2_ARCH && !BR2_STATIC_LIBS
`)
	r := composed(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_STATIC_LIBS)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f))`)

	rep := Check(r, tr, lang.BuiltinTrees()["buildroot"])
	if rep.OK() {
		t.Fatal("expected the refuted dependency to be reported")
	}
	m := msgs(rep)
	for _, want := range []string{"BR2_PKG requires !BR2_STATIC_LIBS", "stated y by profile:p"} {
		if !strings.Contains(m, want) {
			t.Errorf("missing %q:\n%s", want, m)
		}
	}
	// Only the refuted term, not the whole conjunction.
	if strings.Contains(m, "BR2_ARCH &&") {
		t.Errorf("whole depends expression printed; name only the refuted term:\n%s", m)
	}
}

// An unstated symbol is unknown, never false. Reporting it would block
// configurations kbuild satisfies from its own defaults.
func TestUnstatedDependencyIsNotAnError(t *testing.T) {
	tr := tree(t, `
config BR2_ARCH
	bool "arch"
config BR2_PKG
	bool "pkg"
	depends on BR2_SOMETHING_ELSE
`)
	r := composed(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f))`)

	if rep := Check(r, tr, lang.BuiltinTrees()["buildroot"]); !rep.OK() {
		t.Errorf("unstated dependency must not be reported:\n%s", msgs(rep))
	}
}

// A disjunction is refuted only when every branch is.
func TestDisjunctionNeedsBothBranchesRefuted(t *testing.T) {
	tr := tree(t, `
config BR2_ARCH
	bool "arch"
config BR2_A
	bool "a"
config BR2_PKG
	bool "pkg"
	depends on BR2_A || BR2_B
`)
	r := composed(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (n BR2_A)))`,
		`(fragment feature:f (buildroot (y BR2_PKG)))`,
	}, `(image i (compose target:t profile:p feature:f))`)

	if rep := Check(r, tr, lang.BuiltinTrees()["buildroot"]); !rep.OK() {
		t.Errorf("BR2_B is unstated, so the disjunction stands:\n%s", msgs(rep))
	}
}

// Setting a string symbol as a boolean, or vice versa, is dropped by kbuild
// rather than rejected.
func TestTypeMismatch(t *testing.T) {
	tr := tree(t, `
config BR2_ARCH
	bool "arch"
config BR2_NAME
	string "name"
`)
	r := composed(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_NAME)))`,
	}, `(image i (compose target:t profile:p))`)

	rep := Check(r, tr, lang.BuiltinTrees()["buildroot"])
	if rep.OK() || !strings.Contains(msgs(rep), "is string, but is set as a boolean") {
		t.Errorf("expected a type mismatch:\n%s", msgs(rep))
	}
}

// Unmanaged prefixes name trees Silt does not model; their symbols must not be
// reported as unknown.
func TestUnmanagedIsSkipped(t *testing.T) {
	tr := tree(t, "config BR2_ARCH\n\tbool \"arch\"\n")
	r := composed(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_TARGET_UBOOT_THING)))`,
	}, `(image i (compose target:t profile:p) (unmanaged buildroot:BR2_TARGET_UBOOT_*))`)

	if rep := Check(r, tr, lang.BuiltinTrees()["buildroot"]); !rep.OK() {
		t.Errorf("unmanaged symbols must be skipped:\n%s", msgs(rep))
	}
}

func TestTreeVersion(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Makefile", []byte("x := 1\nexport BR2_VERSION := 2025.02.16\ny := 2\n"), 0o644)
	v, err := kconfig.TreeVersion(dir)
	if err != nil || v != "2025.02.16" {
		t.Fatalf("got %q, %v", v, err)
	}
}

func TestConsumedByMustExistAndTakeAString(t *testing.T) {
	tr := tree(t, `
config BR2_PACKAGE_BUSYBOX_CONFIG_FRAGMENT_FILES
	string "fragments"
config BR2_PACKAGE_BUSYBOX
	bool "busybox"
`)
	trees := map[string]lang.TreeDecl{
		"busybox": {Name: "busybox", ConsumedBy: "BR2_PACKAGE_BUSYBOX_CONFIG_FRAGMENT_FILES"},
		"uboot":   {Name: "uboot", ConsumedBy: "BR2_TARGET_UBOOT_CONFIG_FRAGMENT_FILE"},
		"wrong":   {Name: "wrong", ConsumedBy: "BR2_PACKAGE_BUSYBOX"},
	}
	rep := &Report{}
	CheckTrees(trees, tr, rep)
	m := msgs(rep)
	if len(rep.Findings) != 2 || !strings.Contains(m, "does not exist") || !strings.Contains(m, "is bool") {
		t.Fatalf("findings:\n%s", m)
	}
}

// A tree's prefix is serialization, not identity. Buildroot declares
// `config BR2_X` and writes BR2_X; the kernel declares `config EXT4_FS` and
// writes CONFIG_EXT4_FS, because conf adds the prefix on the way out. Looking
// the written name up verbatim reported every CONFIG_ symbol in the library
// as missing, including ones that plainly exist.
func TestPrefixIsStrippedForTheTreeThatOmitsIt(t *testing.T) {
	tr := tree(t, "config EXT4_FS\n\tbool \"ext4\"\nconfig DEVTMPFS\n\tbool \"devtmpfs\"\n")
	r := composed(t, []string{tgt,
		`(fragment profile:p (requires (capability mmu))
		   (linux (y CONFIG_EXT4_FS) (y CONFIG_NOT_A_KERNEL_SYMBOL)))`,
	}, `(image i (compose target:t profile:p))`)

	decl := lang.TreeDecl{Name: "linux", Prefix: "CONFIG_"}
	rep := Check(r, tr, decl)
	if len(rep.Findings) != 1 || !strings.Contains(msgs(rep), "CONFIG_NOT_A_KERNEL_SYMBOL does not exist") {
		t.Fatalf("findings:\n%s", msgs(rep))
	}
	if rep.Checked != 2 {
		t.Errorf("checked %d, want both linux symbols", rep.Checked)
	}
}

// A rule's consequent is a claim about a tree even when the rule never fires,
// and an unfired rule is where a stale claim hides.
func TestRuleSymbolsAreCheckedWhetherOrNotTheyFire(t *testing.T) {
	tr := tree(t, "config FS_ENCRYPTION\n\tbool \"encryption\"\n")
	f, err := lang.ParseFile(`(rules cross
	  (when (y buildroot:BR2_PACKAGE_FSCRYPTCTL) (y linux:CONFIG_EXT4_ENCRYPTION))
	  (when (y buildroot:BR2_PACKAGE_OTHER) (y linux:CONFIG_FS_ENCRYPTION)))`, "r.sx")
	if err != nil {
		t.Fatal(err)
	}
	rep := &Report{}
	CheckRules(f.Rules, tr, lang.TreeDecl{Name: "linux", Prefix: "CONFIG_"}, rep)
	if len(rep.Findings) != 1 || !strings.Contains(msgs(rep), "CONFIG_EXT4_ENCRYPTION does not exist") {
		t.Fatalf("findings:\n%s", msgs(rep))
	}
}
