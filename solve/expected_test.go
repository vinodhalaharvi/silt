package solve_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/solve"
)

var update = flag.Bool("update", false, "rewrite the pinned explanations")

// The explanation edge-camera produces, pinned. Explanation quality is the
// subjective part of this project and therefore the part most likely to rot
// quietly: nothing else fails when the wording of a conflict gets worse, or
// when a repair that used to be named stops being named. Regenerate with
//
//	SILT_BUILDROOT=... go test ./solve -run Expected -update
//
// and read the diff before committing it.
func TestEdgeCameraExplanation(t *testing.T) {
	root := os.Getenv("SILT_BUILDROOT")
	if root == "" {
		t.Skip("set SILT_BUILDROOT to a Buildroot checkout")
	}
	env, err := kconfig.BuildrootEnv(root, filepath.Join(root, "output"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: root, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	lib := compose.NewLibrary()
	lib.Tree = tree
	a, _ := filepath.Glob("../fragments/*.sx")
	b, _ := filepath.Glob("../fragments/*/*.sx")
	// Packs are part of the library too; qemu-arm-boot composes one.
	pk, _ := filepath.Glob("../packs/*/fragments/*/*.sx")
	b = append(b, pk...)
	for _, p := range append(a, b...) {
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
	}
	data, err := os.ReadFile("../images/edge-camera.sx")
	if err != nil {
		t.Fatal(err)
	}
	f, err := lang.ParseFile(string(data), "edge-camera")
	if err != nil {
		t.Fatal(err)
	}
	res, err := lib.Compose(f.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	out := solve.Solve(res, tree)
	got := out.Explain("edge-camera") +
		solve.ExplainRepairs(solve.Repairs(out, out.Formula(), out.Owners(), res.Image.Policy))

	golden := "testdata/edge-camera.expected"
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("rewrote " + golden)
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("explanation changed.\n--- pinned\n%s\n--- now\n%s", want, got)
	}
}
