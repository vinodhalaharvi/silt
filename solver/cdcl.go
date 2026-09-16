// Package solver is a conflict-driven clause-learning SAT solver.
//
// DESIGN.md §12 rung 6 is explicit that writing this is tuition, not product:
// the advice never to write your own solver is right for a product and exactly
// inverted for learning. Unsat cores stop being magic once you have watched a
// learned clause come out of a conflict. It is meant to be replaced by a real
// solver once it has done its teaching, and it is kept behind an interface so
// that swap costs nothing.
package solver

import (
	"fmt"
	"sort"

	"github.com/vinodhalaharvi/silt/cnf"
)

// Status is the outcome of a solve.
type Status uint8

const (
	Unknown Status = iota
	SAT
	UNSAT
)

func (s Status) String() string {
	switch s {
	case SAT:
		return "SAT"
	case UNSAT:
		return "UNSAT"
	}
	return "UNKNOWN"
}

// Result carries the outcome and, when SAT, the assignment.
type Result struct {
	Status Status
	Model  map[int]bool
	// Core, when UNSAT under assumptions, is the subset of assumptions that
	// caused it. This is the raw material for an explanation — not the
	// explanation itself (DESIGN.md §11.1).
	Core []cnf.Lit
	// Conflicts is how many times the solver learned a clause. Useful for
	// telling a hard instance from a bug.
	Conflicts int
	// Err is set when the search produced something it could not verify. A
	// Result with Err is never an answer.
	Err error
}

const (
	unassigned = 0
	vTrue      = 1
	vFalse     = -1
)

type clause struct {
	lits []cnf.Lit
}

// Solver holds the mutable search state.
type Solver struct {
	clauses []*clause
	watches map[cnf.Lit][]*clause // literal -> clauses watching it

	value  []int8    // per variable: unassigned/vTrue/vFalse
	level  []int     // decision level at which each var was assigned
	reason []*clause // implying clause, nil for decisions
	trail  []cnf.Lit // assignment order
	lim    []int     // trail index where each decision level began

	activity []float64
	bump     float64

	nvars     int
	conflicts int
	broken    bool         // a unit clause contradicted another at construction
	assumps   map[int]bool // variable -> assumed sign, for core extraction
}

// New builds a solver over a formula.
func New(f *cnf.Formula) *Solver {
	n := f.NumVars()
	s := &Solver{
		watches:  map[cnf.Lit][]*clause{},
		value:    make([]int8, n+1),
		level:    make([]int, n+1),
		reason:   make([]*clause, n+1),
		activity: make([]float64, n+1),
		bump:     1.0,
		nvars:    n,
	}
	for _, c := range f.Clauses {
		if !s.addClause(append([]cnf.Lit(nil), c...)) {
			// Two unit clauses contradicted, or an empty clause appeared.
			// Dropping this made the solver report SAT for a formula
			// containing both A and !A.
			s.broken = true
		}
	}
	return s
}

func (s *Solver) addClause(lits []cnf.Lit) bool {
	// Drop duplicates and detect tautologies.
	seen := map[cnf.Lit]bool{}
	var out []cnf.Lit
	for _, l := range lits {
		if seen[l.Neg()] {
			return true // tautology: always satisfied
		}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return false // empty clause: formula is unsatisfiable
	}
	c := &clause{lits: out}
	s.clauses = append(s.clauses, c)
	if len(out) == 1 {
		return s.enqueue(out[0], nil)
	}
	// Two-watched-literal scheme: only two literals per clause are watched, so
	// propagation touches a clause only when one of them becomes false.
	s.watches[out[0].Neg()] = append(s.watches[out[0].Neg()], c)
	s.watches[out[1].Neg()] = append(s.watches[out[1].Neg()], c)
	return true
}

func (s *Solver) litValue(l cnf.Lit) int8 {
	v := s.value[l.Var()]
	if v == unassigned {
		return unassigned
	}
	if l.Sign() {
		return v
	}
	return -v
}

