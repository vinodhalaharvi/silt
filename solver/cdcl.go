// Package solver is a conflict-driven clause-learning SAT solver.
//
// DESIGN.md §12 rung 6 is explicit that writing this is tuition, not product:
// the advice never to write your own solver is right for a product and exactly
// inverted for learning. Unsat cores stop being magic once you have watched a
// learned clause come out of a conflict. It is meant to be replaced by a real
// solver once it has done its teaching, and it is kept behind an interface so
// that swap costs nothing.
//
// The solver is incremental in the MiniSat sense. One Solver answers many
// queries over the same formula: learned clauses survive between calls,
// clauses can be added after construction, and assumptions are not asserted
// into the formula but tried as the first decisions of each search, so they
// disappear when the call returns. That is what makes a forward pass over nine
// thousand Kconfig symbols one solver doing nine thousand cheap queries rather
// than nine thousand constructions of a hundred-thousand-clause formula.
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
	// Model is indexed by variable; index 0 is unused. It is a copy, so a
	// caller may keep it across later calls on the same Solver.
	Model []bool
	// Core, when UNSAT under assumptions, is a subset of the assumptions that
	// is unsatisfiable together with the formula. It is extracted from the
	// final conflict, so it is usually much smaller than the assumption list,
	// but it is not guaranteed minimal: minimising it is rung 8's job, and
	// claiming minimality here would be a lie the explanation layer inherits.
	// An empty Core with UNSAT means the formula is unsatisfiable on its own.
	Core []cnf.Lit
	// Conflicts is how many clauses this call learned. Useful for telling a
	// hard instance from a bug.
	Conflicts int
	// Err is set when the search produced something it could not verify. A
	// Result with Err is never an answer.
	Err error
}

// Value reports variable v in the model, false when v is outside it. Callers
// that look symbols up by name can allocate variables the solver never saw;
// those are unconstrained, and false is the value any model can give them.
func (r Result) Value(v int) bool { return v > 0 && v < len(r.Model) && r.Model[v] }

const (
	unassigned = 0
	vTrue      = 1
	vFalse     = -1
)

type clause struct {
	lits   []cnf.Lit
	learnt bool
}

// Solver holds the formula, what has been learned about it, and the search
// state of the current or most recent call.
type Solver struct {
	clauses []*clause // original clauses, which a model must satisfy
	learnts []*clause // derived clauses, implied by the originals
	watches [][]*clause

	value  []int8
	level  []int
	reason []*clause
	trail  []cnf.Lit
	lim    []int // trail index where each decision level began
	qhead  int   // propagation frontier into trail

	activity []float64
	bump     float64
	order    heap

	// polarity biases the value tried first for a variable. Kconfig defaults
	// are supplied here rather than as assumptions: a decision heuristic gets
	// a model close to the defaults in one search, and where a default cannot
	// hold the solver simply decides otherwise.
	polarity []int8

	nvars int
	// ok is false once the formula is known unsatisfiable with no
	// assumptions at all. Nothing added later can repair that.
	ok bool

	seen []bool // scratch for analysis, cleared after each use
}

// New builds a solver over a formula.
func New(f *cnf.Formula) *Solver {
	s := &Solver{bump: 1.0, ok: true}
	s.order.act = &s.activity
	s.grow(f.NumVars())
	for _, c := range f.Clauses {
		// Keep going after a contradiction so every clause is recorded; the
		// solver answers UNSAT regardless. Dropping one made an earlier
		// version report SAT for a formula containing both A and !A.
		s.AddClause(c...)
	}
	return s
}

// NumVars is the number of variables the solver knows about.
func (s *Solver) NumVars() int { return s.nvars }

// grow makes room for variables up to n, so clauses may mention variables the
// formula allocated after the solver was built.
func (s *Solver) grow(n int) {
	if len(s.value) == 0 {
		// Index 0 is unused, so variables index directly.
		s.value = []int8{0}
		s.level = []int{0}
		s.reason = []*clause{nil}
		s.activity = []float64{0}
		s.polarity = []int8{0}
		s.seen = []bool{false}
		s.order.ensure(0)
	}
	for s.nvars < n {
		s.nvars++
		s.value = append(s.value, 0)
		s.level = append(s.level, 0)
		s.reason = append(s.reason, nil)
		s.activity = append(s.activity, 0)
		s.polarity = append(s.polarity, 0)
		s.seen = append(s.seen, false)
		s.order.push(s.nvars)
	}
	for len(s.watches) < 2*(s.nvars+1) {
		s.watches = append(s.watches, nil)
	}
}

