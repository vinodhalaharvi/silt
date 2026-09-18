package lang

import (
	"fmt"
	"strings"

	"github.com/vinodhalaharvi/silt/sexpr"
)

// A pack is a directory someone else maintains: fragments, the br2-external
// tree that implements them, and a declaration of what it offers.
//
//	(pack hello-silt
//	  (version "0.1.0")
//	  (doc "The smallest pack: one package, one feature, one boot assertion")
//	  (requires (silt ">=0.9") (buildroot "2025.02.16"))
//	  (external "br2-external")
//	  (provides (feature hello-silt)))
//
// The declaration is the pack's interface, and it is checked both ways:
// everything promised must exist, and nothing else may be exported. A pack
// whose fragments drift from what it says it offers is the version of this
// project's whole problem that happens between repositories rather than
// between Kconfig trees.
type PackDecl struct {
	Name     string
	Version  string
	Doc      string
	Requires map[string]string // tree or tool name -> version constraint
	External string            // br2-external directory, relative to the pack
	Provides []ID
	Dir      string // where it was loaded from, filled in by the loader
	Pos      sexpr.Pos
}

func parsePack(n *sexpr.Node) (*PackDecl, error) {
	args := n.Args()
	if len(args) == 0 || args[0].Kind != sexpr.KindSymbol {
		return nil, errf(n, "(pack NAME ...) needs a name")
	}
	p := &PackDecl{Name: args[0].Text, Requires: map[string]string{}, Pos: n.Pos}
	if !nameOK(p.Name) {
		return nil, errf(n, "%q is not a pack name (lowercase letters, digits, hyphens)", p.Name)
	}
	for _, cl := range args[1:] {
		switch cl.Head() {
		case "version", "doc", "external":
			s, err := oneString(cl)
			if err != nil {
				return nil, err
			}
			switch cl.Head() {
			case "version":
				p.Version = s
			case "doc":
				p.Doc = s
			case "external":
				p.External = s
			}
		case "requires":
			for _, r := range cl.Args() {
				s, err := oneString(r)
				if err != nil {
					return nil, errf(r, `expected (NAME "version")`)
				}
				p.Requires[r.Head()] = s
			}
		case "provides":
			for _, pr := range cl.Args() {
				kind, err := kindOf(pr.Head())
				if err != nil {
					return nil, errf(pr, "%v; write (target NAME), (profile NAME) or (feature NAME)", err)
				}
				a := pr.Args()
				if len(a) != 1 || a[0].Kind != sexpr.KindSymbol {
					return nil, errf(pr, "(%s NAME) takes one name", pr.Head())
				}
				p.Provides = append(p.Provides, ID{Kind: kind, Name: a[0].Text})
			}
		default:
			return nil, errf(cl, "unknown pack clause %q", cl.Head())
		}
	}
	if p.Version == "" {
		return nil, errf(n, `pack %s needs a (version "...")`, p.Name)
	}
	return p, nil
}

func kindOf(s string) (Kind, error) {
	switch k := Kind(s); k {
	case Target, Profile, Feature:
		return k, nil
	}
	return "", fmt.Errorf("%q is not a fragment kind", s)
}

func nameOK(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Satisfies reports whether a version meets a pack's requirement. Only exact
// equality and >= are understood, which is all the requirements in this
// repository use; anything else is an error rather than a guess, because a
// constraint nobody enforces is worse than none.
func Satisfies(have, want string) (bool, error) {
	if strings.HasPrefix(want, ">=") {
		return atLeast(have, strings.TrimSpace(strings.TrimPrefix(want, ">="))), nil
	}
	if strings.ContainsAny(want, "<>~^*") {
		return false, fmt.Errorf("version constraint %q is not understood; use an exact version or >=", want)
	}
	return have == want, nil
}

// atLeast compares dotted versions field by field, numerically where both
// sides are numbers. Buildroot's 2025.02.16 and Linux's 6.12 are both this
// shape.
func atLeast(have, want string) bool {
	h, w := strings.FieldsFunc(have, isSep), strings.FieldsFunc(want, isSep)
	for i := range w {
		if i >= len(h) {
			return false
		}
		a, aok := number(h[i])
		b, bok := number(w[i])
		switch {
		case aok && bok && a != b:
			return a > b
		case !(aok && bok) && h[i] != w[i]:
			return h[i] > w[i]
		}
	}
	return true
}

func isSep(r rune) bool { return r == '.' || r == '-' || r == '_' }

func number(s string) (int, bool) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, s != ""
}
