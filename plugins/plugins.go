// Package plugins binds each kind of tree to the questions a plan asks.
//
// Core composes plans and knows nothing about Kconfig, YAML or device trees;
// each kind here supplies an Open and an Answer and nothing else is required
// of it. The registry at the bottom is the only place in silt that knows which
// kinds exist, and cmd/silt names none of them.
//
// What is wired so far is Settled - what a name ends up as - because that is
// what a same-value rule needs, and because it is the question whose answer
// has the same shape in every tree: a string. Holds and Exists still go
// through verify, which checks a whole composition at once rather than a claim
// at a time; moving those is the next step and is a migration rather than a
// new capability.
package plugins

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/dt"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugin"
	"github.com/vinodhalaharvi/silt/verify"
)

// Stated is what an image says, indexed for lookup. Every kind needs it,
// because what a name settles to is first of all what the composition says it
// is; a tree is consulted only for what the composition leaves open.
type Stated map[lang.SymbolID]lang.Constraint

// Index builds a Stated from a composition's constraints.
func Index(byScope map[lang.Scope][]lang.Constraint) Stated {
	out := Stated{}
	for _, cs := range byScope {
		for _, c := range cs {
			out[c.Sym] = c
		}
	}
	return out
}

func (s Stated) value(id lang.SymbolID) (string, bool) {
	c, ok := s[id]
	if !ok {
		return "", false
	}
	if c.IsValue || c.IsPath {
		return c.Value, true
	}
	// A tristate settles to its letter, so a rule can compare one against a
	// value elsewhere: an RTOS that spells a flag "y" and a header that spells
	// it 1 are a mismatch worth seeing rather than hiding.
	return strings.ToLower(c.Want.String()), true
}

// --- Kconfig -----------------------------------------------------------
//
// buildroot, linux, busybox, uboot and - when it arrives - zephyr are one
// interpreter with different configurations. They differ in where the tree is
// and what environment it is read with, not in what a symbol means.

type KconfigCfg struct {
	Stated Stated
	// Tree may be nil: a check run without --linux has claims about a tree it
	// was not given, and the honest answer to every question about it is that
	// it cannot be answered.
	Tree    *kconfig.Tree
	Version string

	// Decl is the tree's declaration, which carries the prefix convention: a
	// kernel symbol is declared EXT4_FS and written CONFIG_EXT4_FS, and
	// looking a stated name up verbatim reports every CONFIG_ symbol in the
	// library as missing.
	Decl lang.TreeDecl
	// ByName is what the composition asserts in this tree, keyed by bare
	// name, which is the shape dependency evaluation needs: a dependency is
	// refuted only when the composition contradicts it, never because a
	// symbol is unmentioned.
	ByName map[string]lang.Constraint
	// Unmanaged are the symbols this image makes no claim about.
	Unmanaged []lang.SymbolID
}

var Kconfig = plugin.Interp[KconfigCfg, KconfigCfg]{
	Open: func(c KconfigCfg) (KconfigCfg, error) { return c, nil },
	Answer: func(c KconfigCfg, k plan.Ask) plan.Answer {
		switch k.Op {
		case plan.Exists:
			if c.Tree == nil {
				return plan.NotAnswerable()
			}
			return plan.Yes(c.Tree.Symbols[verify.TreeName(c.Tree, c.Decl)(k.Name)] != nil)

		case plan.Holds:
			if c.Tree == nil {
				return plan.NotAnswerable()
			}
			cons, ok := k.Claim.(lang.Constraint)
			if !ok {
				return plan.NotAnswerable()
			}
			// The same function Check walks a composition with. Two code
			// paths deciding what counts as a problem would disagree within a
			// week, and the one a reader sees would depend on which command
			// they ran.
			fs := verify.CheckConstraint(cons, c.Tree, verify.TreeName(c.Tree, c.Decl),
				c.ByName, k.Tree)
			if len(fs) == 0 {
				return plan.OK()
			}
			msgs := make([]string, len(fs))
			for i, f := range fs {
				msgs[i] = f.Message
				if f.Detail != "" {
					msgs[i] += "\n    " + f.Detail
				}
			}
			return plan.Fail(errors.New(strings.Join(msgs, "\n  ")))

		case plan.Settled:
			id := lang.SymbolID{Tree: k.Tree, Name: k.Name}
			if v, ok := c.Stated.value(id); ok {
				return plan.Val(v)
			}
			if c.Tree == nil {
				return plan.NotAnswerable()
			}
			sym := c.Tree.Get(k.Name)
			if sym == nil {
				return plan.Fail(fmt.Errorf("%s is not a symbol in %s", k.Name, k.Tree))
			}
			// Stated by nothing and present in the tree: its value is
			// whatever completion works out, which is a fixpoint and not a
			// question this can answer. Saying so beats guessing a default
			// that selects and defaults may override.
			return plan.NotAnswerable()
		}
		return plan.NotAnswerable()
	},
}

