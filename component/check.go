package component

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/verify"
)

// Applies reports whether a component tree is part of an image: the image
// states policy in its scope, or carries one of its components. Either one
// is enough. Policy for a component nothing carries is a finding (it checks
// nothing), and a carried component with no policy is still checked against
// its vocabulary, because an import no WIT defines is a finding regardless.
func Applies(res *compose.Result, decl lang.TreeDecl) bool {
	if len(res.Constraints[lang.Scope(decl.Name)]) > 0 {
		return true
	}
	for _, c := range decl.ResolvedComponents {
		if Carried(res, c) {
			return true
		}
	}
	return false
}

// Trees lists the component trees that apply to an image, sorted.
func Trees(res *compose.Result) []lang.TreeDecl {
	var out []lang.TreeDecl
	for _, d := range res.Trees {
		if d.CheckOnly() && Applies(res, d) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Carried reports whether a file reaches the image: it is, or is inside,
// something a (path ...) the composition states carries.
//
// This is the assertion that keeps check and build on the same bytes. A
// component checked here and absent from the image would be a claim about a
// file nothing ships; a component shipped from somewhere else would be
// unchecked. Only a file the composition carries is the file the image has.
//
// Both sides are made absolute before comparing. A (path ...) resolves
// against the pack directory as it was given, so --pack packs/x leaves it
// relative, while component paths are absolute; comparing the two as given
// found every component uncarried whenever the pack was named relatively,
// which is how it is usually named.
func Carried(res *compose.Result, file string) bool {
	abs, err := filepath.Abs(file)
	if err != nil {
		return false
	}
	for _, cs := range res.Constraints {
		for _, c := range cs {
			if !c.IsPath || c.Soft {
				continue
			}
			for _, r := range strings.Fields(c.Resolved) {
				root, err := filepath.Abs(r)
				if err != nil {
					continue
				}
				rel, err := filepath.Rel(root, abs)
				if err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
					return true
				}
			}
		}
	}
	return false
}

// Findings checks an image's policy for one component tree against what its
// components import. It is the second of two stages: verify.Check has
// already asked whether each policy symbol exists in the vocabulary, and this
// asks whether the components honour it.
func Findings(res *compose.Result, t *Tree) []verify.Finding {
	var out []verify.Finding
	sc := lang.Scope(t.Decl.Name)
	pos := t.Decl.Pos.Short()

	for _, c := range res.Constraints[sc] {
		// Only (n ...). A component tree's claim is what a component
		// cannot do; "must import X" is not a security property, and a
		// value or path has no meaning for an import.
		if c.Soft || c.IsValue || c.AtLeast || c.Want != lang.N {
			out = append(out, verify.Finding{
				Pos: c.Pos.Short(), Symbol: c.Sym.Name,
				Message: fmt.Sprintf("%s: component trees take only (n ...)", c.Sym),
				Detail:  "stated by " + c.From,
			})
			continue
		}
		if by := t.ImportedBy[c.Sym.Name]; len(by) > 0 {
			out = append(out, verify.Finding{
				Pos: c.Pos.Short(), Symbol: c.Sym.Name,
				Message: fmt.Sprintf("%s is forbidden, and %s imports %s",
					c.Sym, strings.Join(by, ", "), t.Vocabulary[c.Sym.Name].Name),
				Detail: "forbidden by " + c.From,
			})
		}
	}

	for _, rel := range sortedKeys(t.Outside) {
		for _, name := range t.Outside[rel] {
			out = append(out, verify.Finding{
				Pos: pos,
				Message: fmt.Sprintf("%s: %s imports %s, which the pack's WIT does not define",
					t.Decl.Name, rel, name),
				Detail: "no policy can forbid an interface its vocabulary cannot name",
			})
		}
	}
	for _, rel := range sortedKeys(t.NotInterface) {
		for _, name := range t.NotInterface[rel] {
			out = append(out, verify.Finding{
				Pos: pos,
				Message: fmt.Sprintf("%s: %s imports %q, which is not a WIT interface",
					t.Decl.Name, rel, name),
				Detail: "policy is written against interfaces and cannot reason about it",
			})
		}
	}
	for i, abs := range t.Decl.ResolvedComponents {
		if !Carried(res, abs) {
			out = append(out, verify.Finding{
				Pos: pos,
				Message: fmt.Sprintf("%s: %s is checked but not carried into the image",
					t.Decl.Name, t.Decl.Components[i]),
				Detail: "no (path ...) the image states reaches it; the check would be " +
					"about a file the image does not have",
			})
		}
	}
	return out
}

// Why explains one symbol of a component tree: what the pack's vocabulary
// says it is, and which components import it. Policy stated about it is the
// caller's to report, as for any other tree.
func (t *Tree) Why(sym string) string {
	it, ok := t.Vocabulary[sym]
	if !ok {
		return fmt.Sprintf("%s:%s is not an interface the pack's WIT defines\n", t.Decl.Name, sym)
	}
	s := fmt.Sprintf("%s:%s is %s\n  defined at %s:%d\n", t.Decl.Name, sym, it.Name, it.File, it.Line)
	if by := t.ImportedBy[sym]; len(by) > 0 {
		return s + "  imported by " + strings.Join(by, ", ") + "\n"
	}
	return s + "  imported by nothing in this tree\n"
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
