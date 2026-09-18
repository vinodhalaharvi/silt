package verify_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/verify"
)

// The library's CONFIG_ claims against a real kernel. Set SILT_LINUX to a
// Linux checkout (the Kconfig files are enough). Three defects were found the
// first time this ran: CONFIG_EXT4_FS_MODULE, which no kernel declares;
// CONFIG_SYSFS_DEPRECATED, removed before 6.12; and CONFIG_EXT4_ENCRYPTION,
// renamed to CONFIG_FS_ENCRYPTION, copied from Buildroot's own stale help.
func TestLibraryAgainstLinux(t *testing.T) {
	root := os.Getenv("SILT_LINUX")
	if root == "" {
		t.Skip("set SILT_LINUX to a Linux checkout")
	}
	arch := os.Getenv("SILT_LINUX_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	env, err := kconfig.LinuxEnv(root, arch)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kconfig.Load("Kconfig", kconfig.Options{Root: root, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("linux %s %s: %d symbols, %d choices", env["KERNELVERSION"], arch,
		len(tree.Symbols), len(tree.Choices))
	if len(tree.Symbols) < 5000 {
		t.Fatalf("only %d symbols: the import stopped early", len(tree.Symbols))
	}
	for _, name := range []string{"ARM64", "EXT4_FS", "DEVTMPFS_MOUNT", "VIRTIO_BLK"} {
		if tree.Symbols[name] == nil {
			t.Errorf("%s missing from the import", name)
		}
	}

	lib := compose.NewLibrary()
	var images []*lang.Image
	a, _ := filepath.Glob("../fragments/*.sx")
	b, _ := filepath.Glob("../fragments/*/*.sx")
	// Packs are part of the library too; qemu-arm-boot composes one.
	pk, _ := filepath.Glob("../packs/*/fragments/*/*.sx")
	b = append(b, pk...)
	c, _ := filepath.Glob("../images/*.sx")
	for _, p := range append(append(a, b...), c...) {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := lang.ParseFile(string(data), p)
		if err != nil {
			t.Fatal(err)
		}
		if err := lib.Add(f); err != nil {
			t.Fatal(err)
		}
		images = append(images, f.Images...)
	}
	decl := lib.Trees["linux"]
	checked := 0
	for _, im := range images {
		res, err := lib.Compose(im)
		if err != nil {
			t.Fatal(err)
		}
		rep := verify.Check(res, tree, decl)
		verify.CheckRules(lib.Rules, tree, decl, rep)
		checked += rep.Checked
		for _, f := range rep.Findings {
			t.Errorf("%s: %s", im.Name, f)
		}
	}
	t.Logf("%d images, %d CONFIG_ claims checked", len(images), checked)
}
