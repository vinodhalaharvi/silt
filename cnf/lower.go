package cnf

import (
	"fmt"

	"github.com/vinodhalaharvi/silt/kconfig"
)

// Model is a lowered Kconfig tree.
type Model struct {
	F    *Formula
	Tree *kconfig.Tree
	// Skipped records symbols excluded from the model and why, because an
	// undeclared exclusion is indistinguishable from a bug (Invariant 9).
	Skipped map[string]string
}

// varFor names the Boolean for a symbol being enabled.
//
// Buildroot has no tristate symbols at all, so one Boolean per symbol is exact
// there. For a tristate tree, a second variable per symbol plus at-most-one is
// needed; that is Lower's tristate path.
func varFor(f *Formula, sym string) Lit { return f.Var(sym) }

// varForM names the Boolean for a symbol being a module.
func varForM(f *Formula, sym string) Lit { return f.Var(sym + "!m") }

// varSet names the Boolean for a string symbol being non-empty.
//
// 154 places in Buildroot compare a string inside a real depends-on clause, so
// treating strings as wholly absent from the model would leave out constraints
// kbuild enforces (DESIGN.md §8.3).
func varSet(f *Formula, sym string) Lit { return f.Var(sym + "!set") }

// Lower turns an imported tree into CNF.
func Lower(t *kconfig.Tree) *Model {
	m := &Model{F: New(), Tree: t, Skipped: map[string]string{}}
	f := m.F

	for _, name := range t.Order {
		s := t.Symbols[name]
		where := fmt.Sprintf("%s:%d", s.File, s.Line)

		switch s.Type {
		case kconfig.Bool:
			// nothing structural beyond the dependency clauses below
		case kconfig.Tristate:
			// n < m < y as two Booleans with at-most-one.
			f.Add(where+" tristate", varFor(f, name).Neg(), varForM(f, name).Neg())
		case kconfig.String, kconfig.Int, kconfig.Hex:
			// Value is not modelled; only emptiness is.
			m.Skipped[name] = string(s.Type.String()) + ": value not modelled, emptiness is"
		default:
			m.Skipped[name] = "untyped"
			continue
		}

		// depends on: enabling the symbol requires the condition.
		if s.Depends != nil {
			cond := m.lowerExpr(s.Depends, where)
			sym := varFor(f, name)
			if !s.Type.Solvable() {
				sym = varSet(f, name)
			}
			f.Implies(where+" depends", sym, cond)
		}

		// select: the target is forced on WITHOUT visiting its own depends.
		// That is what the C implementation does, and modelling it any other
		// way would make Silt predict configurations kbuild does not produce
		// (DESIGN.md §8.1).
		for _, sel := range s.Selects {
			origin := where + " select " + sel.Target
			ante := varFor(f, name)
			if sel.Cond != nil {
				g := m.lowerExpr(sel.Cond, origin)
				aux := f.Fresh("sel")
				// aux <-> (name && cond)
				f.Add(origin, aux.Neg(), ante)
				f.Add(origin, aux.Neg(), g)
				f.Add(origin, aux, ante.Neg(), g.Neg())
				f.Implies(origin, aux, varFor(f, sel.Target))
				continue
			}
			f.Implies(origin, ante, varFor(f, sel.Target))
		}
	}

	// choice groups: at most one member enabled.
	for _, c := range t.Choices {
		if len(c.Members) < 2 {
			continue
		}
		lits := make([]Lit, 0, len(c.Members))
		for _, mem := range c.Members {
			lits = append(lits, varFor(f, mem))
		}
		origin := fmt.Sprintf("%s:%d choice", c.File, c.Line)
		f.AtMostOne(origin, lits)
		// Deliberately NOT emitting an at-least-one clause.
		//
		// Kconfig does not force a member on when none is visible: a choice
		// whose members all have unmet dependencies is simply left unset.
		// Encoding "some member must be true" makes assuming an unrelated
		// symbol activate a choice guard and then contradict, which is exactly
		// what it did — assuming BR2_STATIC_LIBS alone came back UNSAT.
		//
		// This under-approximates: the model admits configurations where a
		// non-optional choice has no member selected, which kbuild would fill
		// in from defaults. That is the safe direction — it never rejects a
		// configuration kbuild accepts — and completing a choice from its
		// defaults is the completion problem, which is rung 7.
		m.Skipped[origin] = "choice at-least-one not modelled; see cnf/lower.go"
	}
	return m
}

// lowerExpr Tseitin-encodes a Kconfig expression, returning a literal that is
// true exactly when the expression is.
func (m *Model) lowerExpr(e *kconfig.Expr, origin string) Lit {
	f := m.F
	switch e.Op {
	case kconfig.ExprSym:
		switch e.Sym {
		case "y", "m":
			t := f.Var("!true")
			f.Add("constant", t)
			return t
		case "n":
			t := f.Var("!true")
			f.Add("constant", t)
			return t.Neg()
		}
		if s, ok := m.Tree.Symbols[e.Sym]; ok && !s.Type.Solvable() {
			return varSet(f, e.Sym)
		}
		return varFor(f, e.Sym)

	case kconfig.ExprNot:
		return m.lowerExpr(e.Args[0], origin).Neg()

	case kconfig.ExprAnd, kconfig.ExprOr:
		a := m.lowerExpr(e.Args[0], origin)
		b := m.lowerExpr(e.Args[1], origin)
		aux := f.Fresh("op")
		if e.Op == kconfig.ExprAnd {
			f.Add(origin, aux.Neg(), a)
			f.Add(origin, aux.Neg(), b)
			f.Add(origin, aux, a.Neg(), b.Neg())
		} else {
			f.Add(origin, aux, a.Neg())
			f.Add(origin, aux, b.Neg())
			f.Add(origin, aux.Neg(), a, b)
		}
		return aux

	case kconfig.ExprEq, kconfig.ExprNeq:
		// Only emptiness is modelled. X != "" is "X is set"; every other
		// comparison against a literal is unmodelled and declared as such.
		if e.IsLit && e.Lit == "" {
			l := varSet(f, e.Sym)
			if e.Op == kconfig.ExprNeq {
				return l
			}
			return l.Neg()
		}
		if e.Sym == "y" || e.Lit == "y" {
			sym := e.Sym
			if sym == "y" {
				sym = e.Lit
			}
			l := varFor(f, sym)
			if e.Op == kconfig.ExprNeq {
				return l.Neg()
			}
			return l
		}
		m.Skipped[e.Sym] = "literal comparison not modelled: " + e.String()
		// Unconstrained: a fresh variable with no clauses, so the solver may
		// choose either way. Pretending to know would be worse than admitting
		// the gap.
		return f.Fresh("cmp")
	}
	return f.Fresh("unknown")
}
