// Package compose merges fragments into a single constraint set.
//
// The thing this must beat is merge_config.sh, which is textual and
// last-one-wins: a later fragment silently overrides an earlier one and you
// discover what you actually got by reading the result. Here a genuine
// conflict is an error naming both sources.
package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

// Library is a set of fragments and rule blocks, indexed by id.
type Library struct {
	Fragments map[string]*lang.Fragment
	Rules     []*lang.Rules
	// Declared is the capability vocabulary. Empty means undeclared, in which
	// case provides and requires are matched by string alone as before — a
	// library that has not adopted the declaration file still works.
	Declared map[string]lang.CapabilityDecl
	// Tree, when set, lets rule conditions see symbols that Kconfig's own
	// select machinery will enable. Optional: without it, rules fire only on
	// what fragments state, which is correct but misses select-implied
	// antecedents.
	Tree *kconfig.Tree
}

func NewLibrary() *Library {
	return &Library{
		Fragments: map[string]*lang.Fragment{},
		Declared:  map[string]lang.CapabilityDecl{},
	}
}

// Add indexes everything in a parsed file.
func (l *Library) Add(f *lang.File) error {
	for _, fr := range f.Fragments {
		if prev, ok := l.Fragments[fr.ID.String()]; ok {
			return fmt.Errorf("%s: %s redefined (first at %s)",
				fr.Pos.Short(), fr.ID, prev.Pos.Short())
		}
		l.Fragments[fr.ID.String()] = fr
	}
	for _, cs := range f.Capabilities {
		for _, d := range cs.Decls {
			if prev, ok := l.Declared[d.Name]; ok {
				return fmt.Errorf("%s: capability %q redeclared (first at %s)",
					d.Pos.Short(), d.Name, prev.Pos.Short())
			}
			l.Declared[d.Name] = d
		}
	}
	l.Rules = append(l.Rules, f.Rules...)
	return nil
}

// checkDeclared reports a capability name that is not in the vocabulary.
//
// This is the whole point of declaring them: provides and requires used to be
// matched by string alone, so the same typo on both sides composed cleanly and
// meant nothing.
func (l *Library) checkDeclared(c lang.Capability, from lang.ID, verb string) error {
	if len(l.Declared) == 0 {
		return nil // no vocabulary declared; keep the old behaviour
	}
	if _, ok := l.Declared[c.Name]; ok {
		return nil
	}
	known := make([]string, 0, len(l.Declared))
	for n := range l.Declared {
		known = append(known, n)
	}
	sort.Strings(known)
	return fmt.Errorf("%s: %s %s capability %q, which is not declared\n  declared: %s",
		c.Pos.Short(), from, verb, c.Name, strings.Join(known, ", "))
}

// Conflict is two fragments wanting different things from one symbol.
type Conflict struct {
	Symbol string
	A, B   lang.Constraint
}

func (c Conflict) Error() string {
	return fmt.Sprintf("conflicting values for %s\n\n  %-4s %-22s %s\n  %-4s %-22s %s",
		c.Symbol,
		describe(c.A), c.A.From, c.A.Pos.Short(),
		describe(c.B), c.B.From, c.B.Pos.Short())
}

func describe(c lang.Constraint) string {
	switch {
	case c.IsValue:
		return `"` + c.Value + `"`
	case c.AtLeast:
		return "≥" + c.Want.String()
	default:
		return c.Want.String()
	}
}

// Result is a composed image: the merged constraint set plus the accounting
// that Invariant 9 requires be visible on every run.
type Result struct {
	Image        *lang.Image
	Fragments    []*lang.Fragment
	Constraints  map[lang.Scope][]lang.Constraint
	Guards       []lang.Guarded
	Opaque       []lang.Constraint
	Environment  []lang.Constraint
	Unmanaged    []string
	Overridden   []lang.Constraint
	Derived      []Derived
	Capabilities map[string]string // capability -> providing fragment
	// Selected maps a symbol Kconfig will enable via select to the symbol that
	// selects it. Populated only when the library carries a tree.
	Selected map[string]string
	tree     *kconfig.Tree
}

// Tree is the Kconfig tree the library was composed against, or nil.
func (r *Result) Tree() *kconfig.Tree { return r.tree }

