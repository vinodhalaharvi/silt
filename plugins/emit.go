package plugins

import (
	"fmt"
	"sort"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/emit"
	"github.com/vinodhalaharvi/silt/esphome"
	"github.com/vinodhalaharvi/silt/lang"
)

// Writing a composition out, per kind.
//
// emit knows one thing it should not: that a tree's configuration is handed to
// Buildroot through a symbol, which is true of a Kconfig fragment and of
// nothing else. An ESPHome document is built by esphome on another processor;
// a device tree is built by the kernel. Both were special cases in a package
// that is supposed to be about writing files.
//
// So the question "what file does this tree produce, and how does its builder
// receive it" belongs to the kind. What is left in emit is rendering a Kconfig
// file, which is the part every Kconfig tree shares.

// Emitted is one tree's configuration and where its builder expects it.
type Emitted struct {
	Tree string
	Name string
	Body []byte
	// ConsumedBy is the Buildroot symbol that hands this file to the build,
	// empty for a tree Buildroot does not consume. It is the kind's answer
	// rather than core's assumption: a tree with no answer is not an error,
	// it is a tree built by something else.
	ConsumedBy string
}

// EmitAll writes every tree a composition states, each through its own kind.
//
// The Buildroot defconfig is written last, because it names the files the
// other trees produced and cannot be rendered before they exist.
func EmitAll(res *compose.Result, trees map[string]lang.TreeDecl) ([]Emitted, error) {
	var out []Emitted
	paths := map[string]string{}

	// Every tree the composition states, not only the ones Buildroot
	// consumes. emit.Trees answers the narrower question - which trees are
	// handed to Buildroot as fragments - and that is the assumption this is
	// here to remove.
	for _, tree := range statedTrees(res, trees) {
		d, ok := trees[tree]
		if !ok {
			return nil, fmt.Errorf("tree %s is stated and not declared", tree)
		}
		e, err := emitTree(res, tree, d)
		if err != nil {
			return nil, err
		}
		if e == nil {
			continue // this kind writes nothing, which is a fact and not a fault
		}
		out = append(out, *e)
		paths[tree] = e.Name
	}

	def, err := emit.BuildrootDefconfig(res, paths)
	if err != nil {
		return nil, err
	}
	out = append(out, Emitted{
		Tree: string(lang.Buildroot),
		Name: emit.FileName(string(lang.Buildroot)),
		Body: []byte(def),
	})
	return out, nil
}

// statedTrees is every tree other than Buildroot that the composition makes a
// hard claim in, in a stable order.
func statedTrees(res *compose.Result, trees map[string]lang.TreeDecl) []string {
	var out []string
	for sc, cs := range res.Constraints {
		if sc == lang.Buildroot {
			continue
		}
		hard := false
		for _, c := range cs {
			if !c.Soft {
				hard = true
				break
			}
		}
		if hard {
			out = append(out, string(sc))
		}
	}
	sort.Strings(out)
	return out
}

// emitTree is the per-kind answer. nil means this kind produces no file.
func emitTree(res *compose.Result, tree string, d lang.TreeDecl) (*Emitted, error) {
	switch d.Kind {
	case lang.KindKconfig:
		if d.ConsumedBy == "" {
			return nil, fmt.Errorf("tree %s has constraints but no (consumed-by BR2_...) "+
				"symbol to hand its config to Buildroot", tree)
		}
		return &Emitted{
			Tree:       tree,
			Name:       emit.FileName(tree),
			Body:       []byte(emit.Defconfig(res, lang.Scope(tree))),
			ConsumedBy: d.ConsumedBy,
		}, nil

	case lang.KindESPHome:
		doc := esphome.New()
		for _, c := range res.Constraints[lang.Scope(tree)] {
			if !c.IsValue {
				return nil, fmt.Errorf("%s: %s is a tristate, and an esphome setting takes a value",
					c.Pos.Short(), c.Sym)
			}
			doc.Set(c.Sym.Name, c.Value)
		}
		if doc.Len() == 0 {
			return nil, nil
		}
		// No ConsumedBy: esphome builds this, on a different processor, and
		// Buildroot has no symbol to hand it to. Before the kinds were
		// separated this was an error demanding a symbol that could not
		// exist.
		return &Emitted{Tree: tree, Name: tree + ".yaml", Body: []byte(doc.Render())}, nil

	case lang.KindWasmComponent:
		// Constrains what an image may contain and configures nothing.
		return nil, nil
	}
	return nil, fmt.Errorf("tree %s has kind %q, which cannot be emitted", tree, d.Kind)
}
