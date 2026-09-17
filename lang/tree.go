package lang

import (
	"fmt"
	"strings"

	"github.com/vinodhalaharvi/silt/sexpr"
)

// SymbolID is a symbol's identity: the tree it belongs to and its name there.
//
// The name alone is not an identity. CONFIG_ is not a namespace: Linux has
// CONFIG_ARM64, BusyBox has CONFIG_DESKTOP, U-Boot has CONFIG_FIT_SIGNATURE,
// and Buildroot compiles its own copy of the Kconfig parser with the prefix
// set to the empty string (support/kconfig/Makefile.br). The prefix is a
// compile-time flag of whichever conf binary reads the file. Inferring the
// tree from it made two unrelated symbols one solver variable the moment a
// second CONFIG_ tree appeared.
//
// So the tree comes from where the symbol is written: the enclosing scope
// block, or an explicit tree:NAME qualifier where there is no scope.
type SymbolID struct {
	Tree string
	Name string
}

func (s SymbolID) String() string {
	if s.Tree == "" {
		return s.Name
	}
	return s.Tree + ":" + s.Name
}

// IsZero reports an unset identity.
func (s SymbolID) IsZero() bool { return s.Name == "" }

// TreeDecl declares a configuration tree.
//
//	(tree busybox (kind kconfig) (prefix "CONFIG_")
//	  (consumed-by BR2_PACKAGE_BUSYBOX_CONFIG_FRAGMENT_FILES))
type TreeDecl struct {
	Name string
	// Kind is the only thing that says what a tree is. Only kconfig exists.
	// Devicetree is deliberately not a kind: it binds to drivers by string
	// match at runtime and is not a constraint system (HANDOFF §8), and a
	// kind that is never implemented is a promise in the schema.
	Kind string
	// Prefix is a spelling convention checked on every symbol written for
	// this tree. It catches BR2_X in a linux block; it is never used to
	// decide which tree a symbol belongs to.
	Prefix string
	// ConsumedBy is the Buildroot symbol that hands this tree's emitted
	// config fragment to its build. Empty for buildroot itself.
	ConsumedBy string
	Source     string // checkout of the tree, for importing it
	Pos        sexpr.Pos
}

// BuiltinTrees are declared without a (tree ...) form, so a library written
// before the registry existed keeps working. Anything else must be declared.
func BuiltinTrees() map[string]TreeDecl {
	return map[string]TreeDecl{
		"buildroot": {Name: "buildroot", Kind: "kconfig", Prefix: "BR2_"},
		"linux": {Name: "linux", Kind: "kconfig", Prefix: "CONFIG_",
			ConsumedBy: "BR2_LINUX_KERNEL_CONFIG_FRAGMENT_FILES"},
	}
}

var treeNameOK = func(s string) bool {
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

func parseTreeDecl(n *sexpr.Node) (*TreeDecl, error) {
	args := n.Args()
	if len(args) == 0 || args[0].Kind != sexpr.KindSymbol || !treeNameOK(args[0].Text) {
		return nil, errf(n, "(tree NAME ...) needs a lowercase name")
	}
	d := &TreeDecl{Name: args[0].Text, Pos: n.Pos}
	for _, cl := range args[1:] {
		switch cl.Head() {
		case "kind":
			if len(cl.Args()) != 1 || cl.Args()[0].Kind != sexpr.KindSymbol {
				return nil, errf(cl, "(kind KIND) takes one symbol")
			}
			d.Kind = cl.Args()[0].Text
			if d.Kind != "kconfig" {
				return nil, errf(cl, "tree kind %q is not supported; only kconfig is. "+
					"Devicetree binds by compatible string at runtime and is not a constraint system", d.Kind)
			}
		case "prefix", "source":
			s, err := oneString(cl)
			if err != nil {
				return nil, err
			}
			if cl.Head() == "prefix" {
				d.Prefix = s
			} else {
				d.Source = s
			}
		case "consumed-by":
			if len(cl.Args()) != 1 || cl.Args()[0].Kind != sexpr.KindSymbol {
				return nil, errf(cl, "(consumed-by BR2_SYMBOL) takes one Buildroot symbol")
			}
			d.ConsumedBy = cl.Args()[0].Text
		default:
			return nil, errf(cl, "unknown tree clause %q", cl.Head())
		}
	}
	if d.Kind == "" {
		return nil, errf(n, "tree %s needs (kind kconfig)", d.Name)
	}
	return d, nil
}

// parseSymbolRef reads a symbol written either bare, taking the enclosing
// scope's tree, or as tree:NAME. Outside a scope the qualifier is required:
// there is nothing else to take the tree from.
func parseSymbolRef(n *sexpr.Node, scope Scope) (SymbolID, error) {
	if n.Kind != sexpr.KindSymbol {
		return SymbolID{}, errf(n, "expected a symbol")
	}
	text := n.Text
	if i := strings.IndexByte(text, ':'); i >= 0 {
		id := SymbolID{Tree: text[:i], Name: text[i+1:]}
		if !treeNameOK(id.Tree) || id.Name == "" {
			return SymbolID{}, errf(n, "%q is not TREE:SYMBOL", text)
		}
		if scope != "" && Scope(id.Tree) != scope {
			return SymbolID{}, errf(n, "%s is qualified with tree %s but written in a %s scope", text, id.Tree, scope)
		}
		return id, checkName(n, id.Name)
	}
	if scope == "" {
		return SymbolID{}, errf(n, "%s needs a tree outside a scope block; write TREE:%s, "+
			"e.g. buildroot:%s", text, text, text)
	}
	return SymbolID{Tree: string(scope), Name: text}, checkName(n, text)
}

func checkName(n *sexpr.Node, name string) error {
	for i, r := range name {
		ok := r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			(r == '*' && i == len(name)-1)
		if !ok {
			return errf(n, "%q is not a Kconfig symbol name", name)
		}
	}
	return nil
}

// CheckPrefix reports a symbol spelled against its tree's convention.
func CheckPrefix(id SymbolID, decl TreeDecl) error {
	if decl.Prefix != "" && !strings.HasPrefix(id.Name, decl.Prefix) {
		return fmt.Errorf("%s: tree %s spells its symbols %s*", id, id.Tree, decl.Prefix)
	}
	return nil
}
