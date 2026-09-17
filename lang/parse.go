package lang

import (
	"fmt"
	"strings"

	"github.com/vinodhalaharvi/silt/sexpr"
)

// Error is a diagnostic carrying the position that caused it. Every error the
// user sees must name a file and line: that is Invariant 3 applied to
// diagnostics, not just to explanations.
type Error struct {
	Pos sexpr.Pos
	Msg string
}

func (e *Error) Error() string { return e.Pos.Short() + ": " + e.Msg }

func errf(n *sexpr.Node, format string, a ...any) error {
	return &Error{Pos: n.Pos, Msg: fmt.Sprintf(format, a...)}
}

// ParseFile parses one .sx source into typed forms.
func ParseFile(src, path string) (*File, error) {
	nodes, err := sexpr.Parse(src, path)
	if err != nil {
		return nil, err
	}
	f := &File{Path: path}
	for _, n := range nodes {
		switch n.Head() {
		case "fragment":
			fr, err := parseFragment(n)
			if err != nil {
				return nil, err
			}
			f.Fragments = append(f.Fragments, fr)
		case "rules":
			r, err := parseRules(n)
			if err != nil {
				return nil, err
			}
			f.Rules = append(f.Rules, r)
		case "capabilities":
			c, err := parseCapabilityDecls(n)
			if err != nil {
				return nil, err
			}
			f.Capabilities = append(f.Capabilities, c)
		case "tree":
			d, err := parseTreeDecl(n)
			if err != nil {
				return nil, err
			}
			f.Trees = append(f.Trees, d)
		case "image":
			im, err := parseImage(n)
			if err != nil {
				return nil, err
			}
			f.Images = append(f.Images, im)
		case "":
			return nil, errf(n, "expected a list headed by a keyword")
		default:
			return nil, errf(n, "unknown top-level form %q; expected fragment, rules, image, capabilities or tree", n.Head())
		}
	}
	return f, nil
}

func parseID(n *sexpr.Node) (ID, error) {
	if n.Kind != sexpr.KindSymbol {
		return ID{}, errf(n, "expected a fragment id such as target:name")
	}
	k, name, ok := strings.Cut(n.Text, ":")
	if !ok {
		return ID{}, errf(n, "fragment id %q must be kind:name", n.Text)
	}
	switch Kind(k) {
	case Target, Profile, Feature:
	default:
		return ID{}, errf(n, "unknown fragment kind %q; expected target, profile or feature", k)
	}
	if name == "" {
		return ID{}, errf(n, "fragment id %q has an empty name", n.Text)
	}
	return ID{Kind: Kind(k), Name: name}, nil
}

func parseFragment(n *sexpr.Node) (*Fragment, error) {
	args := n.Args()
	if len(args) == 0 {
		return nil, errf(n, "fragment needs an id")
	}
	id, err := parseID(args[0])
	if err != nil {
		return nil, err
	}
	fr := &Fragment{ID: id, Pos: n.Pos, Constraints: map[Scope][]Constraint{}}
	for _, cl := range args[1:] {
		switch cl.Head() {
		case "doc":
			if fr.Doc, err = oneString(cl); err != nil {
				return nil, err
			}
		case "provides":
			caps, err := parseCapabilities(cl)
			if err != nil {
				return nil, err
			}
			// Targets and profiles may both provide; features may not.
			//
			// An earlier rule allowed only targets, on the reasoning that
			// targets describe hardware and profiles describe policy. Two
			// collisions disproved it: profile:minimal supplies BusyBox udhcpc
			// and profile:standard supplies systemd-networkd, and both are
			// things a feature needs and neither is hardware. A feature that
			// provided capabilities could satisfy another feature's
			// requirement, which would make composition order-dependent — so
			// features still may not.
			if id.Kind == Feature {
				return nil, errf(cl, "a feature may not provide capabilities (%s); "+
					"only targets and profiles may, or composition becomes order-dependent", id)
			}
			fr.Provides = append(fr.Provides, caps...)
		case "requires":
			caps, err := parseCapabilities(cl)
			if err != nil {
				return nil, err
			}
			if id.Kind == Target {
				return nil, errf(cl, "a target provides capabilities, it does not require them")
			}
			fr.Requires = append(fr.Requires, caps...)
		case "buildroot", "linux", "scope":
			sc, items, err := scopeBlock(cl)
			if err != nil {
				return nil, err
			}
			cs, gs, version, err := parseScope(items, sc, id.String())
			if err != nil {
				return nil, err
			}
			fr.Constraints[sc] = append(fr.Constraints[sc], cs...)
			fr.Guards = append(fr.Guards, gs...)
			if version != "" {
				fr.Version = version
			}
		default:
			return nil, errf(cl, "unknown clause %q in fragment", cl.Head())
		}
	}
	return fr, nil
}

