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
	Scope    string // the tree this report is about
	Symbols  int
	Findings []Finding
	Checked  int
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

// OK reports whether the composition is consistent with the tree.
func (r *Report) OK() bool { return len(r.Findings) == 0 }

// Check verifies one tree's half of a composed image against that tree.
//
// Only the named scope is checked. A symbol from a tree that was not loaded is
// left alone rather than reported as unknown: saying nothing is correct,
// saying "unknown symbol" about a tree nobody imported would be a lie.
func Check(res *compose.Result, tree *kconfig.Tree, decl lang.TreeDecl) *Report {
	sc := lang.Scope(decl.Name)
	rep := &Report{Symbols: len(tree.Symbols), Scope: decl.Name}

	// The prefix is serialization, not identity. A Buildroot symbol is
	// declared as `config BR2_X` and written as BR2_X; a kernel symbol is
	// declared as `config EXT4_FS` and written as CONFIG_EXT4_FS, because
	// conf adds CONFIG_ on the way out. Looking a stated name up in the tree
	// verbatim reported every CONFIG_ symbol in the library as missing,
	// including ones that plainly exist.
	inTree := treeName(tree, decl)

	// Index what the composition asserts, so dependency evaluation can consult it.
	// Only Buildroot symbols: that is the tree loaded here. A symbol from
	// another tree is not unknown, it is unchecked, and saying "does not
	// exist" about it would be a lie. Keying by bare name used to let an
	// opaque linux value be looked up in the Buildroot tree.
	stated := map[string]lang.Constraint{}
	for _, c := range res.Constraints[sc] {
		if !c.Soft {
			stated[c.Sym.Name] = c
		}
	}
	for _, cs := range [][]lang.Constraint{res.Opaque, res.Environment} {
		for _, c := range cs {
			if lang.Scope(c.Sym.Tree) == sc {
				stated[c.Sym.Name] = c
			}
		}
	}

	// Deterministic order, so output is diffable.
	names := make([]string, 0, len(stated))
	for n := range stated {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		c := stated[name]
		if isUnmanaged(name, res.Unmanaged, sc) {
			continue
		}
		rep.Checked++

		sym, ok := tree.Symbols[inTree(name)]
		if !ok {
			rep.add(Finding{
				Pos: c.Pos.Short(), Symbol: name,
				Message: fmt.Sprintf("%s does not exist in this %s tree", name, sc),
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
					name, requirement(sym.Depends, why.Sym.Name), sym.File, sym.Line),
				Detail: fmt.Sprintf("but %s is stated %s by %s at %s",
					why.Sym, describe(*why), why.From, why.Pos.Short()),
			})
		}
	}
	return rep
}

// CheckCapabilities verifies that every capability bound to a symbol names one
// the tree actually has. A binding that points at nothing is worse than no
// binding: it reads as verified and is not.
func CheckCapabilities(decls map[string]lang.CapabilityDecl, tree *kconfig.Tree, rep *Report) {
	names := make([]string, 0, len(decls))
	for n := range decls {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := decls[n]
		if d.Symbol.IsZero() || d.Symbol.Tree != string(lang.Buildroot) {
			continue // unbound, or a symbol from a tree not loaded here
		}
		if _, ok := tree.Symbols[d.Symbol.Name]; !ok {
			rep.add(Finding{
				Pos: d.Pos.Short(), Symbol: d.Symbol.String(),
				Message: fmt.Sprintf("capability %q is bound to %s, which does not exist in this tree",
					d.Name, d.Symbol),
			})
		}
	}
}

func describe(c lang.Constraint) string {
	if c.IsValue {
		return `"` + c.Value + `"`
	}
	return c.Want.String()
}

func isUnmanaged(name string, ids []lang.SymbolID, sc lang.Scope) bool {
	for _, id := range ids {
		if lang.Scope(id.Tree) != sc {
			continue
		}
		p := id.Name
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

// CheckTrees verifies that every declared tree hands its config to Buildroot
// through a symbol that exists and takes a string. A consumed-by pointing at a
// misspelled symbol would emit a line kbuild drops, and the tree's whole
// config fragment would be built and then ignored.
func CheckTrees(trees map[string]lang.TreeDecl, tree *kconfig.Tree, rep *Report) {
	names := make([]string, 0, len(trees))
	for n := range trees {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		d := trees[n]
		if d.ConsumedBy == "" {
			continue
		}
		sym, ok := tree.Symbols[d.ConsumedBy]
		switch {
		case !ok:
			rep.add(Finding{
				Pos: d.Pos.Short(), Symbol: d.ConsumedBy,
				Message: fmt.Sprintf("tree %s is consumed by %s, which does not exist in this tree", n, d.ConsumedBy),
			})
		case sym.Type != kconfig.String:
			rep.add(Finding{
				Pos: d.Pos.Short(), Symbol: d.ConsumedBy,
				Message: fmt.Sprintf("tree %s is consumed by %s, which is %s, not a string of file paths",
					n, d.ConsumedBy, sym.Type),
			})
		}
	}
}

// firstSymbol returns any declared symbol's name, to see whether this tree
// spells its symbols with the prefix it is written with. Buildroot's do;
// the kernel's do not.
func firstSymbol(tree *kconfig.Tree) string {
	for _, name := range tree.Order {
		if s := tree.Symbols[name]; s != nil && s.Type != kconfig.Unknown {
			return name
		}
	}
	return ""
}

// CheckRules verifies every symbol a rule mentions, whether or not the rule
// fires. A rule's consequent is a claim about the tree exactly like a
// fragment's, and a rule nothing has triggered yet is where a stale claim
// hides: the fscryptctl rule named CONFIG_EXT4_ENCRYPTION, copied from
// Buildroot's own help text, which the kernel renamed to CONFIG_FS_ENCRYPTION.
func CheckRules(rules []*lang.Rules, tree *kconfig.Tree, decl lang.TreeDecl, rep *Report) {
	inTree := treeName(tree, decl)
	seen := map[lang.SymbolID]bool{}
	report := func(id lang.SymbolID, pos, from string) {
		if lang.Scope(id.Tree) != lang.Scope(decl.Name) || seen[id] {
			return
		}
		seen[id] = true
		if _, ok := tree.Symbols[inTree(id.Name)]; !ok {
			rep.add(Finding{
				Pos: pos, Symbol: id.Name,
				Message: fmt.Sprintf("%s does not exist in this %s tree", id.Name, decl.Name),
				Detail:  "named by " + from,
			})
		}
	}
	var cond func(c *lang.Cond, pos, from string)
	cond = func(c *lang.Cond, pos, from string) {
		if c == nil {
			return
		}
		switch c.Op {
		case "constraint":
			report(c.C.Sym, pos, from)
		case "set?", "equal?":
			report(c.Sym, pos, from)
		}
		for _, a := range c.Args {
			cond(a, pos, from)
		}
	}
	for _, rs := range rules {
		for _, g := range rs.Guards {
			cond(g.Cond, g.Pos.Short(), "rules:"+rs.Name)
			for _, c := range g.Then {
				report(c.Sym, c.Pos.Short(), "rules:"+rs.Name)
			}
		}
	}
}

// treeName maps a symbol as written to the name the tree declares it under.
func treeName(tree *kconfig.Tree, decl lang.TreeDecl) func(string) string {
	if decl.Prefix == "" || strings.HasPrefix(firstSymbol(tree), decl.Prefix) {
		return func(name string) string { return name }
	}
	return func(name string) string { return strings.TrimPrefix(name, decl.Prefix) }
}
