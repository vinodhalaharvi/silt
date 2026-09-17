package cnf

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vinodhalaharvi/silt/kconfig"
)

// Values of string, int and hex symbols, as a finite domain.
//
// A Kconfig comparison can only distinguish a symbol's value from the
// literals it is compared against. So a symbol's domain is those literals,
// the literals its defaults can produce, the empty string, and one variable
// standing for every other value. Exactly one holds. That is exact for every
// comparison against a literal, which is all Buildroot's depends-on and
// select conditions contain: 46 in depends (40 on the env-derived
// BR2_HOSTARCH, 6 on BR2_UCLIBC_TARGET_ARCH) and 4 in select conditions (on
// BR2_ENDIAN). The other 63 are in default conditions, which this model does
// not lower; the evaluator in kconfig handles those.
type domain struct {
	sym    string
	typ    kconfig.Type
	values map[string]Lit // canonical value -> literal
	other  Lit
}

// canonical folds values kbuild compares as equal. Int and hex symbols are
// compared numerically when both sides parse, so 0x10 and 16 are one value
// for a hex symbol.
func canonical(t kconfig.Type, v string) string {
	switch t {
	case kconfig.Int:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return strconv.FormatInt(n, 10)
		}
	case kconfig.Hex:
		s := strings.TrimPrefix(strings.TrimPrefix(v, "0x"), "0X")
		if n, err := strconv.ParseUint(s, 16, 64); err == nil && s != "" {
			return "0x" + strconv.FormatUint(n, 16)
		}
	}
	return v
}

// collectLiterals finds, for every non-bool symbol, the literals it is
// compared against or defaults to.
func collectLiterals(t *kconfig.Tree) map[string]map[string]bool {
	lits := map[string]map[string]bool{}
	add := func(sym, v string) {
		s, ok := t.Symbols[sym]
		if !ok || s.Type.Solvable() || s.Type == kconfig.Unknown {
			return
		}
		if lits[sym] == nil {
			lits[sym] = map[string]bool{}
		}
		lits[sym][canonical(s.Type, v)] = true
	}
	var walk func(e *kconfig.Expr)
	walk = func(e *kconfig.Expr) {
		if e == nil {
			return
		}
		if e.Op == kconfig.ExprEq || e.Op == kconfig.ExprNeq {
			if e.IsLit || t.Symbols[e.Lit] == nil {
				add(e.Sym, e.Lit)
			}
		}
		for _, a := range e.Args {
			walk(a)
		}
	}
	for _, name := range t.Order {
		s := t.Symbols[name]
		walk(s.Depends)
		for _, sel := range s.Selects {
			walk(sel.Cond)
		}
		for _, d := range s.Defaults {
			walk(d.Cond)
			if v, ok := quotedLiteral(d.Value); ok {
				add(name, v)
			}
		}
		if s.EnvVar != "" {
			if v, ok := t.Env[s.EnvVar]; ok {
				add(name, v)
			}
		}
	}
	for _, c := range t.Choices {
		walk(c.Depends)
	}
	return lits
}

func quotedLiteral(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' && strings.Count(v, `"`) == 2 {
		return v[1 : len(v)-1], true
	}
	return "", false
}

// buildDomains allocates the value variables and the exactly-one clauses.
func (m *Model) buildDomains() {
	f := m.F
	lits := collectLiterals(m.Tree)
	m.domains = map[string]*domain{}
	for _, name := range m.Tree.Order {
		s := m.Tree.Symbols[name]
		switch s.Type {
		case kconfig.String, kconfig.Int, kconfig.Hex:
		default:
			continue
		}
		d := &domain{sym: name, typ: s.Type, values: map[string]Lit{}}
		vals := []string{""}
		for v := range lits[name] {
			if v != "" {
				vals = append(vals, v)
			}
		}
		sort.Strings(vals[1:])
		all := make([]Lit, 0, len(vals)+1)
		for _, v := range vals {
			l := f.Var(fmt.Sprintf("%s=%q", name, v))
			d.values[v] = l
			all = append(all, l)
		}
		d.other = f.Var(name + "=!other")
		all = append(all, d.other)
		origin := fmt.Sprintf("%s:%d value", s.File, s.Line)
		f.Add(origin, all...)
		f.AtMostOne(origin, all)
		// set? is "not the empty string".
		f.Add(origin, varSet(f, name).Neg(), d.values[""].Neg())
		f.Add(origin, varSet(f, name), d.values[""])
		m.domains[name] = d
	}
}

// ValueLit is the literal for a non-bool symbol holding value v, or false if
// the symbol has no domain. A value no comparison mentions is "other": no
// condition in the tree can tell it apart from any other unmentioned value.
func (m *Model) ValueLit(sym, v string) (Lit, bool) {
	d, ok := m.domains[sym]
	if !ok {
		return 0, false
	}
	if l, ok := d.values[canonical(d.typ, v)]; ok {
		return l, true
	}
	return d.other, true
}

