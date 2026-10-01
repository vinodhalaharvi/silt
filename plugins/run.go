package plugins

import (
	"fmt"
	"sort"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/dt"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugin"
	"github.com/vinodhalaharvi/silt/verify"
)

// Running a composition's claims, one kind at a time.
//
// Each kind's plan is built and run against its own interpreter, and the
// results meet as plan.V - which is one type. That is the shape Go's type
// system wants and, as it turns out, the shape the problem wants: questions of
// different kinds have nothing to say to each other, and answers do.

// Sources is what a run was given.
type Sources struct {
	Buildroot        *kconfig.Tree
	BuildrootVersion string
	Linux            *kconfig.Tree
	DeviceTree       *dt.Tree
}

// NewRunner binds every kind that this run can answer for. A kind with no
// source is left nil, and every question about it answers "cannot say".
func NewRunner(res *compose.Result, trees map[string]lang.TreeDecl, src Sources) Runner {
	var r Runner

	bind := func(name string, tree *kconfig.Tree, version string) *plugin.Bound[KconfigClaim, KconfigCfg, kconfigLoaded] {
		d, ok := trees[name]
		if !ok || d.Kind != lang.KindKconfig {
			return nil
		}
		b := plugin.Bind(Kconfig, KconfigCfg{
			Tree:    tree,
			Decl:    d,
			Version: version,
			Stated:  verify.StatedIn(res, lang.Scope(name)),
			Scope:   name,
		})
		b.Version = version
		return b
	}

	r.Buildroot = bind("buildroot", src.Buildroot, src.BuildrootVersion)
	r.Linux = bind("linux", src.Linux, "")

	for name, d := range trees {
		if d.Kind != lang.KindESPHome {
			continue
		}
		stated := map[string]string{}
		for _, c := range res.Constraints[lang.Scope(name)] {
			stated[c.Sym.Name] = constraintValue(c)
		}
		r.ESPHome = plugin.Bind(ESPHome, ESPHomeCfg{Stated: stated})
	}

	if src.DeviceTree != nil {
		r.DeviceTree = plugin.Bind(DeviceTree, DeviceTreeCfg{Tree: src.DeviceTree})
	}
	return r
}

