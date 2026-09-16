package solver

import (
	"os"
	"testing"
	"time"

	"github.com/vinodhalaharvi/silt/cnf"
	"github.com/vinodhalaharvi/silt/kconfig"
)

func realModel(t *testing.T) *cnf.Model {
	t.Helper()
	root := os.Getenv("SILT_BUILDROOT")
	if root == "" {
		t.Skip("set SILT_BUILDROOT to a Buildroot checkout to run")
	}
	tr, err := kconfig.Load("Config.in", kconfig.Options{Root: root, Env: map[string]string{
		"BR2_BASE_DIR": "/nonexistent", "HOSTARCH": "x86_64",
		"HOST_GCC_VERSION": "13 2", "BR2_VERSION_FULL": "0",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return cnf.Lower(tr)
}

// These four answers were established independently by running kbuild itself
// (DESIGN.md §14.5), so they are a real oracle rather than a restatement of
// what the solver happens to do.
// Answers established independently by running kbuild itself (DESIGN.md
// §14.5), so this is a real oracle rather than a restatement of the solver.
func TestRealTreeQueries(t *testing.T) {
	m := realModel(t)
	t.Logf("%d vars, %d clauses", m.F.NumVars(), len(m.F.Clauses))

	cases := []struct {
		name   string
		assume []string
		want   Status
		then   []string // symbols that must come out true
	}{
		{"empty", nil, SAT, nil},
		{"libcamera", []string{"BR2_PACKAGE_LIBCAMERA"}, SAT,
			[]string{"BR2_PACKAGE_GNUTLS", "BR2_PACKAGE_LIBYAML", "BR2_INSTALL_LIBSTDCPP"}},
		{"static-libs alone", []string{"BR2_STATIC_LIBS"}, SAT, nil},
		{"libcamera + static-libs", []string{"BR2_PACKAGE_LIBCAMERA", "BR2_STATIC_LIBS"}, UNSAT, nil},
	}

	for _, c := range cases {
		var as []cnf.Lit
		for _, a := range c.assume {
			as = append(as, m.F.Var(a))
		}
		start := time.Now()
		r := New(m.F).Solve(as...)
		if r.Err != nil {
			t.Fatalf("%s: %v", c.name, r.Err)
		}
		if r.Status != c.want {
			t.Errorf("%s: got %v want %v (%d conflicts)", c.name, r.Status, c.want, r.Conflicts)
			continue
		}
		t.Logf("%-26s %-5s %6d conflicts %8s", c.name, r.Status, r.Conflicts,
			time.Since(start).Round(time.Millisecond))
		for _, sym := range c.then {
			if !r.Model[m.F.Var(sym).Var()] {
				t.Errorf("%s: %s should have been implied", c.name, sym)
			}
		}
	}
}
