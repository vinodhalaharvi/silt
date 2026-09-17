package cnf_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/cnf"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/solver"
)

// TestKbuildConfigsAreModels checks the model is sound against kbuild: every
// full .config kbuild produces for a shipped defconfig, bools and values
// alike, must satisfy the formula. A rejected one is a configuration kbuild
// builds and Silt calls impossible.
//
// The .configs come from kconfig's evaluator, which matches Buildroot's conf
// line for line on all 290 (kconfig/kbuild_test.go), so no make is needed.
func TestKbuildConfigsAreModels(t *testing.T) {
	root := os.Getenv("SILT_BUILDROOT")
	if root == "" {
		t.Skip("set SILT_BUILDROOT")
	}
	env, err := kconfig.BuildrootEnv(root, filepath.Join(root, "output"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: root, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	menu, err := kconfig.LoadMenu("Config.in", kconfig.Options{Root: root, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	m := cnf.Lower(tree)
	s := solver.New(m.F)

	paths, _ := filepath.Glob(filepath.Join(root, "configs", "*_defconfig"))
	bad, values := 0, 0
	for _, p := range paths {
		f, _ := os.Open(p)
		as, _ := kconfig.ReadAssignments(f)
		f.Close()
		cfg := menu.Evaluate(as).Config()
		names := kconfig.Names(cfg)
		var lits []cnf.Lit
		for _, name := range names {
			v := cfg[name]
			sym := tree.Symbols[name]
			if sym == nil {
				continue
			}
			if sym.Type.Solvable() {
				l := m.F.Var(name)
				if v == "n" {
					l = l.Neg()
				}
				lits = append(lits, l)
				continue
			}
			if l, ok := m.ValueLit(name, strings.Trim(v, `"`)); ok {
				lits = append(lits, l)
				values++
			}
		}
		r := s.Solve(lits...)
		if r.Status != solver.SAT {
			bad++
			var core []string
			for _, c := range r.Core {
				core = append(core, m.F.Name(c.Var()))
			}
			sort.Strings(core)
			t.Errorf("%s: kbuild's .config is not a model; core %v", filepath.Base(p), core)
		}
	}
	t.Logf("%d .configs, %d value assignments among them, %d rejected; %d hidden strings derived",
		len(paths), values, bad, m.Derived)
}

// Host dependence, made visible: the same stated symbol is possible on one
// host architecture and impossible on another, because Buildroot's
// depends on BR2_HOSTARCH = "x86_64" is now in the model.
func TestHostArchIsInTheModel(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Config.in"), []byte(`
config BR2_HOSTARCH
	string
	option env="HOSTARCH"

config BR2_ENDIAN
	string
	default "LITTLE" if BR2_LE
	default "BIG"

config BR2_LE
	bool "little endian"

config BR2_TOOLCHAIN_EXTERNAL_BOOTLIN
	bool "bootlin"
	depends on BR2_HOSTARCH = "x86_64"

config BR2_NEEDS_BIG
	bool "needs big endian"
	depends on BR2_ENDIAN = "BIG"
`), 0o644)
	for arch, want := range map[string]solver.Status{"x86_64": solver.SAT, "aarch64": solver.UNSAT} {
		tr, err := kconfig.Load("Config.in", kconfig.Options{Root: dir, Env: map[string]string{"HOSTARCH": arch}})
		if err != nil {
			t.Fatal(err)
		}
		m := cnf.Lower(tr)
		if got := solver.New(m.F).Solve(m.F.Var("BR2_TOOLCHAIN_EXTERNAL_BOOTLIN")).Status; got != want {
			t.Errorf("HOSTARCH=%s: bootlin toolchain %v, want %v", arch, got, want)
		}
		// A hidden string is a function of its defaults: big endian is
		// exactly when little endian is off.
		s := solver.New(m.F)
		big, le := m.F.Var("BR2_NEEDS_BIG"), m.F.Var("BR2_LE")
		if got := s.Solve(big, le).Status; got != solver.UNSAT {
			t.Errorf("BR2_NEEDS_BIG with BR2_LE: %v, want UNSAT", got)
		}
		if got := s.Solve(big, le.Neg()).Status; got != solver.SAT {
			t.Errorf("BR2_NEEDS_BIG without BR2_LE: %v, want SAT", got)
		}
		// A stated value outside every compared literal is "other", and
		// still distinct from a compared one.
		if l, ok := m.ValueLit("BR2_ENDIAN", "PDP"); !ok || s.Solve(l, big).Status != solver.UNSAT {
			t.Error("an unmentioned value should not satisfy = \"BIG\"")
		}
	}
}
