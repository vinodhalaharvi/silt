package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/lang"
)

// Derived records a constraint that a rule produced rather than a fragment
// stating it, together with the rule responsible.
//
// This is what makes a derived symbol explicable. Without it a config gains
// CONFIG_PACKET=y for no visible reason, which is the same opacity Silt exists
// to remove.
type Derived struct {
	Constraint lang.Constraint
	Rule       lang.Guarded
	Round      int // which forward-chaining pass produced it
}

// RuleConflict is a rule whose consequent contradicts something already
// stated. It is an error rather than a silent override: a rule that quietly
// flips a symbol a fragment deliberately set is exactly merge_config.sh's
// behaviour wearing different syntax.
type RuleConflict struct {
	Derived Derived
	Stated  lang.Constraint
}

func (c RuleConflict) Error() string {
	return fmt.Sprintf(
		"rule at %s requires %s = %s, but %s is stated %s at %s\n"+
			"  rule fired because its condition held: %s",
		c.Derived.Rule.Pos.Short(),
		c.Derived.Constraint.Sym, describe(c.Derived.Constraint),
		c.Stated.Sym, describe(c.Stated), c.Stated.Pos.Short(),
		c.Derived.Rule.From)
}

// maxRounds bounds forward chaining. Rules may legitimately cascade — one
// firing can satisfy another's condition — but a cycle must not hang the tool.
const maxRounds = 32

// applyRules forward-chains every guard to a fixed point.
//
// No solver is involved and none is needed. A rule is an implication; if its
// antecedent is known true from what has been stated, its consequent is added.
// That is enough to enforce the nineteen cross-tree requirements Buildroot
// currently documents in help text and checks nowhere.
//
// Conditions are evaluated three-valued and conservatively: a rule fires only
// when its condition is *known* true from stated or already-derived
// constraints. An unstated symbol makes a condition unknown, never false, so
// (not (y X)) does not fire merely because X was not mentioned. Deciding those
// cases needs the completed model, which is Rung 7.
func (r *Result) applyRules() error {
	// Keyed by SymbolID, not name. Keyed by name, linux:CONFIG_NET and
	// busybox:CONFIG_NET were one entry, and a rule about one fired on the
	// other.
	index := map[lang.SymbolID]lang.Constraint{}
	for _, cs := range r.Constraints {
		for _, c := range cs {
			if !c.Soft {
				index[c.Sym] = c
			}
		}
	}
	for _, c := range r.Opaque {
		index[c.Sym] = c
	}
	for _, c := range r.Environment {
		index[c.Sym] = c
	}

	// Add what Kconfig's select machinery will turn on. These are real facts
	// about the built image, so a rule may fire on them — but they are not
	// emitted, because kbuild derives them itself and restating a select is
	// exactly what fragments/README.md forbids. The loaded tree is Buildroot's,
	// so only Buildroot symbols take part.
	if r.tree != nil {
		on := map[string]bool{}
		for id, c := range index {
			if !c.IsValue && id.Tree == string(lang.Buildroot) {
				on[id.Name] = c.Want != lang.N
			}
		}
		r.Selected = selectClosure(r.tree, on)
		for sym, by := range r.Selected {
			id := lang.SymbolID{Tree: string(lang.Buildroot), Name: sym}
			if _, ok := index[id]; ok {
				continue
			}
			index[id] = lang.Constraint{
				Sym: id, Want: lang.Y,
				From: "selected by " + by,
			}
		}
	}

	for round := 1; round <= maxRounds; round++ {
		changed := false
		for _, g := range r.Guards {
			if holds(g.Cond, index) != known {
				continue
			}
			for _, want := range g.Then {
				have, ok := index[want.Sym]
				if ok {
					if conflicts(have, want) {
						d := Derived{Constraint: want, Rule: g, Round: round}
						return RuleConflict{Derived: d, Stated: have}
					}
					if !stronger(want, have) {
						continue
					}
				}
				d := want
				d.From = "rule " + g.From
				d.Pos = g.Pos
				index[d.Sym] = d
				r.Derived = append(r.Derived, Derived{Constraint: d, Rule: g, Round: round})
				changed = true
			}
		}
		if !changed {
			break
		}
		if round == maxRounds {
			return fmt.Errorf("rules did not reach a fixed point in %d rounds; "+
				"check fragments/rules for a cycle", maxRounds)
		}
	}

	// Fold the derived constraints back into the emitted set. A symbol that
	// only exists in the index because select will enable it is not folded in:
	// kbuild produces it, and emitting it would restate a select.
	for _, d := range r.Derived {
		if _, viaSelect := r.Selected[d.Constraint.Sym.Name]; viaSelect &&
			d.Constraint.Sym.Tree == string(lang.Buildroot) {
			continue
		}
		sc := lang.Scope(d.Constraint.Sym.Tree)
		replaced := false
		for i, c := range r.Constraints[sc] {
			if c.Sym == d.Constraint.Sym && !c.Soft {
				r.Constraints[sc][i] = d.Constraint
				replaced = true
				break
			}
		}
		if !replaced {
			r.Constraints[sc] = append(r.Constraints[sc], d.Constraint)
		}
	}
	for sc := range r.Constraints {
		cs := r.Constraints[sc]
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].Sym.Name < cs[j].Sym.Name })
		r.Constraints[sc] = cs
	}
	sort.SliceStable(r.Derived, func(i, j int) bool {
		return r.Derived[i].Constraint.Sym.String() < r.Derived[j].Constraint.Sym.String()
	})
	return nil
}