func (s *Solver) enqueue(l cnf.Lit, from *clause) bool {
	switch s.litValue(l) {
	case vTrue:
		return true
	case vFalse:
		return false
	}
	v := l.Var()
	if l.Sign() {
		s.value[v] = vTrue
	} else {
		s.value[v] = vFalse
	}
	s.level[v] = s.decisionLevel()
	s.reason[v] = from
	s.trail = append(s.trail, l)
	return true
}

func (s *Solver) decisionLevel() int { return len(s.lim) }

// propagate runs unit propagation to a fixed point, returning the conflicting
// clause if one is found.
func (s *Solver) propagate(from int) *clause {
	for from < len(s.trail) {
		l := s.trail[from]
		from++
		// Copy before truncating: handleWatch appends to s.watches[l], which
		// would otherwise overwrite the entries still being iterated, since
		// the truncated slice aliases the same backing array.
		ws := append([]*clause(nil), s.watches[l]...)
		s.watches[l] = s.watches[l][:0]
		for i, c := range ws {
			if !s.handleWatch(c, l) {
				// Conflict: restore the untouched remainder and report.
				s.watches[l] = append(s.watches[l], ws[i:]...)
				return c
			}
		}
	}
	return nil
}

func (s *Solver) handleWatch(c *clause, falsified cnf.Lit) bool {
	// Keep the falsified literal in position 1.
	if c.lits[0].Neg() == falsified {
		c.lits[0], c.lits[1] = c.lits[1], c.lits[0]
	}
	if s.litValue(c.lits[0]) == vTrue {
		s.watches[falsified] = append(s.watches[falsified], c)
		return true
	}
	// Look for a new literal to watch.
	for i := 2; i < len(c.lits); i++ {
		if s.litValue(c.lits[i]) != vFalse {
			c.lits[1], c.lits[i] = c.lits[i], c.lits[1]
			nl := c.lits[1].Neg()
			s.watches[nl] = append(s.watches[nl], c)
			return true
		}
	}
	// None found: the clause is unit under the current assignment.
	s.watches[falsified] = append(s.watches[falsified], c)
	return s.enqueue(c.lits[0], c)
}

// analyze derives a learned clause from a conflict, using the first unique
// implication point. This is the step that makes cores possible: the learned
// clause names exactly the decisions responsible.
func (s *Solver) analyze(conflict *clause) ([]cnf.Lit, int) {
	seen := make([]bool, s.nvars+1)
	var learnt []cnf.Lit
	counter := 0
	var p cnf.Lit
	idx := len(s.trail) - 1
	c := conflict

	for {
		for _, q := range c.lits {
			if q == p {
				continue
			}
			v := q.Var()
			if seen[v] || s.level[v] == 0 {
				continue
			}
			seen[v] = true
			s.activity[v] += s.bump
			if s.level[v] >= s.decisionLevel() {
				counter++
			} else {
				learnt = append(learnt, q)
			}
		}
		for idx >= 0 && !seen[s.trail[idx].Var()] {
			idx--
		}
		if idx < 0 {
			break
		}
		p = s.trail[idx]
		seen[p.Var()] = false
		counter--
		if counter <= 0 {
			break
		}
		c = s.reason[p.Var()]
		if c == nil {
			break
		}
		idx--
	}
	learnt = append([]cnf.Lit{p.Neg()}, learnt...)

	// Backjump to the second-highest level in the learned clause.
	back := 0
	if len(learnt) > 1 {
		best := 1
		for i := 2; i < len(learnt); i++ {
			if s.level[learnt[i].Var()] > s.level[learnt[best].Var()] {
				best = i
			}
		}
		learnt[1], learnt[best] = learnt[best], learnt[1]
		back = s.level[learnt[1].Var()]
	}
	return learnt, back
}

func (s *Solver) backtrack(level int) {
	if s.decisionLevel() <= level {
		return
	}
	for i := len(s.trail) - 1; i >= s.lim[level]; i-- {
		v := s.trail[i].Var()
		s.value[v] = unassigned
		s.reason[v] = nil
		s.level[v] = 0
	}
	s.trail = s.trail[:s.lim[level]]
	s.lim = s.lim[:level]
}

