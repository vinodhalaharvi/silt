package emit

import (
	"os"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

func build(t *testing.T, srcs []string, image string) *compose.Result {
	t.Helper()
	l := compose.NewLibrary()
	for i, s := range srcs {
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

func TestDefconfigForms(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)) (buildroot
		    (n BR2_PACKAGE_SYSTEMD)
		    (value BR2_TARGET_ROOTFS_EXT2_SIZE "120M")
		    (prefer n BR2_ENABLE_DEBUG)))`,
	}, `(image i (compose target:t profile:p))`)

	out := Defconfig(r, lang.Buildroot)
	for _, want := range []string{
		"BR2_aarch64=y",
		"# BR2_PACKAGE_SYSTEMD is not set",
		`BR2_TARGET_ROOTFS_EXT2_SIZE="120M"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("defconfig missing %q:\n%s", want, out)
		}
	}
	// Soft constraints need MaxSAT; emitting them at Rung 2 would assert
	// something the model cannot yet justify.
	if strings.Contains(out, "BR2_ENABLE_DEBUG") {
		t.Errorf("soft constraint must not be emitted before Rung 7:\n%s", out)
	}
}

func TestScopesAreSeparate(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64)) (linux (y CONFIG_ARM64))
		   (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)))`,
	}, `(image i (compose target:t profile:p))`)

	br, lx := Defconfig(r, lang.Buildroot), Defconfig(r, lang.Linux)
	if strings.Contains(br, "CONFIG_ARM64") || strings.Contains(lx, "BR2_aarch64") {
		t.Errorf("trees leaked into each other:\n--- br ---\n%s\n--- lx ---\n%s", br, lx)
	}
}

func TestOpaqueIsEmittedVerbatim(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)))`,
	}, `(image i (compose target:t profile:p)
	      (opaque (value BR2_ROOTFS_POST_SCRIPT_ARGS "$(BR2_DEFCONFIG)")))`)

	out := Defconfig(r, lang.Buildroot)
	if !strings.Contains(out, `BR2_ROOTFS_POST_SCRIPT_ARGS="$(BR2_DEFCONFIG)"`) {
		t.Errorf("opaque value not carried through unevaluated:\n%s", out)
	}
}

// at-least m emits the weakest satisfying value. Choosing between m and y is
// an optimization, and optimization is Rung 7.
func TestAtLeastEmitsWeakest(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)) (linux (at-least m CONFIG_MAC80211)))`,
	}, `(image i (compose target:t profile:p))`)
	if !strings.Contains(Defconfig(r, lang.Linux), "CONFIG_MAC80211=m") {
		t.Errorf("want =m:\n%s", Defconfig(r, lang.Linux))
	}
}

// Regression: BR2_TARGET_ROOTFS_EXT2_4 is a choice member inside
// "if BR2_TARGET_ROOTFS_EXT2". The parent is a prerequisite, not something the
// member implies. Omitting it made kbuild drop both the member and the size.
func TestExt2ParentIsStated(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)) (buildroot
		   (y BR2_TARGET_ROOTFS_EXT2)
		   (y BR2_TARGET_ROOTFS_EXT2_4)))`,
	}, `(image i (compose target:t profile:p))`)
	out := Defconfig(r, lang.Buildroot)
	if !strings.Contains(out, "BR2_TARGET_ROOTFS_EXT2=y") {
		t.Errorf("parent guard symbol must be emitted:\n%s", out)
	}
}

// A kernel is only built when BR2_LINUX_KERNEL is set. Stating CONFIG_*
// symbols does not imply it: they describe a kernel's content, while this
// switches the kernel package on. Omitting it produced a rootfs and no Image,
// and nothing warned — qemu just refused to load a kernel never built.
func TestKernelIsActuallyRequested(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64) (y BR2_LINUX_KERNEL)
		    (y BR2_LINUX_KERNEL_IMAGE)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)))`,
	}, `(image i (compose target:t profile:p) (linux (custom-version "6.18.7")))`)

	out := DefconfigWithKernel(r, lang.Buildroot, "/abs/out/linux.config")
	for _, want := range []string{
		"BR2_LINUX_KERNEL=y",
		"BR2_LINUX_KERNEL_IMAGE=y",
		`BR2_LINUX_KERNEL_CUSTOM_VERSION_VALUE="6.18.7"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// The emitted linux.config must be handed to Buildroot, or the Linux half of
// every fragment is produced and then ignored — Silt committing the very
// two-halves-disagree failure it exists to remove.
func TestLinuxHalfIsWiredIn(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64)) (linux (y CONFIG_VIRTIO_BLK))
		   (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)))`,
	}, `(image i (compose target:t profile:p))`)

	if !strings.Contains(DefconfigWithKernel(r, lang.Buildroot, "/x/linux.config"),
		"BR2_LINUX_KERNEL_CONFIG_FRAGMENT_FILES") {
		t.Error("linux.config is emitted but never referenced from the defconfig")
	}
	if !strings.Contains(Defconfig(r, lang.Linux), "CONFIG_VIRTIO_BLK=y") {
		t.Error("linux scope did not reach linux.config")
	}
}

// With no Linux half there is nothing to layer, and the line must not appear:
// an imported defconfig would otherwise never round-trip through emit.
func TestNoLinuxHalfNoFragmentLine(t *testing.T) {
	r := build(t, []string{
		`(fragment target:t (buildroot (y BR2_aarch64) (y BR2_LINUX_KERNEL)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)))`,
	}, `(image i (compose target:t profile:p))`)
	if strings.Contains(DefconfigWithKernel(r, lang.Buildroot, "/x/linux.config"),
		"BR2_LINUX_KERNEL_CONFIG_FRAGMENT_FILES") {
		t.Error("an empty linux.config was wired in")
	}
}

// A fragment's (value X "2048") is a string, but kbuild discards
// X="2048" when X is an int. With a tree, int and hex go out bare.
func TestIntAndHexAreWrittenBare(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(`
config BR2_UBI_SUBSIZE
	int "subsize"
config BR2_LEBSIZE
	hex "leb"
config BR2_NAME
	string "name"
`), 0o644)
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	l := compose.NewLibrary()
	l.Tree = tree
	for _, s := range []string{
		`(fragment target:t (buildroot (value BR2_UBI_SUBSIZE "2048") (value BR2_LEBSIZE "0x1f000") (value BR2_NAME "2048")))`,
		`(fragment profile:p)`,
		`(image i (compose target:t profile:p))`,
	} {
		f, err := lang.ParseFile(s, "x.sx")
		if err != nil {
			t.Fatal(err)
		}
		l.Add(f)
	}
	f, _ := lang.ParseFile(`(image i (compose target:t profile:p))`, "i.sx")
	r, err := l.Compose(f.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	out := Defconfig(r, lang.Buildroot)
	for _, want := range []string{"BR2_UBI_SUBSIZE=2048\n", "BR2_LEBSIZE=0x1f000\n", `BR2_NAME="2048"` + "\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}

	// Without a tree the type is unknown; the output must say so.
	l.Tree = nil
	r, _ = l.Compose(f.Images[0])
	if !strings.Contains(Defconfig(r, lang.Buildroot), "WARNING") {
		t.Error("numeric values emitted untyped without a warning")
	}
}