// KconfigClaims is one Kconfig tree's claims, as a plan.
func KconfigClaims(res *compose.Result, tree string, trees map[string]lang.TreeDecl) plan.Plan[KconfigClaim, plan.V[plan.Unit]] {
	d, ok := trees[tree]
	if !ok || d.Kind != lang.KindKconfig {
		return plan.Pure[KconfigClaim](plan.Good(plan.Unit{}))
	}

	// Keyed by symbol, because verify indexes what the composition asserts by
	// name and a symbol stated twice is checked once. Counting it twice would
	// make "31 buildroot symbols checked" mean something it never meant.
	byName := map[string]lang.Constraint{}
	for _, c := range res.Constraints[lang.Scope(tree)] {
		if !c.Soft {
			byName[c.Sym.Name] = c
		}
	}
	// Opaque and environment symbols are stated too, and verify checks them:
	// a value silt passes through without modelling still has to name a
	// symbol that exists.
	for _, cs := range [][]lang.Constraint{res.Opaque, res.Environment} {
		for _, c := range cs {
			if c.Sym.Tree == tree {
				byName[c.Sym.Name] = c
			}
		}
	}

	var claims []lang.Constraint
	for _, c := range byName {
		if unmanaged(c.Sym, res.Unmanaged) {
			continue
		}
		claims = append(claims, c)
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].Sym.Name < claims[j].Sym.Name })

	return plan.Map(
		plan.Traverse(claims, func(c lang.Constraint) plan.Plan[KconfigClaim, plan.V[plan.Unit]] {
			return plan.Claim(plan.Ask[KconfigClaim]{
				Op: plan.Holds, Tree: tree, Name: c.Sym.Name,
				Claim: KconfigClaim{Constraint: c},
				Pos:   c.Pos.Short(), Hint: "stated by " + c.From,
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
		if n := len(u.Name); n > 0 && u.Name[n-1] == '*' &&
			len(id.Name) >= n-1 && id.Name[:n-1] == u.Name[:n-1] {
			return true
		}
	}
	return false
}

// Report runs an image's claims across every kind and returns what
// verify.Check would have returned: the findings, and how many symbols were
// checked in each tree.
//
// Each kind runs separately and typed; they meet as findings. The output of
// silt check is unchanged by this - deliberately, because a refactor that also
// changes what the reader sees cannot be verified against what it replaces.
func Report(res *compose.Result, trees map[string]lang.TreeDecl, r Runner) ([]verify.Finding, map[string]int, error) {
	checked := map[string]int{}
	var problems []plan.Problem

	for _, tree := range kconfigTrees(trees) {
		p := KconfigClaims(res, tree, trees)
		checked[tree] = len(p.Asks())
		v, err := plugin.Run(p, r.Kconfig(tree))
		if err != nil {
			return nil, nil, err
		}
		problems = append(problems, v.Problems...)
	}

	var out []verify.Finding
	for _, pr := range problems {
		if fs, ok := AsFindings(pr.Err); ok {
			out = append(out, fs...)
			continue
		}
		out = append(out, verify.Finding{Pos: pr.Pos, Symbol: pr.Name, Message: pr.Err.Error()})
	}
	return out, checked, nil
}

func kconfigTrees(trees map[string]lang.TreeDecl) []string {
	var out []string
	for name, d := range trees {
		if d.Kind == lang.KindKconfig {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// CheckSameValues answers a library's same-value rules.
//
// The one check that cannot be a single plan: its two sides are questions of
// two different kinds, so they have two different types and Go has no list
// that holds both. That is the design rather than a limitation - each side is
// asked by its own kind's runner, and the two meet as answers.
func CheckSameValues(rules []*lang.Rules, r Runner) (plan.V[plan.Unit], error) {
	out := plan.V[plan.Unit]{}
	for _, rs := range rules {
		for _, sv := range rs.Same {
			a, err := r.Settled(sv.A)
			if err != nil {
				return out, err
			}
			b, err := r.Settled(sv.B)
			if err != nil {
				return out, err
			}
			v := plan.Compare(a, b,
				fmt.Sprintf("%s is %s and %s is", sv.A, "%s", sv.B), sv.Why)
			for i := range v.Problems {
				v.Problems[i].Pos = sv.Pos.Short()
				v.Problems[i].Hint = sv.From
			}
			out.Problems = append(out.Problems, v.Problems...)
		}
	}
	return out, nil
}

// Settled asks one kind what a name ends up as, choosing the kind by the
// symbol's tree. A tree this run has no source for answers "cannot say", which
// is why a rule is not a failure when half of it was never given.
func (r Runner) Settled(id lang.SymbolID) (plan.Answer, error) {
	switch {
	case r.Kconfig(id.Tree) != nil:
		return plugin.Run(
			plan.Lift(plan.Ask[KconfigClaim]{Op: plan.Settled, Tree: id.Tree, Name: id.Name},
				func(a plan.Answer) plan.Answer { return a }),
			r.Kconfig(id.Tree))

	case id.Tree == "esphome":
		return plugin.Run(
			plan.Lift(plan.Ask[PathClaim]{Op: plan.Settled, Tree: id.Tree, Name: id.Name},
				func(a plan.Answer) plan.Answer { return a }),
			r.ESPHome)

	case id.Tree == "devicetree":
		return plugin.Run(
			plan.Lift(plan.Ask[NodeClaim]{Op: plan.Settled, Tree: id.Tree, Name: id.Name},
				func(a plan.Answer) plan.Answer { return a }),
			r.DeviceTree)
	}
	return plan.NotAnswerable(), nil
}
