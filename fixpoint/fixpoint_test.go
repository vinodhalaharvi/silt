package fixpoint

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/lang"
)

func con(t *testing.T, src string) lang.Constraint {
	t.Helper()
	f, err := lang.ParseFile(`(fragment target:x (buildroot `+src+`))`, "t.sx")
	if err != nil {
		t.Fatal(err)
	}
	return f.Fragments[0].Constraints[lang.Buildroot][0]
}

const cfgText = `# Automatically generated file; DO NOT EDIT.
BR2_aarch64=y
BR2_PACKAGE_KMOD=m
# BR2_PACKAGE_XZ is not set
BR2_TARGET_ROOTFS_EXT2_SIZE="120M"
BR2_ROOTFS_POST_SCRIPT_ARGS="-c \"x\" $(BINARIES_DIR)"
BR2_TARGET_ROOTFS_UBI_SUBSIZE=2048
`

func TestHolds(t *testing.T) {
	cfg, err := Read(strings.NewReader(cfgText))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		src  string
		want bool
	}{
		{`(y BR2_aarch64)`, true},
		{`(n BR2_aarch64)`, false},
		{`(m BR2_PACKAGE_KMOD)`, true},
		{`(y BR2_PACKAGE_KMOD)`, false},
		{`(at-least m BR2_PACKAGE_KMOD)`, true},
		{`(at-least m BR2_aarch64)`, true},
		{`(n BR2_PACKAGE_XZ)`, true},
		// The point of the package: absent is n.
		{`(n BR2_PACKAGE_SYSTEMD)`, true},
		{`(at-least n BR2_PACKAGE_SYSTEMD)`, true},
		// And only in that direction.
		{`(y BR2_PACKAGE_SYSTEMD)`, false},
		{`(at-least m BR2_PACKAGE_SYSTEMD)`, false},
		{`(value BR2_BOARD_NAME "")`, false},
		{`(value BR2_TARGET_ROOTFS_EXT2_SIZE "120M")`, true},
		{`(value BR2_TARGET_ROOTFS_EXT2_SIZE "128M")`, false},
		{`(value BR2_ROOTFS_POST_SCRIPT_ARGS "-c \"x\" $(BINARIES_DIR)")`, true},
		{`(value BR2_TARGET_ROOTFS_UBI_SUBSIZE "2048")`, true},
		{`(y BR2_TARGET_ROOTFS_EXT2_SIZE)`, false},
	} {
		if got := Holds(con(t, c.src), cfg); got != c.want {
			t.Errorf("%s: got %v, want %v", c.src, got, c.want)
		}
	}
}

func TestCheckRespectsTreesAndUnmanaged(t *testing.T) {
	l := compose.NewLibrary()
	for _, src := range []string{
		`(fragment target:t (provides (capability mmu))
		   (buildroot (y BR2_aarch64) (y BR2_TARGET_UBOOT) (y BR2_PACKAGE_LIBCAMERA))
		   (linux (y CONFIG_BR2_aarch64)))`,
		`(fragment profile:p (requires (capability mmu)) (buildroot (n BR2_PACKAGE_SYSTEMD)))`,
	} {
		f, err := lang.ParseFile(src, "f.sx")
		if err != nil {
			t.Fatal(err)
		}
		l.Add(f)
	}
	f, _ := lang.ParseFile(`(image i (compose target:t profile:p) (unmanaged buildroot:BR2_TARGET_UBOOT*))`, "i.sx")
	res, err := l.Compose(f.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := Read(strings.NewReader("BR2_aarch64=y\n"))
	ms := Check(res, lang.Buildroot, cfg)
	if len(ms) != 1 || ms[0].Want.Sym.Name != "BR2_PACKAGE_LIBCAMERA" {
		t.Fatalf("want exactly the dropped libcamera, got %v", ms)
	}
	if !strings.Contains(ms[0].Error(), "kbuild wrote absent") {
		t.Errorf("message: %s", ms[0].Error())
	}
}
