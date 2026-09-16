package importer

import (
	"os"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

func tinyTree(t *testing.T) *kconfig.Tree {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(dir+"/arch", 0o755)
	os.MkdirAll(dir+"/boot", 0o755)
	os.WriteFile(dir+"/Config.in", []byte(`
source "arch/Config.in"
source "boot/Config.in"
config BR2_TARGET_ROOTFS_EXT2
	bool "ext2"
config BR2_ROOTFS_POST_SCRIPT_ARGS
	string "args"
config BR2_TARGET_ROOTFS_EXT2_SIZE
	string "size"
config BR2_PACKAGE_SYSTEMD
	bool "systemd"
config BR2_JLEVEL
	int "jobs"
`), 0o644)
	os.WriteFile(dir+"/arch/Config.in", []byte("config BR2_aarch64\n\tbool \"aarch64\"\n"), 0o644)
	os.WriteFile(dir+"/boot/Config.in", []byte("config BR2_TARGET_UBOOT\n\tbool \"u-boot\"\n"), 0o644)
	tr, err := kconfig.Load("Config.in", kconfig.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestParseDefconfig(t *testing.T) {
	es, err := ParseDefconfig(strings.NewReader(`BR2_aarch64=y
# a comment
# BR2_PACKAGE_SYSTEMD is not set

BR2_ROOTFS_POST_SCRIPT_ARGS="-c \"quoted\" $(BINARIES_DIR)"
BR2_JLEVEL=4
`), "d")
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 4 {
		t.Fatalf("want 4 entries, got %d: %+v", len(es), es)
	}
	if !es[1].Unset || es[1].Symbol != "BR2_PACKAGE_SYSTEMD" || es[1].Line != 3 {
		t.Errorf("not-set line misread: %+v", es[1])
	}
	if got := es[2].String(); got != `-c "quoted" $(BINARIES_DIR)` {
		t.Errorf("string unescaped wrong: %q", got)
	}
}

// Silently skipping a line is how a config loses a setting without anyone
// noticing, so anything unrecognised is an error.
func TestParseDefconfigRejects(t *testing.T) {
	for _, src := range []string{
		"BR2_A=y\nBR2_A=n\n",
		"CONFIG_FOO=y\n",
		"BR2_A y\n",
	} {
		if _, err := ParseDefconfig(strings.NewReader(src), "d"); err == nil {
			t.Errorf("accepted %q", src)
		}
	}
}

func TestImportChecksAgainstTree(t *testing.T) {
	tr := tinyTree(t)
	for _, c := range []struct{ src, want string }{
		{"BR2_NOT_A_SYMBOL=y\n", "not a symbol"},
		{"BR2_TARGET_ROOTFS_EXT2=\"y\"\n", "assigned"},
		{"BR2_TARGET_ROOTFS_EXT2_SIZE=120M\n", "unquoted"},
		{"BR2_TARGET_ROOTFS_EXT2=m\n", "assigned m"},
	} {
		es, err := ParseDefconfig(strings.NewReader(c.src), "d")
		if err != nil {
			t.Fatal(err)
		}
		_, err = Import("x", "d", "test", es, tr)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: want error containing %q, got %v", c.src, c.want, err)
		}
	}
}

// Classification must be a function of the symbol, never its value: two
// boards that split the same symbol differently cannot be recombined.
func TestClassifyIgnoresValue(t *testing.T) {
	tr := tinyTree(t)
	sym := tr.Symbols["BR2_ROOTFS_POST_SCRIPT_ARGS"]
	a, _ := Classify("BR2_ROOTFS_POST_SCRIPT_ARGS", sym)
	for _, name := range []string{"BR2_ROOTFS_POST_SCRIPT_ARGS"} {
		b, _ := Classify(name, sym)
		if a != b {
			t.Fatal("same symbol, different class")
		}
	}
	if c, _ := Classify("BR2_aarch64", tr.Symbols["BR2_aarch64"]); c != Target {
		t.Error("arch/ symbol should be target")
	}
	if c, _ := Classify("BR2_TARGET_UBOOT", tr.Symbols["BR2_TARGET_UBOOT"]); c != Target {
		t.Error("boot/ symbol should be target")
	}
	if c, _ := Classify("BR2_PACKAGE_SYSTEMD", tr.Symbols["BR2_PACKAGE_SYSTEMD"]); c != Profile {
		t.Error("systemd should be profile")
	}
}

// The rendered text is what gets written, so it must parse, compose, and state
// exactly what the defconfig did, escapes included.
func TestRenderedFragmentsComposeToTheSource(t *testing.T) {
	tr := tinyTree(t)
	src := `BR2_aarch64=y
BR2_TARGET_UBOOT=y
BR2_TARGET_ROOTFS_EXT2=y
# BR2_PACKAGE_SYSTEMD is not set
BR2_ROOTFS_POST_SCRIPT_ARGS="-c \"x\" \\n"
BR2_JLEVEL=4
`
	es, err := ParseDefconfig(strings.NewReader(src), "configs/tiny_board_defconfig")
	if err != nil {
		t.Fatal(err)
	}
	im, err := Import(NameFor("configs/tiny_board_defconfig"), "configs/tiny_board_defconfig", "test", es, tr)
	if err != nil {
		t.Fatal(err)
	}
	if im.Name != "tiny-board" {
		t.Errorf("name %q", im.Name)
	}
	im.Provides = []string{"mmu"}
	res, err := Compose(tr, im.Image(), im.Fragment(Target), im.Fragment(Profile))
	if err != nil {
		t.Fatalf("%v\n%s\n%s", err, im.Fragment(Target), im.Fragment(Profile))
	}
	got := map[string]lang.Constraint{}
	for _, c := range res.Constraints[lang.Buildroot] {
		got[c.Symbol] = c
	}
	if len(got) != len(es) {
		t.Fatalf("composed %d symbols from %d lines", len(got), len(es))
	}
	if c := got["BR2_ROOTFS_POST_SCRIPT_ARGS"]; c.Value != `-c "x" \n` {
		t.Errorf("string value changed on the way through: %q", c.Value)
	}
	if c := got["BR2_PACKAGE_SYSTEMD"]; c.Want != lang.N {
		t.Errorf("not-set became %v", c.Want)
	}
	if c := got["BR2_JLEVEL"]; !c.IsValue || c.Value != "4" {
		t.Errorf("int value: %+v", c)
	}
}

func TestNameFor(t *testing.T) {
	for in, want := range map[string]string{
		"configs/raspberrypi4_64_defconfig": "raspberrypi4-64",
		"odroidc2_defconfig":                "odroidc2",
		"/x/1234_defconfig":                 "imported-1234",
	} {
		if got := NameFor(in); got != want {
			t.Errorf("NameFor(%q) = %q, want %q", in, got, want)
		}
	}
}
