package lang

import (
	"strings"

	"github.com/vinodhalaharvi/silt/sexpr"
)

func parseImage(n *sexpr.Node) (*Image, error) {
	args := n.Args()
	if len(args) == 0 || args[0].Kind != sexpr.KindSymbol {
		return nil, errf(n, "image needs a name")
	}
	im := &Image{Name: args[0].Text, Pos: n.Pos, Constraints: map[Scope][]Constraint{}}
	from := "image:" + im.Name
	var err error

	for _, cl := range args[1:] {
		switch cl.Head() {
		case "doc":
			if im.Doc, err = oneString(cl); err != nil {
				return nil, err
			}

		case "compose":
			for _, a := range cl.Args() {
				id, err := parseID(a)
				if err != nil {
					return nil, err
				}
				im.Compose = append(im.Compose, id)
			}
			if err := checkComposeShape(cl, im.Compose); err != nil {
				return nil, err
			}

		case "buildroot", "linux", "scope":
			sc, items, err := scopeBlock(cl)
			if err != nil {
				return nil, err
			}
			cs, _, version, err := parseScope(items, sc, from)
			if err != nil {
				return nil, err
			}
			im.Constraints[sc] = append(im.Constraints[sc], cs...)
			if version != "" {
				im.Version = version
			}

		case "override":
			for _, a := range cl.Args() {
				c, err := parseConstraint(a, "", from)
				if err != nil {
					return nil, err
				}
				im.Override = append(im.Override, c)
			}

		case "opaque", "environment":
			var dst *[]Constraint
			if cl.Head() == "opaque" {
				dst = &im.Opaque
			} else {
				dst = &im.Environment
			}
			for _, a := range cl.Args() {
				if a.Head() != "value" {
					return nil, errf(a, "(%s ...) holds only (value SYMBOL \"...\") forms", cl.Head())
				}
				c, err := parseConstraint(a, "", from)
				if err != nil {
					return nil, err
				}
				*dst = append(*dst, c)
			}

		case "unmanaged":
			for _, a := range cl.Args() {
				if a.Kind != sexpr.KindSymbol {
					return nil, errf(a, "unmanaged takes symbol patterns such as buildroot:BR2_TARGET_UBOOT_*")
				}
				id, err := parseSymbolRef(a, "")
				if err != nil {
					return nil, err
				}
				im.Unmanaged = append(im.Unmanaged, id)
			}

		case "delegate":
			d, err := parseDelegate(cl)
			if err != nil {
				return nil, err
			}
			im.Delegate = append(im.Delegate, d)

		case "verified-against":
			if im.VerifiedAgainst == nil {
				im.VerifiedAgainst = map[string]string{}
			}
			for _, a := range cl.Args() {
				h := a.Head()
				if h == "" || len(a.Args()) != 1 || a.Args()[0].Kind != sexpr.KindString {
					return nil, errf(a, `expected (TREE "version"), e.g. (buildroot "2025.02.16")`)
				}
				im.VerifiedAgainst[h] = a.Args()[0].Text
			}

		case "repair-policy":
			if err := parsePolicy(cl, &im.Policy); err != nil {
				return nil, err
			}

		default:
			return nil, errf(cl, "unknown clause %q in image", cl.Head())
		}
	}
	return im, nil
}

// checkComposeShape enforces exactly one target, exactly one profile, and any
// number of features.
//
// Profiles are mutually exclusive by construction: profile:minimal wants musl
// and static libs, profile:standard wants glibc and systemd. Composing two is
// UNSAT, and saying so here is far kinder than discovering it as a solver
// conflict with no explanation of why two profiles were ever allowed.
func checkComposeShape(n *sexpr.Node, ids []ID) error {
	var targets, profiles []ID
	for _, id := range ids {
		switch id.Kind {
		case Target:
			targets = append(targets, id)
		case Profile:
			profiles = append(profiles, id)
		}
	}
	if len(targets) != 1 {
		return errf(n, "compose needs exactly one target, found %d", len(targets))
	}
	if len(profiles) != 1 {
		if len(profiles) > 1 {
			return errf(n, "compose needs exactly one profile, found %d (%s); "+
				"profiles are mutually exclusive, features are additive",
				len(profiles), joinIDs(profiles))
		}
		return errf(n, "compose needs exactly one profile, found none")
	}
	return nil
}

func joinIDs(ids []ID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = id.String()
	}
	return strings.Join(s, ", ")
}

func parseDelegate(n *sexpr.Node) (Delegation, error) {
	args := n.Args()
	if len(args) != 2 || args[0].Kind != sexpr.KindSymbol {
		return Delegation{}, errf(n, "(delegate TREE (custom-config-file \"...\")) takes a tree and a delegation")
	}
	d := Delegation{Tree: args[0].Text, Pos: n.Pos}
	switch args[1].Head() {
	case "custom-config-file":
		s, err := oneString(args[1])
		if err != nil {
			return Delegation{}, err
		}
		d.CustomConfig = s
	case "config-fragment-files":
		for _, a := range args[1].Args() {
			if a.Kind != sexpr.KindString {
				return Delegation{}, errf(a, "config-fragment-files takes strings")
			}
			d.FragmentFiles = append(d.FragmentFiles, a.Text)
		}
	default:
		return Delegation{}, errf(args[1], "unknown delegation %q", args[1].Head())
	}
	return d, nil
}

func parsePolicy(n *sexpr.Node, p *Policy) error {
	for _, cl := range n.Args() {
		a := cl.Args()
		switch cl.Head() {
		case "minimize":
			if len(a) != 1 || a[0].Kind != sexpr.KindSymbol {
				return errf(cl, "(minimize METRIC) takes one of: changed-symbols, packages")
			}
			switch a[0].Text {
			case "changed-symbols", "packages":
			default:
				return errf(a[0], "unknown metric %q", a[0].Text)
			}
			p.Minimize = append(p.Minimize, a[0].Text)
		case "keep":
			if len(a) != 1 || a[0].Kind != sexpr.KindSymbol {
				return errf(cl, "(keep WHAT) takes one of: target, profile, toolchain")
			}
			switch a[0].Text {
			case "target", "profile", "toolchain":
			default:
				return errf(a[0], "unknown keep target %q", a[0].Text)
			}
			p.Keep = append(p.Keep, a[0].Text)
		case "baseline":
			s, err := oneString(cl)
			if err != nil {
				return err
			}
			p.Baseline = s
		case "prefer":
			return errf(cl, "repair-policy uses (keep target), not (prefer preserve-target); "+
				"prefer is the soft-constraint form")
		default:
			return errf(cl, "unknown repair-policy clause %q", cl.Head())
		}
	}
	return nil
}