func parseCapabilities(n *sexpr.Node) ([]Capability, error) {
	var out []Capability
	for _, c := range n.Args() {
		if c.Head() != "capability" || len(c.Args()) != 1 {
			return nil, errf(c, "expected (capability name)")
		}
		a := c.Args()[0]
		if a.Kind != sexpr.KindSymbol {
			return nil, errf(a, "capability name must be a symbol")
		}
		out = append(out, Capability{Name: a.Text, Pos: c.Pos})
	}
	return out, nil
}

// scopeBlock splits a scope block into its tree and its items. (buildroot
// ...) and (linux ...) are shorthand for (scope buildroot ...) and (scope
// linux ...); any other tree is written with scope, since only the registry
// knows which trees exist and every list must still begin with a keyword.
func scopeBlock(n *sexpr.Node) (Scope, []*sexpr.Node, error) {
	if n.Head() != "scope" {
		return Scope(n.Head()), n.Args(), nil
	}
	a := n.Args()
	if len(a) == 0 || a[0].Kind != sexpr.KindSymbol || !treeNameOK(a[0].Text) {
		return "", nil, errf(n, "(scope TREE ...) needs a tree name")
	}
	return Scope(a[0].Text), a[1:], nil
}

// parseScope reads the items of a scope block.
func parseScope(items []*sexpr.Node, sc Scope, from string) ([]Constraint, []Guarded, string, error) {
	var cs []Constraint
	var gs []Guarded
	var version string
	for _, item := range items {
		switch item.Head() {
		case "when":
			g, err := parseGuarded(item, sc, from)
			if err != nil {
				return nil, nil, "", err
			}
			gs = append(gs, g)
		case "custom-version":
			if sc != Linux {
				return nil, nil, "", errf(item, "custom-version is valid only in the linux scope")
			}
			v, err := oneString(item)
			if err != nil {
				return nil, nil, "", err
			}
			version = v
		default:
			c, err := parseConstraint(item, sc, from)
			if err != nil {
				return nil, nil, "", err
			}
			cs = append(cs, c)
		}
	}
	return cs, gs, version, nil
}

func parseConstraint(n *sexpr.Node, sc Scope, from string) (Constraint, error) {
	args := n.Args()
	sym := func(i int) (SymbolID, error) {
		if i >= len(args) || args[i].Kind != sexpr.KindSymbol {
			return SymbolID{}, errf(n, "%s: expected a symbol", n.Head())
		}
		return parseSymbolRef(args[i], sc)
	}

	switch h := n.Head(); h {
	case "y", "m", "n":
		if len(args) != 1 {
			return Constraint{}, errf(n, "(%s SYMBOL) takes exactly one symbol", h)
		}
		s, err := sym(0)
		if err != nil {
			return Constraint{}, err
		}
		return Constraint{Sym: s, Want: tristate(h), Pos: n.Pos, From: from}, nil

	case "at-least":
		if len(args) != 2 {
			return Constraint{}, errf(n, "(at-least TRISTATE SYMBOL) takes two arguments")
		}
		t, ok := parseTristate(args[0])
		if !ok {
			return Constraint{}, errf(args[0], "expected y, m or n")
		}
		s, err := sym(1)
		if err != nil {
			return Constraint{}, err
		}
		return Constraint{Sym: s, Want: t, AtLeast: true, Pos: n.Pos, From: from}, nil

	case "prefer":
		if len(args) != 2 {
			return Constraint{}, errf(n, "(prefer TRISTATE SYMBOL) takes two arguments; "+
				"the repair-policy form is (keep target)")
		}
		t, ok := parseTristate(args[0])
		if !ok {
			return Constraint{}, errf(args[0], "expected y, m or n")
		}
		s, err := sym(1)
		if err != nil {
			return Constraint{}, err
		}
		return Constraint{Sym: s, Want: t, Soft: true, Pos: n.Pos, From: from}, nil

	case "value":
		if len(args) != 2 || args[1].Kind != sexpr.KindString {
			return Constraint{}, errf(n, `(value SYMBOL "string") takes a symbol and a string`)
		}
		s, err := sym(0)
		if err != nil {
			return Constraint{}, err
		}
		return Constraint{Sym: s, Value: args[1].Text, IsValue: true, Pos: n.Pos, From: from}, nil

	case "require", "set":
		return Constraint{}, errf(n, "unknown form %q; see GRAMMAR.md", h)
	case "":
		return Constraint{}, errf(n, "expected a constraint form")
	default:
		return Constraint{}, errf(n, "unknown constraint form %q", h)
	}
}

func parseGuarded(n *sexpr.Node, sc Scope, from string) (Guarded, error) {
	args := n.Args()
	if len(args) < 2 {
		return Guarded{}, errf(n, "(when CONDITION CONSTRAINT...) needs a condition and at least one consequent")
	}
	cond, err := parseCond(args[0], sc)
	if err != nil {
		return Guarded{}, err
	}
	g := Guarded{Cond: cond, Pos: n.Pos, From: from, Scope: sc}
	for _, c := range args[1:] {
		cc, err := parseConstraint(c, sc, from)
		if err != nil {
			return Guarded{}, err
		}
		g.Then = append(g.Then, cc)
	}
	return g, nil
}

