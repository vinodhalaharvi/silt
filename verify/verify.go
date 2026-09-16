// Package verify checks a composition against the Kconfig tree it targets.
//
// Until this existed, Silt checked fragments against each other and against a
// grammar, but never against the tree they make claims about. Every build
// failure in this project's first working week came from that gap: an invented
// kernel version, a patch directory for another architecture, a header series
// symbol absent from the target release, a defconfig name that cannot be
// spelled. None was a constraint violation. All were claims about a specific
// Buildroot release that nothing verified until make did.
package verify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

// Finding is one problem, with the position that caused it.
type Finding struct {
	Pos     string // file:line from the fragment
	Symbol  string
	Message string
	Detail  string // second line, usually the conflicting position
}

func (f Finding) String() string {
	s := fmt.Sprintf("%s: %s", f.Pos, f.Message)
	if f.Detail != "" {
		s += "\n  " + f.Detail
	}
	return s
}

// Report is everything found, plus what the check ran against.
type Report struct {
	Tree     string // the tree's own version string
	Symbols  int
	Findings []Finding
	Checked  int
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// OK reports whether the composition is consistent with the tree.
func (r *Report) OK() bool { return len(r.Findings) == 0 }

// Check verifies a composed image against an imported tree.
//
// Only the Buildroot scope is checked: the Linux tree is a separate import and
// is not loaded here, so CONFIG_ symbols are left alone rather than reported as
// unknown. Saying nothing is correct; saying "unknown symbol" about a symbol
// from a tree that was never loaded would be a lie.
func Check(res *compose.Result, tree *kconfig.Tree) *Report {
	rep := &Report{Symbols: len(tree.Symbols)}

	// Index what the composition asserts, so dependency evaluation can consult it.
	stated := map[string]lang.Constraint{}
	for _, c := range res.Constraints[lang.Buildroot] {
		if !c.Soft {
			stated[c.Symbol] = c
		}
	}
	for _, c := range res.Opaque {
		stated[c.Symbol] = c
	}
	for _, c := range res.Environment {
		stated[c.Symbol] = c
	}

	// Deterministic order, so output is diffable.
	names := make([]string, 0, len(stated))
	for n := range stated {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		c := stated[name]
		if isUnmanaged(name, res.Unmanaged) {
			continue
		}
		rep.Checked++

		sym, ok := tree.Symbols[name]
		if !ok {
			rep.add(Finding{
				Pos: c.Pos.Short(), Symbol: name,
				Message: fmt.Sprintf("%s does not exist in this Buildroot tree", name),
				Detail:  "stated by " + c.From,
			})
			continue
		}

		// Type agreement. Setting a string symbol to y, or a bool to a string,
		// is silently dropped by kbuild rather than rejected.
		switch {
		case c.IsValue && sym.Type.Solvable():
			rep.add(Finding{
				Pos: c.Pos.Short(), Symbol: name,
				Message: fmt.Sprintf("%s is %s, but is given a string value", name, sym.Type),
				Detail:  fmt.Sprintf("declared at %s:%d", sym.File, sym.Line),
			})
		case !c.IsValue && !sym.Type.Solvable():
			rep.add(Finding{
				Pos: c.Pos.Short(), Symbol: name,
				Message: fmt.Sprintf("%s is %s, but is set as a boolean", name, sym.Type),
				Detail:  fmt.Sprintf("declared at %s:%d", sym.File, sym.Line),
			})
		}

		// Dependencies. Only report when the composition definitely
		// contradicts a dependency: an unstated symbol is unknown, not false,
		// and kbuild may well satisfy it from defaults.
		if c.IsValue || c.Want == lang.N || sym.Depends == nil {
			continue
		}
		if why := refutes(sym.Depends, stated); why != nil {
			// Name the refuted term, not the whole conjunction: a
			// twelve-clause depends expression buries the one thing that
			// is wrong.
			rep.add(Finding{
				Pos: c.Pos.Short(), Symbol: name,
				Message: fmt.Sprintf("%s requires %s (%s:%d)",
					name, requirement(sym.Depends, why.Symbol), sym.File, sym.Line),
				Detail: fmt.Sprintf("but %s is stated %s by %s at %s",
					why.Symbol, describe(*why), why.From, why.Pos.Short()),
			})
		}
	}
	return rep
}

func describe(c lang.Constraint) string {
	if c.IsValue {
		return `"` + c.Value + `"`
	}
	return c.Want.String()
}

func isUnmanaged(name string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
				return true
			}
		} else if name == p {
			return true
		}
	}
	return false
}

// refutes returns the stated constraint that makes e definitely false, or nil.
//
// Three-valued and conservative in the same direction as the rules engine: a
// symbol nobody stated is unknown, so only an explicit contradiction is
// reported. A false positive here would be worse than a miss — it would block
// a configuration kbuild accepts.
func refutes(e *kconfig.Expr, stated map[string]lang.Constraint) *lang.Constraint {
	if e == nil {
		return nil
	}
	switch e.Op {
	case kconfig.ExprSym:
		if e.Sym == "n" {
			return nil // a literal n refutes, but names no culprit
		}
		if c, ok := stated[e.Sym]; ok && !c.IsValue && c.Want == lang.N {
			cc := c
			return &cc
		}
		return nil

	case kconfig.ExprNot:
		// !X is refuted when X is stated y.
		sub := e.Args[0]
		if sub.Op == kconfig.ExprSym {
			if c, ok := stated[sub.Sym]; ok && !c.IsValue && c.Want != lang.N {
				cc := c
				return &cc
			}
		}
		return nil

	case kconfig.ExprAnd:
		// A conjunction is refuted if either side is.
		for _, a := range e.Args {
			if c := refutes(a, stated); c != nil {
				return c
			}
		}
		return nil

	case kconfig.ExprOr:
		// A disjunction is refuted only if BOTH sides are, and then the first
		// culprit is the one worth naming.
		var first *lang.Constraint
		for _, a := range e.Args {
			c := refutes(a, stated)
			if c == nil {
				return nil
			}
			if first == nil {
				first = c
			}
		}
		return first
	}
	return nil
}

// requirement renders just the term of e that mentions sym, so a long
// conjunction does not bury the one clause that failed.
func requirement(e *kconfig.Expr, sym string) string {
	if e == nil {
		return sym
	}
	switch e.Op {
	case kconfig.ExprAnd, kconfig.ExprOr:
		for _, a := range e.Args {
			if mentions(a, sym) {
				return requirement(a, sym)
			}
		}
	}
	return e.String()
}

func mentions(e *kconfig.Expr, sym string) bool {
	if e == nil {
		return false
	}
	if e.Sym == sym {
		return true
	}
	for _, a := range e.Args {
		if mentions(a, sym) {
			return true
		}
	}
	return false
}