// --- ESPHome -----------------------------------------------------------

type ESPHomeCfg struct{ Stated Stated }

var ESPHome = plugin.Interp[ESPHomeCfg, ESPHomeCfg]{
	Open: func(c ESPHomeCfg) (ESPHomeCfg, error) { return c, nil },
	Answer: func(c ESPHomeCfg, k plan.Ask) plan.Answer {
		if k.Op != plan.Settled {
			return plan.NotAnswerable()
		}
		if v, ok := c.Stated.value(lang.SymbolID{Tree: k.Tree, Name: k.Name}); ok {
			return plan.Val(v)
		}
		// An ESPHome document has no defaults silt models: a path nobody
		// states is a path the firmware will not carry.
		return plan.NotAnswerable()
	},
}

// --- Device tree -------------------------------------------------------
//
// The one kind whose answers come from a file rather than from the
// composition. A name here is a node path and a property - rpmsg@88000000.reg
// - which is how an address Linux is given can be compared with the address an
// RTOS was built for.

type DeviceTreeCfg struct {
	// Tree may be nil, as for Kconfig: no dtb given, nothing answerable.
	Tree *dt.Tree
}

var DeviceTree = plugin.Interp[DeviceTreeCfg, DeviceTreeCfg]{
	Open: func(c DeviceTreeCfg) (DeviceTreeCfg, error) { return c, nil },
	Answer: func(c DeviceTreeCfg, k plan.Ask) plan.Answer {
		if c.Tree == nil {
			return plan.NotAnswerable()
		}
		node, prop := splitProp(k.Name)
		n := findNode(c.Tree, node)

		switch k.Op {
		case plan.Exists:
			return plan.Yes(n != nil)
		case plan.Settled:
			if n == nil {
				return plan.Fail(fmt.Errorf("no node %s in the device tree", node))
			}
			if prop == "" {
				return plan.Fail(fmt.Errorf("%s names a node and not a property; "+
					"write %s.reg or %s.status", k.Name, node, node))
			}
			v, ok := property(n, prop)
			if !ok {
				return plan.Fail(fmt.Errorf("node %s has no property %s", node, prop))
			}
			return plan.Val(v)
		}
		return plan.NotAnswerable()
	},
}

// splitProp separates a node path from a property: the last dotted component
// is the property, and a name with no dot is a node.
func splitProp(name string) (node, prop string) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return name, ""
	}
	return name[:i], name[i+1:]
}

// findNode accepts a full path or a bare node name, because a rule is easier
// to read naming rpmsg@88000000 than /soc/rpmsg@88000000 and there is rarely
// more than one.
func findNode(t *dt.Tree, want string) *dt.Node {
	for _, n := range t.All {
		if n.Path == want || n.Name == want {
			return n
		}
	}
	if !strings.HasPrefix(want, "/") {
		for _, n := range t.All {
			if strings.HasSuffix(n.Path, "/"+want) {
				return n
			}
		}
	}
	return nil
}

