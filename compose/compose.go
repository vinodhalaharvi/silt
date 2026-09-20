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
	// Images are indexed so that one can derive from another.
	Images map[string]*lang.Image
	Rules  []*lang.Rules
	// Declared is the capability vocabulary. Empty means undeclared, in which
	// case provides and requires are matched by string alone as before — a
	// library that has not adopted the declaration file still works.
	Declared map[string]lang.CapabilityDecl
	// Tree, when set, lets rule conditions see symbols that Kconfig's own
	// select machinery will enable. Optional: without it, rules fire only on
	// what fragments state, which is correct but misses select-implied
	// antecedents.
	Tree *kconfig.Tree
	// Trees is the tree registry: buildroot and linux built in, anything else
	// declared with (tree ...). A symbol for an undeclared tree is an error.
	Trees map[string]lang.TreeDecl
}

func NewLibrary() *Library {
	return &Library{
		Fragments: map[string]*lang.Fragment{},
		Images:    map[string]*lang.Image{},
		Declared:  map[string]lang.CapabilityDecl{},
		Trees:     lang.BuiltinTrees(),
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
	for _, im := range f.Images {
		if prev, ok := l.Images[im.Name]; ok {
			return fmt.Errorf("%s: image %s redefined (first at %s)",
				im.Pos.Short(), im.Name, prev.Pos.Short())
		}
		l.Images[im.Name] = im
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
	for _, d := range f.Trees {
		if prev, ok := l.Trees[d.Name]; ok {
			where := "built in"
			if prev.Pos.File != "" {
				where = "first at " + prev.Pos.Short()
			}
			return fmt.Errorf("%s: tree %s redeclared (%s)", d.Pos.Short(), d.Name, where)
		}
		l.Trees[d.Name] = *d
	}
	l.Rules = append(l.Rules, f.Rules...)
	return nil
}

// checkSymbol reports a symbol whose tree is not registered or whose name is
// spelled against that tree's convention.
func (l *Library) checkSymbol(id lang.SymbolID, pos string) error {
	decl, ok := l.Trees[id.Tree]
	if !ok {
		return fmt.Errorf("%s: %s names tree %q, which is not declared; add (tree %s (kind kconfig) ...)",
			pos, id, id.Tree, id.Tree)
	}
	// An unmanaged pattern such as BR2_TARGET_UBOOT_* is checked on the part
	// before the star, which may itself be shorter than the prefix.
	name := strings.TrimSuffix(id.Name, "*")
	if name != id.Name && strings.HasPrefix(decl.Prefix, name) {
		return nil
	}
	if err := lang.CheckPrefix(lang.SymbolID{Tree: id.Tree, Name: name}, decl); err != nil {
		return fmt.Errorf("%s: %v", pos, err)
	}
	return nil
}

func (l *Library) checkConstraints(cs []lang.Constraint) error {
	for _, c := range cs {
		if err := l.checkSymbol(c.Sym, c.Pos.Short()); err != nil {
			return err
		}
	}
	return nil
}

func (l *Library) checkCond(c *lang.Cond) error {
	if c == nil {
		return nil
	}
	switch c.Op {
	case "constraint":
		return l.checkSymbol(c.C.Sym, c.Pos.Short())
	case "set?", "equal?":
		return l.checkSymbol(c.Sym, c.Pos.Short())
	}
	for _, a := range c.Args {
		if err := l.checkCond(a); err != nil {
			return err
		}
	}
	return nil
}

func (l *Library) checkGuards(gs []lang.Guarded) error {
	for _, g := range gs {
		if err := l.checkCond(g.Cond); err != nil {
			return err
		}
		if err := l.checkConstraints(g.Then); err != nil {
			return err
		}
	}
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
	Unmanaged    []lang.SymbolID
	Overridden   []lang.Constraint
	Derived      []Derived
	Capabilities map[string]string // capability -> providing fragment
	// Selected maps a symbol Kconfig will enable via select to the symbol that
	// selects it. Populated only when the library carries a tree.
	Selected map[string]string
	// Forbidden records the absence claims this composition makes, so the
	// predicted configuration can be checked against them.
	Forbidden map[string]lang.Capability
	forbidder map[string]lang.ID
	// Trees is the registry the image was composed against.
	Trees map[string]lang.TreeDecl
	tree  *kconfig.Tree
}

// Tree is the Kconfig tree the library was composed against, or nil.
func (r *Result) Tree() *kconfig.Tree { return r.tree }

// Compose merges an image's fragments, applies overrides, and checks
// capabilities.
func (l *Library) Compose(im *lang.Image) (*Result, error) {
	// Resolve any base image first: a derived image supplies its target and
	// profile from the base, so nothing below can be checked — and nothing
	// above can be recorded — until the chain is flat.
	im, err := l.flatten(im, nil)
	if err != nil {
		return nil, err
	}

	r := &Result{
		Image:        im,
		Constraints:  map[lang.Scope][]lang.Constraint{},
		Opaque:       im.Opaque,
		Environment:  im.Environment,
		Unmanaged:    im.Unmanaged,
		Capabilities: map[string]string{},
		Trees:        l.Trees,
	}

	// Resolve fragments first so a missing one is reported before anything else.
	for _, id := range im.Compose {
		fr, ok := l.Fragments[id.String()]
		if !ok {
			return nil, fmt.Errorf("%s: no such fragment %s", im.Pos.Short(), id)
		}
		r.Fragments = append(r.Fragments, fr)
		for sc, cs := range fr.Constraints {
			if _, ok := l.Trees[string(sc)]; !ok {
				return nil, fmt.Errorf("%s: %s has a %s scope, but no tree %s is declared",
					fr.Pos.Short(), fr.ID, sc, sc)
			}
			if err := l.checkConstraints(cs); err != nil {
				return nil, err
			}
		}
		if err := l.checkGuards(fr.Guards); err != nil {
			return nil, err
		}
		for _, c := range fr.Provides {
			if err := l.checkDeclared(c, fr.ID, "provides"); err != nil {
				return nil, err
			}
			r.Capabilities[c.Name] = fr.ID.String()
		}
	}

	for sc, cs := range im.Constraints {
		if _, ok := l.Trees[string(sc)]; !ok {
			return nil, fmt.Errorf("%s: image has a %s scope, but no tree %s is declared", im.Pos.Short(), sc, sc)
		}
		if err := l.checkConstraints(cs); err != nil {
			return nil, err
		}
	}
	for _, cs := range [][]lang.Constraint{im.Override, im.Opaque, im.Environment} {
		if err := l.checkConstraints(cs); err != nil {
			return nil, err
		}
	}
	for _, u := range im.Unmanaged {
		if err := l.checkSymbol(u, im.Pos.Short()); err != nil {
			return nil, err
		}
	}
	for _, rs := range l.Rules {
		if err := l.checkGuards(rs.Guards); err != nil {
			return nil, err
		}
	}

	// Capabilities: a profile or feature that needs something no target
	// provides fails here, in milliseconds, rather than partway into a build.
	// Forbidden capabilities are checked before required ones, because a
	// composition that violates an absence claim is wrong whatever else it
	// satisfies.
	forbidden := map[string]lang.Capability{}
	forbidder := map[string]lang.ID{}
	for _, fr := range r.Fragments {
		for _, no := range fr.Forbids {
			if err := l.checkDeclared(no, fr.ID, "forbids"); err != nil {
				return nil, err
			}
			forbidden[no.Name] = no
			forbidder[no.Name] = fr.ID
		}
	}
	for _, fr := range r.Fragments {
		for _, yes := range fr.Provides {
			if no, bad := forbidden[yes.Name]; bad {
				return nil, fmt.Errorf(
					"%s: %s provides capability %q, which %s forbids\n  forbidden at %s",
					yes.Pos.Short(), fr.ID, yes.Name, forbidder[yes.Name], no.Pos.Short())
			}
		}
	}
	r.Forbidden = forbidden
	r.forbidder = forbidder

	for _, fr := range r.Fragments {
		for _, want := range fr.Requires {
			if err := l.checkDeclared(want, fr.ID, "requires"); err != nil {
				return nil, err
			}
			// Requiring what the composition forbids is a contradiction worth
			// naming as one, rather than letting it surface as a missing
			// provider.
			if no, bad := forbidden[want.Name]; bad {
				return nil, fmt.Errorf(
					"%s: %s requires capability %q, which %s forbids\n  forbidden at %s",
					want.Pos.Short(), fr.ID, want.Name, forbidder[want.Name], no.Pos.Short())
			}
			// Named by what could have provided it, not just the target:
			// profiles provide capabilities too, and no-console-login is one
			// only a profile can. Blaming the target sent the reader to the
			// one fragment that could never have fixed it.
			if _, ok := r.Capabilities[want.Name]; !ok {
				return nil, fmt.Errorf("%s: capability %q required by %s is not provided by anything this image composes (%s)\n  provided: %s",
					want.Pos.Short(), want.Name, fr.ID, providersOf(r.Fragments),
					strings.Join(sortedKeys(r.Capabilities), ", "))
			}
		}
	}

	// Merge, detecting conflicts.
	seen := map[lang.SymbolID]lang.Constraint{} // symbol -> winning hard constraint
	add := func(sc lang.Scope, c lang.Constraint) error {
		key := c.Sym
		if prev, ok := seen[key]; ok {
			// A list-valued symbol takes both contributions rather than
			// choosing between them. Buildroot reads BR2_ROOTFS_OVERLAY and
			// its kin as space-separated lists, so a board carrying an
			// overlay and a feature carrying one are not in conflict: they
			// are two entries.
			if prev.List && c.List {
				seen[key] = appendList(prev, c)
				return nil
			}
			if conflicts(prev, c) {
				return Conflict{Symbol: c.Sym.String(), A: prev, B: c}
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
		key := o.Sym
		if prev, ok := seen[key]; ok && conflicts(prev, o) {
			r.Overridden = append(r.Overridden, prev)
		}
		seen[key] = o
	}

	for key, c := range seen {
		sc := lang.Scope(key.Tree)
		r.Constraints[sc] = append(r.Constraints[sc], c)
	}
	for sc := range r.Constraints {
		cs := r.Constraints[sc]
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].Sym.Name < cs[j].Sym.Name })
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

// appendList joins two contributions to a list-valued symbol, in the order
// they were composed, skipping a duplicate. The merged constraint keeps the
// first one's position, since an error has to point somewhere, and its From
// names both.
func appendList(prev, c lang.Constraint) lang.Constraint {
	for _, part := range strings.Fields(prev.Value) {
		if part == c.Value {
			return prev
		}
	}
	merged := prev
	merged.Value = prev.Value + " " + c.Value
	merged.From = prev.From + ", " + c.From
	// Both directories stay resolved, space-separated like the value itself,
	// so the solution hash covers what is in each of them. Buildroot reads
	// these symbols as space-separated lists, so a path with a space in it
	// was never going to work here either.
	merged.Resolved = strings.TrimSpace(prev.Resolved + " " + c.Resolved)
	return merged
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

// providersOf names the composed fragments that can provide capabilities:
// the target and the profile.
func providersOf(frs []*lang.Fragment) string {
	var out []string
	for _, f := range frs {
		if f.ID.Kind == lang.Target || f.ID.Kind == lang.Profile {
			out = append(out, f.ID.String())
		}
	}
	if len(out) == 0 {
		return "no target or profile"
	}
	return strings.Join(out, ", ")
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
