package fixpoint

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/importer"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

// The library's images, emitted and loaded by Buildroot's own kconfig. Images
// with negative intent — profile:minimal says (n BR2_PACKAGE_SYSTEMD) — must
// pass, because kbuild deletes the unavailable symbol rather than writing it
// as not set. edge-camera must fail at exactly the symbol kbuild dropped.
func TestLibraryImagesAgainstKbuild(t *testing.T) {
	root := os.Getenv("SILT_BUILDROOT")
	if root == "" || os.Getenv("SILT_KBUILD") == "" {
		t.Skip("set SILT_BUILDROOT and SILT_KBUILD=1")
	}
	ver, _ := kconfig.TreeVersion(root)
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: root, Env: map[string]string{
		"BR2_BASE_DIR": filepath.Join(root, "output"), "HOSTARCH": "x86_64",
		"HOST_GCC_VERSION": "13 2", "BR2_VERSION_FULL": ver,
	}})
	if err != nil {
		t.Fatal(err)
	}
	lib := compose.NewLibrary()
	lib.Tree = tree
	files, _ := filepath.Glob("../fragments/*.sx")
	more, _ := filepath.Glob("../fragments/*/*.sx")
	// Packs are part of the library too; qemu-arm-boot composes one.
	pk, _ := filepath.Glob("../packs/*/fragments/*/*.sx")
	more = append(more, pk...)
	for _, p := range append(files, more...) {
		data, _ := os.ReadFile(p)
		f, err := lang.ParseFile(string(data), p)
		if err != nil {
			t.Fatal(err)
		}
		if err := lib.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	out := os.Getenv("SILT_KBUILD_OUT")
	if out == "" {
		out = t.TempDir()
	}
	kb := &importer.Kbuild{Root: root, Out: out}

	images, _ := filepath.Glob("../images/*.sx")
	for _, path := range images {
		name := filepath.Base(path[:len(path)-len(".sx")])
		data, err := os.ReadFile("../images/" + name + ".sx")
		if err != nil {
			t.Fatal(err)
		}
		f, err := lang.ParseFile(string(data), name)
		if err != nil {
			t.Fatal(err)
		}
		res, err := lib.Compose(f.Images[0])
		if err != nil {
			t.Fatal(err)
		}
		emitted, err := importer.Emit(res, out)
		if err != nil {
			t.Fatal(err)
		}
		if err := kb.Defconfig(emitted); err != nil {
			t.Fatal(err)
		}
		fh, err := os.Open(filepath.Join(out, ".config"))
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := Read(fh)
		fh.Close()
		if err != nil {
			t.Fatal(err)
		}
		ms := Check(res, lang.Buildroot, cfg)
		absentN := 0
		for _, c := range res.Constraints[lang.Buildroot] {
			if _, ok := cfg[c.Sym.Name]; !ok && !c.IsValue && c.Want == lang.N && !c.Soft {
				absentN++
			}
		}
		t.Logf("%-18s %d mismatches; %d (n X) constraints satisfied by absence", name, len(ms), absentN)
		for _, m := range ms {
			t.Logf("    %v", m)
		}
		switch name {
		case "edge-camera":
			if len(ms) == 0 {
				t.Errorf("edge-camera asks for libcamera with static libs; kbuild must drop something")
			}
		default:
			if len(ms) != 0 {
				t.Errorf("%s: %d mismatches", name, len(ms))
			}
		}
		if name == "qemu-arm-dev" && absentN == 0 {
			t.Errorf("qemu-arm-dev should exercise absent-as-n (profile:minimal states n BR2_PACKAGE_SYSTEMD)")
		}
	}
}