// property renders a device tree property as a string a rule can compare.
//
// reg and other address properties are rendered as 0x-prefixed hex, because
// that is how they are written in the configurations on the other side of the
// comparison. A four-byte property is one cell; eight is two, and the first is
// the one an address rule means.
func property(n *dt.Node, prop string) (string, bool) {
	raw, ok := n.Props[prop]
	if !ok {
		return "", false
	}
	switch len(raw) {
	case 4:
		return "0x" + strconv.FormatUint(uint64(be32(raw)), 16), true
	case 8:
		// Two cells: an address in the first, a size in the second. A rule
		// about where something lives means the address.
		return "0x" + strconv.FormatUint(uint64(be32(raw[:4])), 16), true
	}
	s := strings.TrimRight(string(raw), "\x00")
	if s != "" {
		return s, true
	}
	return "", false
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// --- the registry ------------------------------------------------------

// Sources is what a run was given on the command line.
type Sources struct {
	Buildroot        *kconfig.Tree
	BuildrootVersion string
	Linux            *kconfig.Tree
	DeviceTree       *dt.Tree
}

// Registry binds every declared tree to something that can answer about it.
//
// A tree silt does not recognise gets no entry, and every question about it
// answers Unknown - which reads as "not checked" rather than as a failure, the
// same way a missing --linux always has.
func Registry(decls map[string]lang.TreeDecl, stated Stated, src Sources) plugin.Registry {
	reg := plugin.Registry{}
	for name, d := range decls {
		switch d.Kind {
		case lang.KindESPHome:
			reg[name] = plugin.Erase(ESPHome, ESPHomeCfg{Stated: stated})
		case lang.KindKconfig:
			cfg := KconfigCfg{Stated: stated}
			switch name {
			case "buildroot":
				cfg.Tree, cfg.Version = src.Buildroot, src.BuildrootVersion
			case "linux":
				cfg.Tree = src.Linux
			}
			o := plugin.Erase(Kconfig, cfg)
			o.Version = cfg.Version
			reg[name] = o
		}
	}
	// Not a declared tree: device tree facts come from a file rather than
	// from the library, so the name is reserved rather than registered.
	if src.DeviceTree != nil {
		reg["devicetree"] = plugin.Erase(DeviceTree, DeviceTreeCfg{Tree: src.DeviceTree})
	}
	return reg
}

// SameValues builds the plan for a library's same-value rules.
func SameValues(rules []*lang.Rules) plan.Plan[plan.V[plan.Unit]] {
	var all []lang.SameValue
	for _, r := range rules {
		all = append(all, r.Same...)
	}
	return plan.Map(
		plan.Traverse(all, func(sv lang.SameValue) plan.Plan[plan.V[plan.Unit]] {
			a := plan.Ask{Tree: sv.A.Tree, Name: sv.A.Name, Pos: sv.Pos.Short(), Hint: sv.From}
			b := plan.Ask{Tree: sv.B.Tree, Name: sv.B.Name, Pos: sv.Pos.Short()}
			return plan.SameValue(a, b, sv.Why)
		}),
		func(vs []plan.V[plan.Unit]) plan.V[plan.Unit] {
			// Every rule's problems, not the first rule's: a board with two
			// mismatched addresses should show both.
			all := plan.All(vs)
			return plan.V[plan.Unit]{Problems: all.Problems}
		})
}

// CheckImage is a composition's claims as a plan.
//
// The same questions verify.Check asks when it walks a result, asked one at a
// time so they can be folded with everything else a run wants to know. What
// this buys over calling Check directly is not the checking - that is the same
// function either way - but that the questions are knowable before any tree is
// opened, so one Buildroot tree answers for every image in a run, and that the
// answers fold into the same report as the cross-tree rules rather than into a
// second one that can disagree.
func CheckImage(res *compose.Result, trees map[string]lang.TreeDecl) plan.Plan[plan.V[plan.Unit]] {
	var claims []lang.Constraint
	for sc, cs := range res.Constraints {
		d, ok := trees[string(sc)]
		if !ok || d.Kind != lang.KindKconfig {
			continue
		}
		for _, c := range cs {
			if c.Soft || unmanaged(c.Sym, res.Unmanaged) {
				continue
			}
			claims = append(claims, c)
		}
	}
	// Deterministic, so a plan is diffable and its hash is stable.
	sort.Slice(claims, func(i, j int) bool {
		if claims[i].Sym.Tree != claims[j].Sym.Tree {
			return claims[i].Sym.Tree < claims[j].Sym.Tree
		}
		return claims[i].Sym.Name < claims[j].Sym.Name
	})

	return plan.Map(
		plan.Traverse(claims, func(c lang.Constraint) plan.Plan[plan.V[plan.Unit]] {
			return plan.Claim(plan.Ask{
				Op: plan.Holds, Tree: c.Sym.Tree, Name: c.Sym.Name,
				Claim: c, Pos: c.Pos.Short(), Hint: "stated by " + c.From,
			})
		}),
		func(vs []plan.V[plan.Unit]) plan.V[plan.Unit] {
			return plan.V[plan.Unit]{Problems: plan.All(vs).Problems}
		})
}

func unmanaged(id lang.SymbolID, ids []lang.SymbolID) bool {
	for _, u := range ids {
		if u.Tree != id.Tree {
			continue
		}
		if u.Name == id.Name {
			return true
		}
		if strings.HasSuffix(u.Name, "*") && strings.HasPrefix(id.Name, strings.TrimSuffix(u.Name, "*")) {
			return true
		}
	}
	return false
}

// ForImage builds a registry that can answer about one composition: the same
// trees, with that image's stated symbols indexed the way dependency
// evaluation needs them.
func ForImage(res *compose.Result, trees map[string]lang.TreeDecl, src Sources) plugin.Registry {
	reg := Registry(trees, Index(res.Constraints), src)
	for name, d := range trees {
		if d.Kind != lang.KindKconfig {
			continue
		}
		cfg := KconfigCfg{
			Stated:    Index(res.Constraints),
			Decl:      d,
			ByName:    verify.StatedIn(res, lang.Scope(name)),
			Unmanaged: res.Unmanaged,
		}
		switch name {
		case "buildroot":
			cfg.Tree, cfg.Version = src.Buildroot, src.BuildrootVersion
		case "linux":
			cfg.Tree = src.Linux
		}
		o := plugin.Erase(Kconfig, cfg)
		o.Version = cfg.Version
		reg[name] = o
	}
	return reg
}
