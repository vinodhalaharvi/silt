package kconfig_test

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/importer"
	"github.com/vinodhalaharvi/silt/kconfig"
)

// These compare the evaluator with Buildroot's own conf, full .config against
// full .config. They need a checkout, make and a C compiler.
func setup(t *testing.T) (string, *kconfig.Menu, *importer.Kbuild) {
	t.Helper()
	root := os.Getenv("SILT_BUILDROOT")
	if root == "" || os.Getenv("SILT_KBUILD") == "" {
		t.Skip("set SILT_BUILDROOT and SILT_KBUILD=1")
	}
	out := os.Getenv("SILT_KBUILD_OUT")
	if out == "" {
		out = t.TempDir()
	}
	env, err := kconfig.BuildrootEnv(root, out)
	if err != nil {
		t.Fatal(err)
	}
	m, err := kconfig.LoadMenu("Config.in", kconfig.Options{Root: root, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	return root, m, &importer.Kbuild{Root: root, Out: out}
}

func readConfig(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	as, err := kconfig.ReadAssignments(f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, a := range as {
		out[a.Name] = a.Value
	}
	return out
}

// diff compares two .configs. BR2_DEFCONFIG records the path it was loaded
// from and is the one line that is provenance rather than configuration.
func diff(want, got map[string]string) []string {
	var d []string
	seen := map[string]bool{}
	for _, m := range []map[string]string{want, got} {
		for k := range m {
			if seen[k] || k == "BR2_DEFCONFIG" {
				continue
			}
			seen[k] = true
			w, wok := want[k]
			g, gok := got[k]
			if wok != gok || w != g {
				d = append(d, fmt.Sprintf("%s: kbuild %q (%v), silt %q (%v)", k, w, wok, g, gok))
			}
		}
	}
	sort.Strings(d)
	return d
}

func compareOne(t *testing.T, m *kconfig.Menu, kb *importer.Kbuild, path string) []string {
	t.Helper()
	if err := kb.Defconfig(path); err != nil {
		t.Fatal(err)
	}
	want := readConfig(t, filepath.Join(kb.Out, ".config"))
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	as, err := kconfig.ReadAssignments(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	return diff(want, m.Evaluate(as).Config())
}

// TestEvaluatorMatchesKbuild runs every shipped defconfig (or SILT_DEFCONFIGS)
// through both and requires identical .configs.
func TestEvaluatorMatchesKbuild(t *testing.T) {
	root, m, kb := setup(t)
	paths, _ := filepath.Glob(filepath.Join(root, "configs", "*_defconfig"))
	if only := os.Getenv("SILT_DEFCONFIGS"); only != "" {
		paths = nil
		for _, n := range strings.Fields(only) {
			paths = append(paths, filepath.Join(root, "configs", n+"_defconfig"))
		}
	}
	bad := 0
	for _, p := range paths {
		if d := compareOne(t, m, kb, p); len(d) > 0 {
			bad++
			if len(d) > 10 {
				d = d[:10]
			}
			t.Errorf("%s:\n  %s", filepath.Base(p), strings.Join(d, "\n  "))
		}
	}
	t.Logf("%d defconfigs, %d identical to kbuild's .config", len(paths), len(paths)-bad)
}

// TestEvaluatorMatchesKbuildOnPerturbedConfigs adds random symbols to real
// defconfigs, some set and some not set, sometimes shuffling the lines. Real
// defconfigs are consistent by construction; these reach user values for
// invisible symbols, overridden choices and selects of hidden symbols, which
// no shipped defconfig does.
func TestEvaluatorMatchesKbuildOnPerturbedConfigs(t *testing.T) {
	root, m, kb := setup(t)
	n := 20
	if s := os.Getenv("SILT_FUZZ"); s != "" {
		fmt.Sscan(s, &n)
	}
	var bools []string
	re := regexp.MustCompile(`(?m)^(?:menu)?config (BR2_\w+)\n\s*bool`)
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasPrefix(info.Name(), "Config.in") ||
			strings.Contains(p, "/support/testing/") || strings.Contains(p, "/output/") {
			return nil
		}
		data, _ := os.ReadFile(p)
		for _, mm := range re.FindAllStringSubmatch(string(data), -1) {
			bools = append(bools, mm[1])
		}
		return nil
	})
	sort.Strings(bools)
	configs, _ := filepath.Glob(filepath.Join(root, "configs", "*_defconfig"))
	r := rand.New(rand.NewSource(7))
	dir := t.TempDir()
	bad := 0
	for i := 0; i < n; i++ {
		base, _ := os.ReadFile(configs[r.Intn(len(configs))])
		lines := strings.Split(strings.TrimSpace(string(base)), "\n")
		for j := 0; j < 40; j++ {
			s := bools[r.Intn(len(bools))]
			if r.Float64() < 0.7 {
				lines = append(lines, s+"=y")
			} else {
				lines = append(lines, "# "+s+" is not set")
			}
		}
		if i%3 == 0 {
			r.Shuffle(len(lines), func(a, b int) { lines[a], lines[b] = lines[b], lines[a] })
		}
		p := filepath.Join(dir, fmt.Sprintf("v%02d", i))
		os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		if d := compareOne(t, m, kb, p); len(d) > 0 {
			bad++
			t.Errorf("variant %d:\n  %s", i, strings.Join(d, "\n  "))
		}
	}
	t.Logf("%d perturbed configs, %d identical to kbuild's .config", n, n-bad)
}
