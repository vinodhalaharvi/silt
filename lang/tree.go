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
	// Kind is the only thing that says what a tree is: kconfig, whose
	// symbols come from Kconfig files and which emits a config file, or
	// wasm-component, whose symbols come from the interfaces WebAssembly
	// components import and which emits nothing. Devicetree is deliberately
	// not a kind: it binds to drivers by string match at runtime and is not a
	// constraint system (HANDOFF §8), and a kind that is never implemented is
	// a promise in the schema.
	Kind string
	// Prefix is a spelling convention checked on every symbol written for
	// this tree. It catches BR2_X in a linux block; it is never used to
	// decide which tree a symbol belongs to.
	Prefix string
	// ConsumedBy is the Buildroot symbol that hands this tree's emitted
	// config fragment to its build. Empty for buildroot itself.
	ConsumedBy string
	Source     string // checkout of the tree, for importing it

	// Components and Wit belong to a wasm-component tree. Components are
	// the binaries whose imports are the tree's facts; a tree may name
	// several, and its symbols are the union of what they import. Wit is
	// where the vocabulary comes from: every interface the pack defines,
	// imported or not, so that a policy naming an interface nobody imports
	// can be told apart from one naming an interface that does not exist.
	//
	// Both are written relative to the pack's br2-external tree and
	// resolved by the pack loader into the Resolved fields, against the same
	// base (path ...) uses. Two bases would let check read one file while
	// the image carries another, and both would succeed.
	Components         []string
	Wit                []string
	ResolvedComponents []string
	ResolvedWit        []string
	// Env is what the tree's own Kconfig needs before it can be read at all.
	// The kernel sources arch/$(SRCARCH)/Kconfig, so without ARCH there is
	// no tree to import.
	Env map[string]string
	Pos sexpr.Pos
}

// Tree kinds.
const (
	KindKconfig       = "kconfig"
	KindWasmComponent = "wasm-component"
)

// CheckOnly reports a tree that constrains what an image may contain but
// configures nothing: it has no file to emit and no Buildroot symbol to hand
// one to.
func (d TreeDecl) CheckOnly() bool { return d.Kind == KindWasmComponent }

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
			if d.Kind != KindKconfig && d.Kind != KindWasmComponent {
				return nil, errf(cl, "tree kind %q is not supported; kconfig and wasm-component are. "+
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
		case "component", "wit":
			s, err := oneString(cl)
			if err != nil {
				return nil, err
			}
			if cl.Head() == "component" {
				d.Components = append(d.Components, s)
			} else {
				d.Wit = append(d.Wit, s)
			}
		case "consumed-by":
			if len(cl.Args()) != 1 || cl.Args()[0].Kind != sexpr.KindSymbol {
				return nil, errf(cl, "(consumed-by BR2_SYMBOL) takes one Buildroot symbol")
			}
			d.ConsumedBy = cl.Args()[0].Text
		case "env":
			d.Env = map[string]string{}
			for _, a := range cl.Args() {
				if a.Head() == "" || len(a.Args()) != 1 || a.Args()[0].Kind != sexpr.KindString {
					return nil, errf(a, `expected (NAME "value")`)
				}
				d.Env[a.Head()] = a.Args()[0].Text
			}
		default:
			return nil, errf(cl, "unknown tree clause %q", cl.Head())
		}
	}
	if d.Kind == "" {
		return nil, errf(n, "tree %s needs (kind kconfig) or (kind wasm-component)", d.Name)
	}
	if err := checkKindClauses(n, d); err != nil {
		return nil, err
	}
	return d, nil
}

// checkKindClauses rejects clauses that mean nothing for the tree's kind.
//
// Accepting them silently would be worse than an error: a component tree with
// a consumed-by reads as though its policy reaches Buildroot, and a Kconfig
// tree with a component reads as though that binary were checked.
func checkKindClauses(n *sexpr.Node, d *TreeDecl) error {
	if d.Kind == KindWasmComponent {
		switch {
		case d.ConsumedBy != "":
			return errf(n, "tree %s is a wasm-component tree, which configures nothing; "+
				"it takes no (consumed-by ...)", d.Name)
		case d.Prefix != "" || d.Source != "" || d.Env != nil:
			return errf(n, "tree %s is a wasm-component tree; it takes (component ...) and "+
				"(wit ...), not prefix, source or env", d.Name)
		case len(d.Components) == 0:
			return errf(n, "tree %s names no (component \"...\"); a component tree's "+
				"symbols come from its components", d.Name)
		case len(d.Wit) == 0:
			return errf(n, "tree %s names no (wit \"...\"); without the interfaces the "+
				"pack defines, a misspelled policy symbol would pass forever", d.Name)
		}
		return nil
	}
	if len(d.Components) > 0 || len(d.Wit) > 0 {
		return errf(n, "tree %s is a kconfig tree; (component ...) and (wit ...) "+
			"belong to wasm-component trees", d.Name)
	}
	return nil
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