// parseCondConstraint parses a constraint used as a condition. A qualified
// symbol may name any tree even inside a scope, since a guard in a linux block
// conditioned on a Buildroot symbol is the ordinary cross-tree case.
func parseCondConstraint(n *sexpr.Node, sc Scope) (Constraint, error) {
	for _, a := range n.Args() {
		if a.Kind == sexpr.KindSymbol && strings.Contains(a.Text, ":") {
			return parseConstraint(n, "", "")
		}
	}
	return parseConstraint(n, sc, "")
}

func parseCond(n *sexpr.Node, sc Scope) (*Cond, error) {
	switch h := n.Head(); h {
	case "set?":
		if len(n.Args()) != 1 || n.Args()[0].Kind != sexpr.KindSymbol {
			return nil, errf(n, "(set? SYMBOL) takes one symbol")
		}
		id, err := parseSymbolRef(n.Args()[0], sc)
		if err != nil {
			return nil, err
		}
		return &Cond{Op: "set?", Sym: id, Pos: n.Pos}, nil
	case "and", "or":
		if len(n.Args()) < 2 {
			return nil, errf(n, "(%s ...) needs at least two conditions", h)
		}
		c := &Cond{Op: h, Pos: n.Pos}
		for _, a := range n.Args() {
			sub, err := parseCond(a, sc)
			if err != nil {
				return nil, err
			}
			c.Args = append(c.Args, sub)
		}
		return c, nil
	case "not":
		if len(n.Args()) != 1 {
			return nil, errf(n, "(not CONDITION) takes one condition")
		}
		sub, err := parseCond(n.Args()[0], sc)
		if err != nil {
			return nil, err
		}
		return &Cond{Op: "not", Args: []*Cond{sub}, Pos: n.Pos}, nil
	default:
		// A bare constraint is a condition. Inside a scope a bare symbol
		// belongs to that scope's tree; crossing trees, which is what rules
		// are for, takes an explicit tree:NAME.
		c, err := parseCondConstraint(n, sc)
		if err != nil {
			return nil, err
		}
		if c.Soft {
			return nil, errf(n, "a soft constraint cannot be a condition")
		}
		return &Cond{Op: "constraint", C: &c, Pos: n.Pos}, nil
	}
}

func parseRules(n *sexpr.Node) (*Rules, error) {
	args := n.Args()
	if len(args) == 0 || args[0].Kind != sexpr.KindSymbol {
		return nil, errf(n, "rules needs a name")
	}
	r := &Rules{Name: args[0].Text, Pos: n.Pos}
	for _, item := range args[1:] {
		if item.Head() != "when" {
			return nil, errf(item, "a rules block holds only (when ...) forms; "+
				"an unconditional assertion belongs in a fragment")
		}
		// Rules cross trees, so no scope is imposed on either side.
		g, err := parseGuarded(item, "", "rules:"+r.Name)
		if err != nil {
			return nil, err
		}
		r.Guards = append(r.Guards, g)
	}
	return r, nil
}

func tristate(s string) Tristate {
	switch s {
	case "n":
		return N
	case "m":
		return M
	}
	return Y
}

func parseTristate(n *sexpr.Node) (Tristate, bool) {
	if n.Kind != sexpr.KindSymbol {
		return N, false
	}
	switch n.Text {
	case "y", "m", "n":
		return tristate(n.Text), true
	}
	return N, false
}

func oneString(n *sexpr.Node) (string, error) {
	a := n.Args()
	if len(a) != 1 || a[0].Kind != sexpr.KindString {
		return "", errf(n, `(%s "string") takes one string`, n.Head())
	}
	return a[0].Text, nil
}

func parseCapabilityDecls(n *sexpr.Node) (*Capabilities, error) {
	c := &Capabilities{Pos: n.Pos}
	for _, item := range n.Args() {
		if item.Head() != "capability" || len(item.Args()) == 0 {
			return nil, errf(item, "expected (capability NAME ...)")
		}
		a := item.Args()
		if a[0].Kind != sexpr.KindSymbol {
			return nil, errf(a[0], "capability name must be a symbol")
		}
		d := CapabilityDecl{Name: a[0].Text, Pos: item.Pos}
		for _, opt := range a[1:] {
			switch opt.Head() {
			case "doc":
				s, err := oneString(opt)
				if err != nil {
					return nil, err
				}
				d.Doc = s
			case "symbol":
				if len(opt.Args()) != 1 || opt.Args()[0].Kind != sexpr.KindSymbol {
					return nil, errf(opt, "(symbol TREE:SYMBOL) takes one symbol")
				}
				id, err := parseSymbolRef(opt.Args()[0], "")
				if err != nil {
					return nil, err
				}
				d.Symbol = id
			default:
				return nil, errf(opt, "unknown capability clause %q", opt.Head())
			}
		}
		c.Decls = append(c.Decls, d)
	}
	return c, nil
}
