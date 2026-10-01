package plugins

import (
	"errors"
	"strings"

	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugin"
	"github.com/vinodhalaharvi/silt/verify"
)

// The Kconfig kind.
//
// buildroot, linux, busybox, uboot and - when it arrives - zephyr are one
// interpreter with different configurations. They differ in where the tree is
// read from and what consumes its output, not in what a symbol means, which is
// why a second Kconfig-shaped system costs an Open and an Emit and reuses
// everything below unchanged.

type KconfigCfg struct {
	// Tree may be nil: a check run without --linux has claims about a tree it
	// was not given, and the honest answer to every question is that it
	// cannot be answered.
	Tree    *kconfig.Tree
	Decl    lang.TreeDecl
	Version string

	// Stated is what the composition asserts in this tree, by bare name,
	// which is the shape dependency evaluation needs: a dependency is refuted
	// only when the composition contradicts it, never because a symbol is
	// unmentioned.
	Stated map[string]lang.Constraint
	// Scope is the tree's name, for the message when a symbol is missing.
	Scope string
}

var Kconfig = plugin.Interp[KconfigClaim, KconfigCfg, kconfigLoaded]{
	Open: func(c KconfigCfg) (kconfigLoaded, error) {
		l := kconfigLoaded{Tree: c.Tree, Cfg: c}
		if c.Tree != nil {
			l.Naming = verify.TreeName(c.Tree, c.Decl)
		}
		return l, nil
	},

	Answer: func(l kconfigLoaded, k plan.Ask[KconfigClaim]) plan.Answer {
		switch k.Op {
		case plan.Exists:
			if l.Tree == nil {
				return plan.NotAnswerable()
			}
			return plan.Yes(l.Tree.Symbols[l.Naming(k.Name)] != nil)

		case plan.Holds:
			if l.Tree == nil {
				return plan.NotAnswerable()
			}
			// The same function verify.Check walks a composition with. Two
			// code paths deciding what counts as a problem would disagree
			// within a week, and which answer a reader saw would depend on
			// which command they ran.
			fs := verify.CheckConstraint(k.Claim.Constraint, l.Tree, l.Naming,
				l.Cfg.Stated, l.Cfg.Scope)
			if len(fs) == 0 {
				return plan.OK()
			}
			return plan.Fail(Findings(fs))

		case plan.Settled:
			if c, ok := l.Cfg.Stated[k.Name]; ok {
				return settled(constraintValue(c), true)
			}
			// Stated by nothing: its value is whatever completion works out,
			// which is a fixpoint and not a question this can answer. Saying
			// so beats guessing a default that selects may override.
			return plan.NotAnswerable()
		}
		return plan.NotAnswerable()
	},

	// Which settings change what a build produces. The store's prefix test
	// turns on this: a setting read after every package is built cannot have
	// affected a compiled package.
	Affects: func(name string) bool {
		for _, p := range readAfterBuilding {
			if name == p {
				return false
			}
		}
		return !strings.HasPrefix(name, "BR2_EXTERNAL_")
	},
}

// constraintValue is what a claim settles to: its value, or a tristate's
// letter - so a flag spelled "y" in one tree and 1 in another is a comparison
// that can be made at all.
func constraintValue(c lang.Constraint) string {
	if c.IsValue || c.IsPath {
		return c.Value
	}
	return strings.ToLower(c.Want.String())
}

// Findings is verify's findings carried as an error, so a plan's failure can
// be unwrapped back into exactly what Check produced - a caller printing a
// report wants the position, the symbol and the detail as they were, and
// re-deriving those from a string is how two commands come to print one
// problem two ways.
type Findings []verify.Finding

func (f Findings) Error() string {
	msgs := make([]string, len(f))
	for i, x := range f {
		msgs[i] = x.Message
		if x.Detail != "" {
			msgs[i] += "\n    " + x.Detail
		}
	}
	return strings.Join(msgs, "\n  ")
}

// AsFindings recovers them from a problem's error.
func AsFindings(err error) (Findings, bool) {
	var f Findings
	ok := errors.As(err, &f)
	return f, ok
}

// Symbols read after every package is built, by steps that re-run on every
// make. A changed overlay cannot have affected a compiled package, because
// nothing reads it until target-finalize.
var readAfterBuilding = []string{
	"BR2_ROOTFS_OVERLAY",
	"BR2_ROOTFS_POST_BUILD_SCRIPT",
	"BR2_ROOTFS_POST_IMAGE_SCRIPT",
	"BR2_ROOTFS_POST_SCRIPT_ARGS",
	"BR2_TARGET_ROOTFS_EXT2_SIZE",
	"BR2_TARGET_ROOTFS_TAR",
	"BR2_PACKAGE_RPI_FIRMWARE_CONFIG_FILE",
	"BR2_PACKAGE_RPI_FIRMWARE_CMDLINE_FILE",

	// Filled in from the invocation rather than from the configuration.
	// BR2_EXTERNAL_<NAME>_VERSION is git describe of the external tree, so it
	// changes on every commit - and before it was excluded, every stored tree
	// was disqualified the moment anything was committed, and a one-package
	// image took twenty-two minutes instead of one.
	"BR2_DEFCONFIG",
	"BR2_LINUX_KERNEL_CONFIG_FRAGMENT_FILES",
	"BR2_DL_DIR",
	"BR2_CCACHE_DIR",
	"BR2_JLEVEL",
}
