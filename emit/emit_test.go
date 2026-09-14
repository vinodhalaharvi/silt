package emit

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
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