// watchIdx maps a literal to its slot in watches.
func watchIdx(l cnf.Lit) int {
	if l > 0 {
		return 2 * int(l)
	}
	return 2*int(-l) + 1
}

// SetPolarity biases which value each variable is tried at first. Variables
// absent from p go back to the default, which is false.
func (s *Solver) SetPolarity(p map[int]bool) {
	for v := range s.polarity {
		s.polarity[v] = 0
	}
	for v, want := range p {
		if v <= 0 {
			continue
		}
		s.grow(v)
		if want {
			s.polarity[v] = vTrue
		} else {
			s.polarity[v] = vFalse
		}
	}
}

// AddClause adds a clause permanently. It reports false when the formula has
// become unsatisfiable without any assumptions, which is final.
//
// Adding a clause discards the current search state but keeps everything
// learned, because learned clauses are consequences of the formula and adding
// a clause only strengthens it.
func (s *Solver) AddClause(lits ...cnf.Lit) bool {
	s.backtrack(0)
	if !s.ok {
		return false
	}
	maxv := 0
	for _, l := range lits {
		if l.Var() > maxv {
			maxv = l.Var()
		}
	}
	s.grow(maxv)

	// Simplify against what is already fixed: drop duplicates and literals
	// false at level 0, and discard the clause if it is a tautology or
	// already satisfied.
	out := make([]cnf.Lit, 0, len(lits))
	for _, l := range lits {
		if l == 0 {
			continue
		}
		switch s.litValue(l) {
		case vTrue:
			return true
		case vFalse:
			continue
		}
		dup := false
		for _, o := range out {
			if o == l {
				dup = true
				break
			}
			if o == l.Neg() {
				return true
			}
		}
		if !dup {
			out = append(out, l)
		}
	}

	switch len(out) {
	case 0:
		s.ok = false
		return false
	case 1:
		s.enqueue(out[0], nil)
		if s.propagate() != nil {
			s.ok = false
			return false
		}
		return true
	}
	c := &clause{lits: out}
	s.clauses = append(s.clauses, c)
	s.attach(c)
	return true
}

func (s *Solver) attach(c *clause) {
	i0, i1 := watchIdx(c.lits[0].Neg()), watchIdx(c.lits[1].Neg())
	s.watches[i0] = append(s.watches[i0], c)
	s.watches[i1] = append(s.watches[i1], c)
}

func (s *Solver) litValue(l cnf.Lit) int8 {
	v := s.value[l.Var()]
	if l.Sign() {
		return v
	}
	return -v
}

func (s *Solver) enqueue(l cnf.Lit, from *clause) {
	v := l.Var()
	if l.Sign() {
		s.value[v] = vTrue
	} else {
		s.value[v] = vFalse
	}
	s.level[v] = s.decisionLevel()
	s.reason[v] = from
	s.trail = append(s.trail, l)
}

func (s *Solver) decisionLevel() int { return len(s.lim) }

