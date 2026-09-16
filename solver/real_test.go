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

// TestRealTreeForwardPassShape runs the query pattern rung 7's forward pass
// needs: walk every symbol in declaration order, ask whether it can be on
// given everything decided so far, and fix the answer permanently. Wanting
// every symbol on is deliberately harsher than real defaults, since it drives
// as many queries as possible to UNSAT.
//
// It checks two things. Every UNSAT answer must name the symbol under test in
// its core, since everything else is already a fixed fact. And the fixed
// assignment must be a model, which a final solve verifies clause by clause.
func TestRealTreeForwardPassShape(t *testing.T) {
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
	m := cnf.Lower(tr)

	var order []cnf.Lit
	for _, name := range tr.Order {
		if sym, ok := tr.Symbols[name]; ok && sym.Type.Solvable() {
			order = append(order, m.F.Var(name))
		}
	}

	// Fresh construction per query, on a sample: this is what the forward pass
	// would cost without incremental solving.
	const sample = 100
	start := time.Now()
	for _, l := range order[:sample] {
		New(m.F).Solve(l)
	}
	fresh := time.Since(start) / sample

	s := New(m.F)
	start = time.Now()
	on, solves, conflicts := 0, 0, 0
	var last []bool
	for _, l := range order {
		// A model from an earlier query that already agrees answers the
		// question without searching.
		if last != nil && last[l.Var()] {
			s.AddClause(l)
			on++
			continue
		}
		solves++
		r := s.Solve(l)
		conflicts += r.Conflicts
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if r.Status == SAT {
			last = r.Model
			s.AddClause(l)
			on++
			continue
		}
		if len(r.Core) != 1 || r.Core[0] != l {
			t.Fatalf("core for %s should be the symbol alone, got %v", m.F.Name(l.Var()), r.Core)
		}
		s.AddClause(l.Neg())
		if last != nil && last[l.Var()] {
			last = nil
		}
	}
	incr := time.Since(start)
	final := s.Solve()
	if final.Status != SAT || final.Err != nil {
		t.Fatalf("fixed assignment is not a model: %v %v", final.Status, final.Err)
	}
	t.Logf("%d symbols, %d on, %d solves, %d conflicts", len(order), on, solves, conflicts)
	t.Logf("fresh solver per query: %v each, so ~%v for the pass",
		fresh.Round(time.Millisecond), (fresh * time.Duration(len(order))).Round(time.Second))
	t.Logf("incremental, whole pass: %v", incr.Round(time.Millisecond))
}
