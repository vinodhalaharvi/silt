package component

import (
	"fmt"
	"os"
	"sort"

	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

// Tree is a loaded wasm-component tree: what its pack says may exist, and
// what its components actually import.
type Tree struct {
	Decl lang.TreeDecl
	// Vocabulary is every interface the pack's WIT defines, by symbol.
	Vocabulary map[string]Interface
	// ImportedBy maps a symbol to the components importing it. A symbol
	// absent from here is not imported by anything in the tree.
	ImportedBy map[string][]string
	// Outside are imports whose interface the pack's WIT does not define,
	// by component. They are findings, not facts to reason about: a policy
	// cannot forbid what its vocabulary cannot name, so an import outside
	// it must fail rather than pass unexamined.
	Outside map[string][]string
	// NotInterface are imports that are not interface names at all — a
	// bare function or instance — by component.
	NotInterface map[string][]string
}

// Load reads a component tree's WIT and binaries.
func Load(decl lang.TreeDecl) (*Tree, error) {
	if decl.Kind != lang.KindWasmComponent {
		return nil, fmt.Errorf("tree %s is %s, not %s", decl.Name, decl.Kind, lang.KindWasmComponent)
	}
	if len(decl.ResolvedComponents) == 0 || len(decl.ResolvedWit) == 0 {
		// Only the pack loader resolves them, against the pack's
		// br2-external tree. A component tree declared anywhere else has no
		// base that agrees with what the image carries.
		return nil, fmt.Errorf("%s: tree %s is not declared by a pack with an (external ...) tree, "+
			"so its components have no base the image carries them from",
			decl.Pos.Short(), decl.Name)
	}
	vocab, err := Vocabulary(decl.ResolvedWit)
	if err != nil {
		return nil, fmt.Errorf("tree %s: %w", decl.Name, err)
	}
	t := &Tree{
		Decl:         decl,
		Vocabulary:   vocab,
		ImportedBy:   map[string][]string{},
		Outside:      map[string][]string{},
		NotInterface: map[string][]string{},
	}
	for i, path := range decl.ResolvedComponents {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		names, err := Imports(data)
		if err != nil {
			return nil, fmt.Errorf("tree %s: %s %w", decl.Name, decl.Components[i], err)
		}
		rel := decl.Components[i]
		for _, name := range names {
			sym, err := Symbol(name)
			switch {
			case err != nil:
				t.NotInterface[rel] = append(t.NotInterface[rel], name)
			case vocab[sym].Name == "":
				t.Outside[rel] = append(t.Outside[rel], name)
			default:
				if !contains(t.ImportedBy[sym], rel) {
					t.ImportedBy[sym] = append(t.ImportedBy[sym], rel)
				}
			}
		}
	}
	return t, nil
}

// Kconfig presents the vocabulary as a Kconfig tree of bool symbols with no
// dependencies, so verify.Check runs on it unchanged. That check's job here is
// the vocabulary one: a policy naming a symbol the pack's WIT does not define
// is a finding, which is how a misspelled (n ...) is caught instead of passing
// forever. Whether a symbol is imported is not Kconfig's question and is not
// encoded here; Findings answers it.
func (t *Tree) Kconfig() *kconfig.Tree {
	kt := &kconfig.Tree{Symbols: map[string]*kconfig.Symbol{}}
	syms := make([]string, 0, len(t.Vocabulary))
	for s := range t.Vocabulary {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		it := t.Vocabulary[s]
		kt.Symbols[s] = &kconfig.Symbol{Name: s, Type: kconfig.Bool, File: it.File, Line: it.Line}
		kt.Order = append(kt.Order, s)
	}
	return kt
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