// propagate runs unit propagation from the frontier to a fixed point,
// returning the conflicting clause if one is found.
func (s *Solver) propagate() *clause {
	for s.qhead < len(s.trail) {
		p := s.trail[s.qhead]
		s.qhead++
		// Clauses watching p are those in which !p has just become false.
		wi := watchIdx(p)
		ws := s.watches[wi]
		// Rebuild the list in place. Entries are only ever moved to lists of
		// other literals while this one is being rebuilt, so j never passes i.
		// A clause cannot watch the same literal twice, which is what keeps
		// that true; the aliasing bug an earlier version had came from
		// appending to this very list mid-iteration.
		falsified := p.Neg()
		j := 0
		for i := 0; i < len(ws); i++ {
			c := ws[i]
			if c.lits[0] == falsified {
				c.lits[0], c.lits[1] = c.lits[1], c.lits[0]
			}
			if s.litValue(c.lits[0]) == vTrue {
				ws[j] = c
				j++
				continue
			}
			moved := false
			for k := 2; k < len(c.lits); k++ {
				if s.litValue(c.lits[k]) != vFalse {
					c.lits[1], c.lits[k] = c.lits[k], c.lits[1]
					ni := watchIdx(c.lits[1].Neg())
					s.watches[ni] = append(s.watches[ni], c)
					moved = true
					break
				}
			}
			if moved {
				continue
			}
			ws[j] = c
			j++
			switch s.litValue(c.lits[0]) {
			case vFalse:
				// Conflict: keep the unvisited remainder watched and stop.
				j += copy(ws[j:], ws[i+1:])
				s.watches[wi] = ws[:j]
				s.qhead = len(s.trail)
				return c
			case unassigned:
				s.enqueue(c.lits[0], c)
			}
		}
		s.watches[wi] = ws[:j]
	}
	return nil
}

// analyze derives a learned clause from a conflict, using the first unique
// implication point. The asserting literal is placed first and the literal
// with the next-highest level second, so the clause can be watched on those
// two and is unit immediately after the backjump.
func (s *Solver) analyze(confl *clause) ([]cnf.Lit, int) {
	learnt := []cnf.Lit{0}
	pathC := 0
	var p cnf.Lit
	idx := len(s.trail) - 1
	c := confl
	for {
		for _, q := range c.lits {
			v := q.Var()
			if p != 0 && v == p.Var() {
				continue
			}
			if s.seen[v] || s.level[v] == 0 {
				continue
			}
			s.seen[v] = true
			s.bumpVar(v)
			if s.level[v] >= s.decisionLevel() {
				pathC++
			} else {
				learnt = append(learnt, q)
			}
		}
		for !s.seen[s.trail[idx].Var()] {
			idx--
		}
		p = s.trail[idx]
		idx--
		c = s.reason[p.Var()]
		s.seen[p.Var()] = false
		pathC--
		if pathC <= 0 {
			break
		}
	}
	learnt[0] = p.Neg()
	for _, l := range learnt[1:] {
		s.seen[l.Var()] = false
	}

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

// analyzeFinal explains why assumption a is false under the assumptions
// decided so far: it walks the implication graph back from !a and collects
// the assumptions it reaches. Every decision on the trail at this point is an
// assumption, because assumptions are always decided before anything else.
func (s *Solver) analyzeFinal(a cnf.Lit) []cnf.Lit {
	core := []cnf.Lit{a}
	v := a.Var()
	if s.level[v] == 0 {
		return core
	}
	s.seen[v] = true
	for i := len(s.trail) - 1; i >= s.lim[0]; i-- {
		x := s.trail[i].Var()
		if !s.seen[x] {
			continue
		}
		if r := s.reason[x]; r == nil {
			// Including x == v: assuming both A and !A decides A and then
			// finds !A false, and the core is both of them, not just one.
			core = append(core, s.trail[i])
		} else {
			for _, q := range r.lits {
				if q.Var() != x && s.level[q.Var()] > 0 {
					s.seen[q.Var()] = true
				}
			}
		}
		s.seen[x] = false
	}
	return core
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
		if !s.order.contains(v) {
			s.order.push(v)
		}
	}
	s.trail = s.trail[:s.lim[level]]
	s.lim = s.lim[:level]
	s.qhead = len(s.trail)
}

func (s *Solver) bumpVar(v int) {
	s.activity[v] += s.bump
	if s.activity[v] > 1e100 {
		for i := range s.activity {
			s.activity[i] *= 1e-100
		}
		s.bump *= 1e-100
	}
	if s.order.contains(v) {
		s.order.up(v)
	}
}

// pick chooses the unassigned variable with the highest activity, lowest
// number first among ties.
func (s *Solver) pick() cnf.Lit {
	for s.order.len() > 0 {
		v := s.order.pop()
		if s.value[v] != unassigned {
			continue
		}
		// Default polarity false: most Kconfig symbols are off, so guessing
		// off converges far faster on this shape of problem.
		if s.polarity[v] == vTrue {
			return cnf.Lit(v)
		}
		return cnf.Lit(-v)
	}
	return 0
}

