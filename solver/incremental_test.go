package solver

import (
	"math/rand"
	"testing"

	"github.com/vinodhalaharvi/silt/cnf"
)

// brute decides satisfiability by enumeration. Slow and obviously right, which
// is the point: it is the oracle the solver is checked against.
func brute(n int, clauses [][]cnf.Lit, assume []cnf.Lit) bool {
	for m := 0; m < 1<<n; m++ {
		val := func(l cnf.Lit) bool { return (m>>(l.Var()-1))&1 == 1 == l.Sign() }
		ok := true
		for _, a := range assume {
			if !val(a) {
				ok = false
				break
			}
		}
		for _, c := range clauses {
			if !ok {
				break
			}
			sat := false
			for _, l := range c {
				if val(l) {
					sat = true
					break
				}
			}
			ok = sat
		}
		if ok {
			return true
		}
	}
	return false
}

func randLit(r *rand.Rand, n int) cnf.Lit {
	l := cnf.Lit(r.Intn(n) + 1)
	if r.Intn(2) == 0 {
		return l.Neg()
	}
	return l
}

// TestIncrementalAgainstBruteForce drives one Solver through many queries,
// adding clauses between some of them, and checks every answer: status
// against enumeration, every model against the clauses and assumptions, and
// every core for being a subset of the assumptions that is itself UNSAT.
//
// Reusing the solver is the whole thing under test. A learned clause that is
// only valid under the previous call's assumptions, a stale propagation
// frontier, or a level-0 fact lost on backtrack would each show up here as a
// wrong answer on a later query rather than on the first.
func TestIncrementalAgainstBruteForce(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	queries, unsats, cores := 0, 0, 0
	for inst := 0; inst < 300; inst++ {
		n := 4 + r.Intn(9)
		f := cnf.New()
		for v := 1; v <= n; v++ {
			f.Var(string(rune('A' + v - 1)))
		}
		var clauses [][]cnf.Lit
		for i := 0; i < n*(2+r.Intn(3)); i++ {
			c := []cnf.Lit{randLit(r, n), randLit(r, n), randLit(r, n)}
			clauses = append(clauses, c)
			f.Add("rand", c...)
		}
		s := New(f)
		for q := 0; q < 25; q++ {
			if r.Intn(6) == 0 {
				k := 1 + r.Intn(3)
				c := make([]cnf.Lit, k)
				for i := range c {
					c[i] = randLit(r, n)
				}
				clauses = append(clauses, c)
				s.AddClause(c...)
			}
			assume := make([]cnf.Lit, r.Intn(5))
			for i := range assume {
				assume[i] = randLit(r, n)
			}
			if r.Intn(4) == 0 {
				pol := map[int]bool{}
				for v := 1; v <= n; v++ {
					pol[v] = r.Intn(2) == 0
				}
				s.SetPolarity(pol)
			}
			queries++
			res := s.Solve(assume...)
			if res.Err != nil {
				t.Fatalf("inst %d query %d: %v", inst, q, res.Err)
			}
			want := brute(n, clauses, assume)
			if (res.Status == SAT) != want {
				t.Fatalf("inst %d query %d: got %v, enumeration says sat=%v", inst, q, res.Status, want)
			}
			switch res.Status {
			case SAT:
				for _, a := range assume {
					if res.Model[a.Var()] != a.Sign() {
						t.Fatalf("inst %d query %d: model violates assumption %d", inst, q, a)
					}
				}
			case UNSAT:
				unsats++
				in := map[cnf.Lit]bool{}
				for _, a := range assume {
					in[a] = true
				}
				for _, c := range res.Core {
					if !in[c] {
						t.Fatalf("inst %d query %d: core literal %d is not an assumption", inst, q, c)
					}
				}
				if brute(n, clauses, res.Core) {
					t.Fatalf("inst %d query %d: core %v is satisfiable", inst, q, res.Core)
				}
				if len(res.Core) < len(assume) {
					cores++
				}
			}
		}
	}
	t.Logf("%d queries, %d UNSAT, %d with a core smaller than the assumptions", queries, unsats, cores)
}

// TestCoreIsNotEverything is the property the old solver lacked: it returned
// all assumptions as the core. Rung 8's hitting sets need better raw material.
func TestCoreIsNotEverything(t *testing.T) {
	f := cnf.New()
	a, b, c, d := f.Var("A"), f.Var("B"), f.Var("C"), f.Var("D")
	f.Implies("a excludes b", a, b.Neg())
	r := New(f).Solve(c, a, d, b)
	if r.Status != UNSAT {
		t.Fatal(r.Status)
	}
	if len(r.Core) != 2 || r.Core[0] != a || r.Core[1] != b {
		t.Fatalf("core should be exactly {A, B}, got %v", r.Core)
	}
}

// TestLearningSurvivesCalls checks that a second, identical hard query costs
// fewer conflicts than the first. If learned clauses were discarded between
// calls, it would cost the same.
func TestLearningSurvivesCalls(t *testing.T) {
	f := cnf.New()
	const holes = 5
	// The pigeonhole is switched on by guard, so the formula is satisfiable
	// and only the assumption makes it UNSAT. Otherwise the first call would
	// prove the formula itself UNSAT and the second would answer without
	// searching, which tests nothing about retained learning.
	guard := f.Var("guard")
	p := func(i, j int) cnf.Lit { return f.Var(string(rune('a'+i)) + string(rune('0'+j))) }
	for i := 0; i <= holes; i++ {
		c := []cnf.Lit{guard.Neg()}
		for j := 0; j < holes; j++ {
			c = append(c, p(i, j))
		}
		f.Add("somewhere", c...)
	}
	for j := 0; j < holes; j++ {
		for i := 0; i <= holes; i++ {
			for k := i + 1; k <= holes; k++ {
				f.Add("exclusive", p(i, j).Neg(), p(k, j).Neg())
			}
		}
	}
	s := New(f)
	if r := s.Solve(); r.Status != SAT {
		t.Fatalf("without the guard the formula is satisfiable, got %v", r.Status)
	}
	first := s.Solve(guard)
	second := s.Solve(guard)
	if first.Status != UNSAT || second.Status != UNSAT {
		t.Fatalf("pigeonhole must be UNSAT: %v %v", first.Status, second.Status)
	}
	if len(first.Core) != 1 || first.Core[0] != guard {
		t.Fatalf("core should be the guard alone, got %v", first.Core)
	}
	if second.Conflicts >= first.Conflicts {
		t.Fatalf("second call learned nothing from the first: %d then %d conflicts",
			first.Conflicts, second.Conflicts)
	}
	t.Logf("%d conflicts, then %d", first.Conflicts, second.Conflicts)
}
