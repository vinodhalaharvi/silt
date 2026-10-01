package plugins_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugins"
	"github.com/vinodhalaharvi/silt/verify"
)

// The migration's one real risk: two code paths that decide what counts as a
// problem, giving different answers depending on which command the reader ran.
//
// This runs both over this repository's own library and every image in it, and
// requires the same findings from each. It is the test that lets the plan path
// become the default: not an argument that the refactor is correct, but a
// comparison against the thing it replaces, on real input.
//
// It skips without a Buildroot tree, which is how every other test here that
// needs one behaves.
func TestPlanAgreesWithVerify(t *testing.T) {
	br := os.Getenv("BUILDROOT")
	if br == "" {
		for _, c := range []string{"/home/claude/br", filepath.Join(os.Getenv("HOME"), "buildroot")} {
			if _, err := os.Stat(filepath.Join(c, "Config.in")); err == nil {
				br = c
				break
			}
		}
	}
	if br == "" {
		t.Skip("no Buildroot tree; set BUILDROOT")
	}

	root := repoRoot(t)
	// The same environment the tool builds: Buildroot's Config.in sources
	// paths that only exist once BR2_BASE_DIR and the external list are set.
	env, err := kconfig.BuildrootEnv(br, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: br, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	ver, _ := kconfig.TreeVersion(br)

	lib := loadLibrary(t, root)
	images := allImages(t, root)
	if len(images) == 0 {
		t.Fatal("no images found; the comparison would prove nothing")
	}

	checked := 0
	for _, im := range images {
		res, err := lib.Compose(im)
		if err != nil {
			continue // a composition that does not compose is not this test's subject
		}

		// The old path: verify walks the whole result, per tree.
		var want []string
		for name, d := range lib.Trees {
			if d.Kind != lang.KindKconfig || name != "buildroot" {
				continue
			}
			for _, f := range verifyCheck(res, tree, d).Findings {
				want = append(want, normalise(f.Symbol, f.Message))
			}
		}

		// The new path: the same claims as a plan, answered by the plugin,
		// folded into a validation.
		runner := plugins.NewRunner(res, lib.Trees, plugins.Sources{
			Buildroot: tree, BuildrootVersion: ver,
		})
		fs, counts, err := plugins.Report(res, lib.Trees, runner)
		if err != nil {
			t.Fatalf("%s: %v", im.Name, err)
		}
		var got []string
		for _, f := range fs {
			got = append(got, normalise(f.Symbol, f.Message))
		}

		// The count the report prints has to match too: "43 buildroot
		// symbols checked" is a claim a reader relies on, and a migration
		// that quietly changed it would pass a findings-only comparison.
		if want, have := wantChecked(res, tree, lib.Trees), counts["buildroot"]; want != have {
			t.Errorf("%s: verify checked %d buildroot symbols, the plan asked about %d",
				im.Name, want, have)
		}

		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, "\n") != strings.Join(got, "\n") {
			t.Errorf("%s:\n  verify: %v\n  plan:   %v", im.Name, want, got)
		}
		checked++
	}

	if checked < 10 {
		t.Fatalf("only %d images compared; the comparison is too thin to trust", checked)
	}
	t.Logf("%d images, both paths agree", checked)
}

// A plan knows what it will ask before anything is opened, which is what lets
// one tree answer for every image rather than one per command.
func TestPlanIsKnowableBeforeOpening(t *testing.T) {
	root := repoRoot(t)
	lib := loadLibrary(t, root)
	images := allImages(t, root)
	if len(images) == 0 {
		t.Skip("no images")
	}

	whole := plan.Pure[plugins.KconfigClaim](plan.Good(plan.Unit{}))
	n := 0
	for _, im := range images {
		res, err := lib.Compose(im)
		if err != nil {
			continue
		}
		whole = plan.Map2(whole, plugins.KconfigClaims(res, "buildroot", lib.Trees),
			func(a, b plan.V[plan.Unit]) plan.V[plan.Unit] {
				return plan.V[plan.Unit]{Problems: append(a.Problems, b.Problems...)}
			})
		n++
	}
	if n == 0 {
		t.Skip("nothing composed")
	}

	// No tree was loaded to get here.
	trees := plan.Trees(whole)
	if len(trees) == 0 {
		t.Fatal("a plan over every image names no trees")
	}
	if len(whole.Asks()) < 100 {
		t.Errorf("%d asks over %d images looks too few", len(whole.Asks()), n)
	}
	t.Logf("%d images, %d asks, trees %v, nothing opened", n, len(whole.Asks()), trees)
}

func normalise(symbol, msg string) string { return symbol + ": " + strings.TrimSpace(msg) }

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir) // plugins/ -> repo root
}

func loadLibrary(t *testing.T, root string) *compose.Library {
	t.Helper()
	lib := compose.NewLibrary()
	for _, p := range parseAll(t, root) {
		if err := lib.Add(p); err != nil {
			t.Fatalf("%v", err)
		}
	}
	return lib
}

func allImages(t *testing.T, root string) []*lang.Image {
	t.Helper()
	var out []*lang.Image
	for _, f := range parseAll(t, root) {
		out = append(out, f.Images...)
	}
	return out
}

// verifyCheck is the old path, named here so the comparison reads as what it
// is: the function this migration replaces.
func verifyCheck(res *compose.Result, tree *kconfig.Tree, d lang.TreeDecl) *verify.Report {
	return verify.Check(res, tree, d)
}

// parseAll reads the repository's own library, through the same one list of
// paths the tool uses - three tests each having their own globs is how a
// missing piece once appeared as three unrelated-looking errors.
func parseAll(t *testing.T, root string) []*lang.File {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)

	var out []*lang.File
	for _, p := range compose.LibraryPaths(".") {
		src, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := lang.ParseFile(string(src), p)
		if err != nil {
			continue
		}
		out = append(out, f)
	}
	return out
}

func wantChecked(res *compose.Result, tree *kconfig.Tree, trees map[string]lang.TreeDecl) int {
	return verify.Check(res, tree, trees["buildroot"]).Checked
}