// Solve searches for a model under assumptions. It may be called any number of
// times; each call starts from what earlier calls learned and leaves the
// assumptions behind when it returns.
func (s *Solver) Solve(assumptions ...cnf.Lit) Result {
	s.backtrack(0)
	if !s.ok {
		return Result{Status: UNSAT}
	}
	for _, a := range assumptions {
		s.grow(a.Var())
	}
	if s.propagate() != nil {
		s.ok = false
		return Result{Status: UNSAT}
	}

	conflicts := 0
	restart := 0
	budget := 100 * luby(restart)
	sinceRestart := 0

	for {
		if confl := s.propagate(); confl != nil {
			conflicts++
			sinceRestart++
			if s.decisionLevel() == 0 {
				s.ok = false
				return Result{Status: UNSAT, Conflicts: conflicts}
			}
			learnt, back := s.analyze(confl)
			s.backtrack(back)
			if len(learnt) == 1 {
				// A unit learned clause is a fact about the formula: it goes
				// in at level 0 and is never retracted.
				s.enqueue(learnt[0], nil)
			} else {
				lc := &clause{lits: learnt, learnt: true}
				s.learnts = append(s.learnts, lc)
				s.attach(lc)
				s.enqueue(learnt[0], lc)
			}
			s.bump /= 0.95
			continue
		}

		if sinceRestart >= budget {
			// Restarts undo decisions, not learning. Assumptions are simply
			// decided again on the way back down.
			s.backtrack(0)
			restart++
			budget = 100 * luby(restart)
			sinceRestart = 0
			continue
		}

		var next cnf.Lit
		for s.decisionLevel() < len(assumptions) {
			a := assumptions[s.decisionLevel()]
			switch s.litValue(a) {
			case vTrue:
				// Already holds: open an empty level so level numbers keep
				// lining up with assumption indices.
				s.lim = append(s.lim, len(s.trail))
				continue
			case vFalse:
				core := s.analyzeFinal(a)
				sort.Slice(core, func(i, j int) bool { return core[i].Var() < core[j].Var() })
				return Result{Status: UNSAT, Core: core, Conflicts: conflicts}
			}
			next = a
			break
		}
		if next == 0 {
			next = s.pick()
			if next == 0 {
				return s.model(conflicts)
			}
		}
		s.lim = append(s.lim, len(s.trail))
		s.enqueue(next, nil)
	}
}

// model packages a total assignment, verified against every original clause.
func (s *Solver) model(conflicts int) Result {
	m := make([]bool, s.nvars+1)
	for v := 1; v <= s.nvars; v++ {
		m[v] = s.value[v] == vTrue
	}
	// Verify before claiming SAT. A solver that reports a model it cannot
	// justify is worse than one that admits defeat: the whole project exists
	// because kbuild answers silently and wrongly.
	if bad := s.unsatisfied(m); bad >= 0 {
		return Result{Status: Unknown, Conflicts: conflicts,
			Err: fmt.Errorf("internal: clause %d unsatisfied by the model "+
				"the search produced; this is a solver bug, not an answer", bad)}
	}
	return Result{Status: SAT, Model: m, Conflicts: conflicts}
}

// unsatisfied returns the index of an original clause the model fails to
// satisfy, or -1. Unit clauses live on the level-0 trail rather than in the
// clause list, so those are checked too.
func (s *Solver) unsatisfied(m []bool) int {
	for i, c := range s.clauses {
		ok := false
		for _, l := range c.lits {
			if m[l.Var()] == l.Sign() {
				ok = true
				break
			}
		}
		if !ok {
			return i
		}
	}
	for _, l := range s.trail {
		if s.level[l.Var()] == 0 && m[l.Var()] != l.Sign() {
			return -2 - l.Var()
		}
	}
	return -1
}

// luby is the Luby restart sequence 1 1 2 1 1 2 4 ...
func luby(i int) int {
	size, seq := 1, 0
	for size < i+1 {
		seq++
		size = 2*size + 1
	}
	x := i
	for size-1 != x {
		size = (size - 1) >> 1
		seq--
		x %= size
	}
	return 1 << seq
}
