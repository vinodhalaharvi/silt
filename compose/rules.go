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
		c.Derived.Constraint.Symbol, describe(c.Derived.Constraint),
		c.Stated.Symbol, describe(c.Stated), c.Stated.Pos.Short(),
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
	index := map[string]lang.Constraint{}
	key := func(sym string) string { return sym }
	for _, cs := range r.Constraints {
		for _, c := range cs {
			if !c.Soft {
				index[key(c.Symbol)] = c
			}
		}
	}
	for _, c := range r.Opaque {
		index[key(c.Symbol)] = c
	}
	for _, c := range r.Environment {
		index[key(c.Symbol)] = c
	}

	for round := 1; round <= maxRounds; round++ {
		changed := false
		for _, g := range r.Guards {
			if holds(g.Cond, index) != known {
				continue
			}
			for _, want := range g.Then {
				have, ok := index[key(want.Symbol)]
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
				index[key(d.Symbol)] = d
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

	// Fold the derived constraints back into the emitted set.
	for _, d := range r.Derived {
		sc := scopeFor(d.Constraint.Symbol)
		replaced := false
		for i, c := range r.Constraints[sc] {
			if c.Symbol == d.Constraint.Symbol && !c.Soft {
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
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].Symbol < cs[j].Symbol })
		r.Constraints[sc] = cs
	}
	sort.SliceStable(r.Derived, func(i, j int) bool {
		return r.Derived[i].Constraint.Symbol < r.Derived[j].Constraint.Symbol
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

func holds(c *lang.Cond, index map[string]lang.Constraint) truth {
	if c == nil {
		return unknown
	}
	switch c.Op {
	case "constraint":
		have, ok := index[c.C.Symbol]
		if !ok {
			return unknown
		}
		if conflicts(have, *c.C) {
			return refuted
		}
		return known

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
func (r *Result) ExplainDerived(sym string) (string, bool) {
	for _, d := range r.Derived {
		if d.Constraint.Symbol != sym {
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
		return "(" + describe(*c.C) + " " + c.C.Symbol + ")"
	case "set?":
		return "(set? " + c.Sym + ")"
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