// Compose merges an image's fragments, applies overrides, and checks
// capabilities.
func (l *Library) Compose(im *lang.Image) (*Result, error) {
	r := &Result{
		Image:        im,
		Constraints:  map[lang.Scope][]lang.Constraint{},
		Opaque:       im.Opaque,
		Environment:  im.Environment,
		Unmanaged:    im.Unmanaged,
		Capabilities: map[string]string{},
	}

	// Resolve fragments first so a missing one is reported before anything else.
	for _, id := range im.Compose {
		fr, ok := l.Fragments[id.String()]
		if !ok {
			return nil, fmt.Errorf("%s: no such fragment %s", im.Pos.Short(), id)
		}
		r.Fragments = append(r.Fragments, fr)
		for _, c := range fr.Provides {
			if err := l.checkDeclared(c, fr.ID, "provides"); err != nil {
				return nil, err
			}
			r.Capabilities[c.Name] = fr.ID.String()
		}
	}

	// Capabilities: a profile or feature that needs something no target
	// provides fails here, in milliseconds, rather than partway into a build.
	for _, fr := range r.Fragments {
		for _, want := range fr.Requires {
			if err := l.checkDeclared(want, fr.ID, "requires"); err != nil {
				return nil, err
			}
			if _, ok := r.Capabilities[want.Name]; !ok {
				return nil, fmt.Errorf("%s: capability %q required by %s is not provided by %s\n  %s provides: %s",
					want.Pos.Short(), want.Name, fr.ID, targetOf(r.Fragments),
					targetOf(r.Fragments), strings.Join(sortedKeys(r.Capabilities), ", "))
			}
		}
	}

	// Merge, detecting conflicts.
	seen := map[string]lang.Constraint{} // symbol -> winning hard constraint
	add := func(sc lang.Scope, c lang.Constraint) error {
		key := string(sc) + "/" + c.Symbol
		if prev, ok := seen[key]; ok {
			if conflicts(prev, c) {
				return Conflict{Symbol: c.Symbol, A: prev, B: c}
			}
			if stronger(c, prev) {
				seen[key] = c
			}
			return nil
		}
		seen[key] = c
		return nil
	}

	for _, fr := range r.Fragments {
		for sc, cs := range fr.Constraints {
			for _, c := range cs {
				if c.Soft {
					r.Constraints[sc] = append(r.Constraints[sc], c)
					continue
				}
				if err := add(sc, c); err != nil {
					return nil, err
				}
			}
		}
		r.Guards = append(r.Guards, fr.Guards...)
	}

	// The image's own constraints participate in conflict detection too.
	for sc, cs := range im.Constraints {
		for _, c := range cs {
			if c.Soft {
				r.Constraints[sc] = append(r.Constraints[sc], c)
				continue
			}
			if err := add(sc, c); err != nil {
				return nil, err
			}
		}
	}

	// Overrides are the one place last-wins is permitted, and only because it
	// was asked for explicitly. The displaced constraint is recorded so
	// `silt why` can attribute the value to the override rather than to the
	// fragment it silenced.
	for _, o := range im.Override {
		sc := scopeFor(o.Symbol)
		key := string(sc) + "/" + o.Symbol
		if prev, ok := seen[key]; ok && conflicts(prev, o) {
			r.Overridden = append(r.Overridden, prev)
		}
		seen[key] = o
	}

	for key, c := range seen {
		sc := lang.Scope(strings.SplitN(key, "/", 2)[0])
		r.Constraints[sc] = append(r.Constraints[sc], c)
	}
	for sc := range r.Constraints {
		cs := r.Constraints[sc]
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].Symbol < cs[j].Symbol })
		r.Constraints[sc] = cs
	}

	// Rule blocks apply to every image in the library.
	for _, rs := range l.Rules {
		r.Guards = append(r.Guards, rs.Guards...)
	}

	// Forward-chain the guards. This is what enforces the cross-tree
	// requirements that Buildroot documents in help text and checks nowhere.
	r.tree = l.Tree
	if err := r.applyRules(); err != nil {
		return nil, err
	}
	return r, nil
}

// conflicts reports whether two hard constraints on one symbol are
// irreconcilable.
//
// (y X) and (at-least m X) do not conflict: y satisfies "m or stronger". Two
// different string values do conflict, since only one can be emitted.
func conflicts(a, b lang.Constraint) bool {
	if a.IsValue != b.IsValue {
		return true
	}
	if a.IsValue {
		return a.Value != b.Value
	}
	switch {
	case a.AtLeast && b.AtLeast:
		return false
	case a.AtLeast:
		return b.Want < a.Want
	case b.AtLeast:
		return a.Want < b.Want
	default:
		return a.Want != b.Want
	}
}

// stronger reports whether c is a tighter requirement than prev.
func stronger(c, prev lang.Constraint) bool {
	if c.IsValue || prev.IsValue {
		return false
	}
	if prev.AtLeast && !c.AtLeast {
		return true
	}
	return false
}

func scopeFor(sym string) lang.Scope {
	if strings.HasPrefix(sym, "CONFIG_") {
		return lang.Linux
	}
	return lang.Buildroot
}

func targetOf(frs []*lang.Fragment) string {
	for _, f := range frs {
		if f.ID.Kind == lang.Target {
			return f.ID.String()
		}
	}
	return "(no target)"
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