// pick chooses the unassigned variable with the highest activity.
func (s *Solver) pick() cnf.Lit {
	best, bestAct := 0, -1.0
	for v := 1; v <= s.nvars; v++ {
		if s.value[v] == unassigned && s.activity[v] > bestAct {
			best, bestAct = v, s.activity[v]
		}
	}
	if best == 0 {
		return 0
	}
	// Default polarity false: most Kconfig symbols are off, so guessing off
	// converges far faster on this shape of problem.
	return cnf.Lit(-best)
}

// Solve searches for a model, optionally under assumptions.
func (s *Solver) Solve(assumptions ...cnf.Lit) Result {
	if s.broken {
		return Result{Status: UNSAT}
	}
	s.assumps = map[int]bool{}
	for _, a := range assumptions {
		s.assumps[a.Var()] = a.Sign()
	}
	if c := s.propagate(0); c != nil {
		return Result{Status: UNSAT, Conflicts: s.conflicts}
	}
	head := len(s.trail)

	for _, a := range assumptions {
		if s.litValue(a) == vFalse {
			return Result{Status: UNSAT, Core: s.coreFrom(assumptions), Conflicts: s.conflicts}
		}
		if s.litValue(a) == vTrue {
			continue
		}
		s.lim = append(s.lim, len(s.trail))
		s.enqueue(a, nil)
		if c := s.propagate(head); c != nil {
			return Result{Status: UNSAT, Core: s.coreFrom(assumptions), Conflicts: s.conflicts}
		}
		head = len(s.trail)
	}
	assumpLevel := s.decisionLevel()

	for {
		if c := s.propagate(head); c != nil {
			head = len(s.trail)
			s.conflicts++
			s.bump *= 1.02
			if s.decisionLevel() <= assumpLevel {
				return Result{Status: UNSAT, Core: s.coreFrom(assumptions), Conflicts: s.conflicts}
			}
			learnt, back := s.analyze(c)
			if back < assumpLevel {
				back = assumpLevel
			}
			s.backtrack(back)
			if len(learnt) == 1 {
				s.enqueue(learnt[0], nil)
			} else {
				lc := &clause{lits: learnt}
				s.clauses = append(s.clauses, lc)
				s.watches[learnt[0].Neg()] = append(s.watches[learnt[0].Neg()], lc)
				s.watches[learnt[1].Neg()] = append(s.watches[learnt[1].Neg()], lc)
				s.enqueue(learnt[0], lc)
			}
			head = len(s.trail)
			continue
		}
		head = len(s.trail)

		l := s.pick()
		if l == 0 {
			model := map[int]bool{}
			for v := 1; v <= s.nvars; v++ {
				model[v] = s.value[v] == vTrue
			}
			// Verify before claiming SAT. A solver that reports a model it
			// cannot justify is worse than one that admits defeat: the whole
			// project exists because kbuild answers silently and wrongly.
			if bad := s.unsatisfied(model); bad >= 0 {
				return Result{Status: Unknown, Conflicts: s.conflicts,
					Err: fmt.Errorf("internal: clause %d unsatisfied by the model "+
						"the search produced; this is a solver bug, not an answer", bad)}
			}
			return Result{Status: SAT, Model: model, Conflicts: s.conflicts}
		}
		s.lim = append(s.lim, len(s.trail))
		s.enqueue(l, nil)
	}
}

// coreFrom returns the assumptions implicated in an unsatisfiable result.
//
// This is deliberately a coarse over-approximation: every assumption whose
// variable the search touched. A minimal core needs the MUS work of rung 8;
// claiming minimality here would be a lie the explanation layer then inherits.
func (s *Solver) coreFrom(assumptions []cnf.Lit) []cnf.Lit {
	out := append([]cnf.Lit(nil), assumptions...)
	sort.Slice(out, func(i, j int) bool { return out[i].Var() < out[j].Var() })
	return out
}

// unsatisfied returns the index of a clause the model fails to satisfy, or -1.
func (s *Solver) unsatisfied(model map[int]bool) int {
	for i, c := range s.clauses {
		ok := false
		for _, l := range c.lits {
			if model[l.Var()] == l.Sign() {
				ok = true
				break
			}
		}
		if !ok {
			return i
		}
	}
	return -1
}
