package plugins

import (
	"github.com/vinodhalaharvi/silt/dt"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugin"
)

// The claim types, one per kind.
//
// A claim is what a question carries, and it differs by kind: a Kconfig claim
// is a constraint over a symbol, an ESPHome claim is a path and a value, a
// device tree claim is a node and a property. They are separate types so a
// plan built for one kind cannot be run against another's interpreter, and so
// no interpreter contains an assertion.

// KconfigClaim is a constraint over a Kconfig symbol. Buildroot, linux,
// busybox, uboot and zephyr all use it: they differ in where the tree is read
// from and what consumes its output, not in what a symbol means.
type KconfigClaim struct {
	Constraint lang.Constraint
}

// PathClaim is a setting in a structured document: mqtt.topic_prefix, and the
// value it takes.
type PathClaim struct {
	Path  string
	Value string
}

// NodeClaim is a device tree node and one of its properties.
type NodeClaim struct {
	Node     string
	Property string
}

// Runner is every kind, bound to its configuration.
//
// A struct rather than a map, and one field per kind rather than an interface:
// the set of kinds is closed and deliberate, so a kind added without being
// handled here should be a compile error rather than a tree whose questions
// nobody answers. Twenty fields would be fine; what matters is that it is
// closed.
type Runner struct {
	Buildroot  *plugin.Bound[KconfigClaim, KconfigCfg, kconfigLoaded]
	Linux      *plugin.Bound[KconfigClaim, KconfigCfg, kconfigLoaded]
	ESPHome    *plugin.Bound[PathClaim, ESPHomeCfg, ESPHomeCfg]
	DeviceTree *plugin.Bound[NodeClaim, DeviceTreeCfg, DeviceTreeCfg]
}

// Kconfig returns the runner for a named Kconfig tree, or nil when this run
// was not given one - which is how "43 linux symbols not checked" happens.
func (r Runner) Kconfig(tree string) *plugin.Bound[KconfigClaim, KconfigCfg, kconfigLoaded] {
	switch tree {
	case "buildroot":
		return r.Buildroot
	case "linux":
		return r.Linux
	}
	return nil
}

// kconfigLoaded is what opening a Kconfig tree produces. It carries the
// composition alongside the tree because answering a claim needs both: whether
// a dependency is contradicted is a question about what else was stated.
type kconfigLoaded struct {
	Tree   *kconfig.Tree
	Cfg    KconfigCfg
	Naming func(string) string
}

// settled is the one answer shape every kind shares, which is what lets a fact
// stated in two trees be compared without either knowing the other.
func settled(v string, ok bool) plan.Answer {
	if !ok {
		return plan.NotAnswerable()
	}
	return plan.Val(v)
}

var _ = dt.ReadFile // the device tree kind is in devicetree.go