// truth is three-valued: a condition over a partial assignment is often
// neither true nor false.
type truth uint8

const (
	unknown truth = iota
	known         // condition is satisfied by what is stated
	refuted       // condition is contradicted by what is stated
)

func holds(c *lang.Cond, index map[lang.SymbolID]lang.Constraint) truth {
	if c == nil {
		return unknown
	}
	switch c.Op {
	case "constraint":
		have, ok := index[c.C.Sym]
		if !ok {
			return unknown
		}
		if conflicts(have, *c.C) {
			return refuted
		}
		return known

	case "equal?":
		// Known only from a stated value. An unstated string is unknown,
		// never unequal: kbuild may give it exactly this value by default.
		have, ok := index[c.Sym]
		if !ok || !have.IsValue {
			return unknown
		}
		if have.Value == c.Value {
			return known
		}
		return refuted

	case "set?":
		have, ok := index[c.Sym]
		if !ok {
			return unknown
		}
		if !have.IsValue {
			return unknown
		}
		if have.Value != "" {
			return known
		}
		return refuted

	case "not":
		switch holds(c.Args[0], index) {
		case known:
			return refuted
		case refuted:
			return known
		}
		return unknown

	case "and":
		res := known
		for _, a := range c.Args {
			switch holds(a, index) {
			case refuted:
				return refuted
			case unknown:
				res = unknown
			}
		}
		return res

	case "or":
		res := refuted
		for _, a := range c.Args {
			switch holds(a, index) {
			case known:
				return known
			case unknown:
				res = unknown
			}
		}
		return res
	}
	return unknown
}

// ExplainDerived renders why a symbol ended up derived, for `silt why`.
func (r *Result) ExplainDerived(sym lang.SymbolID) (string, bool) {
	for _, d := range r.Derived {
		if d.Constraint.Sym != sym {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s = %s\n", sym, describe(d.Constraint))
		fmt.Fprintf(&b, "  derived by %s at %s\n", d.Rule.From, d.Rule.Pos.Short())
		fmt.Fprintf(&b, "  condition held: %s\n", condString(d.Rule.Cond))
		return b.String(), true
	}
	return "", false
}

func condString(c *lang.Cond) string {
	if c == nil {
		return "?"
	}
	switch c.Op {
	case "constraint":
		return "(" + describe(*c.C) + " " + c.C.Sym.String() + ")"
	case "set?":
		return "(set? " + c.Sym.String() + ")"
	case "equal?":
		return "(equal? " + c.Sym.String() + " " + fmt.Sprintf("%q", c.Value) + ")"
	case "not":
		return "(not " + condString(c.Args[0]) + ")"
	default:
		parts := make([]string, len(c.Args))
		for i, a := range c.Args {
			parts[i] = condString(a)
		}
		return "(" + c.Op + " " + strings.Join(parts, " ") + ")"
	}
}