// pinEnv fixes option env symbols to the environment the tree was imported
// with, as kbuild does.
func (m *Model) pinEnv() {
	for _, name := range m.Tree.Order {
		s := m.Tree.Symbols[name]
		if s.EnvVar == "" {
			continue
		}
		v, ok := m.Tree.Env[s.EnvVar]
		if !ok {
			m.Skipped[name] = "option env=" + s.EnvVar + " with no value in the import environment"
			continue
		}
		if l, ok := m.ValueLit(name, v); ok {
			m.F.Add(fmt.Sprintf("%s:%d env %s=%q", s.File, s.Line, s.EnvVar, v), l)
		}
	}
}

// deriveHidden encodes the value of a string symbol without a prompt. No user
// value can reach it, so kbuild's value is exactly the first default whose
// condition holds, or the empty string: a function of other symbols, and
// encodable as one. It is restricted to single declarations whose defaults
// are all literals; a default copying another symbol, or defaults split
// across declarations, stays free.
func (m *Model) deriveHidden() {
	f := m.F
	for _, name := range m.Tree.Order {
		s := m.Tree.Symbols[name]
		if s.Type != kconfig.String && s.Type != kconfig.Int && s.Type != kconfig.Hex {
			continue
		}
		if s.Prompt != "" || s.EnvVar != "" || s.Declarations() != 1 || len(s.Defaults) == 0 {
			continue
		}
		vals := make([]string, len(s.Defaults))
		ok := true
		for i, d := range s.Defaults {
			if vals[i], ok = quotedLiteral(d.Value); !ok {
				break
			}
		}
		if !ok {
			continue
		}
		origin := fmt.Sprintf("%s:%d defaults", s.File, s.Line)
		notYet := f.Var("!true")
		f.Add("constant", notYet)
		for i, d := range s.Defaults {
			cond := m.lowerExpr(kconfig.And(s.Depends, d.Cond), origin)
			fire := f.Fresh("default")
			// fire <-> notYet && cond
			f.Add(origin, fire.Neg(), notYet)
			f.Add(origin, fire.Neg(), cond)
			f.Add(origin, fire, notYet.Neg(), cond.Neg())
			l, _ := m.ValueLit(name, vals[i])
			f.Implies(origin, fire, l)
			next := f.Fresh("default")
			// next <-> notYet && !cond
			f.Add(origin, next.Neg(), notYet)
			f.Add(origin, next.Neg(), cond.Neg())
			f.Add(origin, next, notYet.Neg(), cond)
			notYet = next
		}
		empty, _ := m.ValueLit(name, "")
		f.Implies(origin, notYet, empty)
		m.Derived++
	}
}

// compare lowers sym = rhs, where rhs is a literal or names another symbol.
// It reports false when the comparison cannot be encoded exactly.
func (m *Model) compare(e *kconfig.Expr) (Lit, bool) {
	f := m.F
	lhs, lhsDef := m.Tree.Symbols[e.Sym]
	rhsSym, rhsDef := m.Tree.Symbols[e.Lit]
	if e.IsLit {
		rhsDef = false
	}
	constant := func(eq bool) Lit {
		t := f.Var("!true")
		f.Add("constant", t)
		if eq {
			return t
		}
		return t.Neg()
	}
	switch {
	case lhsDef && lhs.Type.Solvable() && !rhsDef:
		// X = y / X = n against a bool.
		switch e.Lit {
		case "y":
			return varFor(f, e.Sym), true
		case "n":
			return varFor(f, e.Sym).Neg(), true
		}
		return constant(false), true // a bool's value is never any other string
	case !lhsDef && rhsDef && rhsSym.Type.Solvable():
		switch e.Sym {
		case "y":
			return varFor(f, e.Lit), true
		case "n":
			return varFor(f, e.Lit).Neg(), true
		}
		return constant(false), true
	case lhsDef && !rhsDef:
		return m.ValueLit(e.Sym, e.Lit)
	case !lhsDef && !rhsDef:
		// Undefined symbols and constants compare by name.
		return constant(e.Sym == e.Lit), true
	case lhsDef && rhsDef && lhs.Type.Solvable() && rhsSym.Type.Solvable():
		a, b := varFor(f, e.Sym), varFor(f, e.Lit)
		aux := f.Fresh("eq")
		f.Add("bool =", aux.Neg(), a.Neg(), b)
		f.Add("bool =", aux.Neg(), a, b.Neg())
		f.Add("bool =", aux, a, b)
		f.Add("bool =", aux, a.Neg(), b.Neg())
		return aux, true
	}
	// Two non-bool symbols: "other" on both sides might or might not be the
	// same string, so equality is not determined by the domains.
	return 0, false
}
