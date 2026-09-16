// Package cnf lowers a constraint model to conjunctive normal form.
//
// CNF is an AND of ORs: every SAT solver takes that shape and nothing else.
// Conversion uses the Tseitin transformation — a fresh variable per
// subexpression, constrained to equal it — because distributing ORs over ANDs
// on nested Kconfig conditions is exponential, while Tseitin is linear. The
// result is not logically identical to the input, but it is satisfiable
// exactly when the input is, which is all a solver needs.
package cnf

import (
	"fmt"
	"sort"
	"strings"
)

// Lit is a literal: a variable, negated when negative. Variables are numbered
// from 1 so that sign can carry negation, as DIMACS requires.
type Lit int

func (l Lit) Var() int   { return int(l.abs()) }
func (l Lit) Neg() Lit   { return -l }
func (l Lit) Sign() bool { return l > 0 }
func (l Lit) abs() Lit {
	if l < 0 {
		return -l
	}
	return l
}

// Clause is a disjunction of literals.
type Clause []Lit

// Formula is a conjunction of clauses, plus the naming needed to explain a
// result in the user's own vocabulary.
type Formula struct {
	Clauses []Clause
	names   map[string]int // variable name -> number
	rnames  []string       // number -> name, index 0 unused
	origin  map[int]string // clause index -> what produced it
}

func New() *Formula {
	return &Formula{names: map[string]int{}, rnames: []string{""}, origin: map[int]string{}}
}

// Var returns the variable for name, allocating on first use.
func (f *Formula) Var(name string) Lit {
	if v, ok := f.names[name]; ok {
		return Lit(v)
	}
	v := len(f.rnames)
	f.names[name] = v
	f.rnames = append(f.rnames, name)
	return Lit(v)
}

// Fresh allocates an unnamed auxiliary variable for Tseitin encoding.
func (f *Formula) Fresh(hint string) Lit {
	return f.Var(fmt.Sprintf("aux!%d!%s", len(f.rnames), hint))
}

// Name returns the variable's name, for explaining a model.
func (f *Formula) Name(v int) string {
	if v > 0 && v < len(f.rnames) {
		return f.rnames[v]
	}
	return fmt.Sprintf("?%d", v)
}

// IsAux reports whether a variable was introduced by the transformation rather
// than named by the user. Auxiliaries must never appear in an explanation.
func (f *Formula) IsAux(v int) bool { return strings.HasPrefix(f.Name(v), "aux!") }

func (f *Formula) NumVars() int { return len(f.rnames) - 1 }

// Add appends a clause, recording what produced it.
//
// Provenance survives into the CNF because a clause with no origin cannot be
// turned back into an explanation (Invariant 3).
func (f *Formula) Add(origin string, lits ...Lit) {
	f.origin[len(f.Clauses)] = origin
	f.Clauses = append(f.Clauses, Clause(lits))
}

// Origin returns what produced clause i.
func (f *Formula) Origin(i int) string { return f.origin[i] }

// FindOrigin returns the origin of the first clause matching lits exactly.
// Used to turn a solver-level conflict back into the Kconfig line that caused
// it, which is the whole reason clauses carry provenance.
func (f *Formula) FindOrigin(lits []Lit) string {
	for i, c := range f.Clauses {
		if len(c) != len(lits) {
			continue
		}
		same := true
		for j := range c {
			if c[j] != lits[j] {
				same = false
				break
			}
		}
		if same {
			return f.origin[i]
		}
	}
	return "(not found)"
}

// Implies adds a => b.
func (f *Formula) Implies(origin string, a, b Lit) { f.Add(origin, a.Neg(), b) }

// AtMostOne forbids more than one of lits being true, pairwise.
//
// Pairwise is quadratic and that is fine here: Kconfig choice groups are
// small — the largest in Buildroot has a few dozen members — and a
// commander or sequential encoding would add auxiliaries that then have to be
// filtered back out of every explanation.
func (f *Formula) AtMostOne(origin string, lits []Lit) {
	for i := 0; i < len(lits); i++ {
		for j := i + 1; j < len(lits); j++ {
			f.Add(origin, lits[i].Neg(), lits[j].Neg())
		}
	}
}

// DIMACS renders the formula in the standard solver interchange format, so it
// can be diffed against klocalizer --save-dimacs on the same tree.
func (f *Formula) DIMACS() string {
	var b strings.Builder
	fmt.Fprintf(&b, "p cnf %d %d\n", f.NumVars(), len(f.Clauses))
	for _, c := range f.Clauses {
		for _, l := range c {
			fmt.Fprintf(&b, "%d ", l)
		}
		b.WriteString("0\n")
	}
	return b.String()
}

// Mapping renders the variable table, without which a DIMACS file is
// unreadable by a human.
func (f *Formula) Mapping() string {
	type kv struct {
		v int
		n string
	}
	var all []kv
	for n, v := range f.names {
		all = append(all, kv{v, n})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v < all[j].v })
	var b strings.Builder
	for _, e := range all {
		fmt.Fprintf(&b, "c %d %s\n", e.v, e.n)
	}
	return b.String()
}
