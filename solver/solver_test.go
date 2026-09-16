package solver

import (
	"testing"

	"github.com/vinodhalaharvi/silt/cnf"
)

func TestTrivial(t *testing.T) {
	f := cnf.New()
	a, b := f.Var("A"), f.Var("B")
	f.Implies("t", a, b)
	r := New(f).Solve(a)
	if r.Status != SAT || !r.Model[b.Var()] {
		t.Fatalf("A implies B: %v %v", r.Status, r.Model)
	}
}

func TestContradiction(t *testing.T) {
	f := cnf.New()
	a := f.Var("A")
	f.Add("t", a)
	f.Add("t", a.Neg())
	if r := New(f).Solve(); r.Status != UNSAT {
		t.Fatalf("got %v", r.Status)
	}
}

func TestAtMostOne(t *testing.T) {
	f := cnf.New()
	a, b, c := f.Var("A"), f.Var("B"), f.Var("C")
	f.AtMostOne("choice", []cnf.Lit{a, b, c})
	f.Add("at-least-one", a, b, c)
	r := New(f).Solve(a)
	if r.Status != SAT {
		t.Fatal(r.Status)
	}
	if r.Model[b.Var()] || r.Model[c.Var()] {
		t.Fatal("at-most-one violated")
	}
	if r := New(f).Solve(a, b); r.Status != UNSAT {
		t.Fatal("two members of a choice must be UNSAT")
	}
}

// Pigeonhole is small but needs real clause learning: without it, search
// blows up. Three pigeons in two holes is unsatisfiable.
// Pigeonhole needs real clause learning: without it the search blows up.
// Three pigeons in two holes is unsatisfiable.
func TestPigeonhole(t *testing.T) {
	f := cnf.New()
	p := func(i, j int) cnf.Lit { return f.Var(string(rune('a'+i)) + string(rune('0'+j))) }
	for i := 0; i < 3; i++ {
		f.Add("somewhere", p(i, 0), p(i, 1))
	}
	for j := 0; j < 2; j++ {
		for i := 0; i < 3; i++ {
			for k := i + 1; k < 3; k++ {
				f.Add("exclusive", p(i, j).Neg(), p(k, j).Neg())
			}
		}
	}
	if r := New(f).Solve(); r.Status != UNSAT {
		t.Fatalf("3 pigeons, 2 holes must be UNSAT, got %v", r.Status)
	}
}

func TestChainPropagation(t *testing.T) {
	f := cnf.New()
	prev := f.Var("S0")
	for i := 1; i < 200; i++ {
		next := f.Var("S" + string(rune('0'+i%10)) + string(rune('a'+i/10)))
		f.Implies("chain", prev, next)
		prev = next
	}
	r := New(f).Solve(f.Var("S0"))
	if r.Status != SAT || !r.Model[prev.Var()] {
		t.Fatal("implication chain did not propagate to the end")
	}
}
