package plugins

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vinodhalaharvi/silt/dt"
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugin"
)

// The device tree kind.
//
// The one kind whose answers come from a file rather than from the
// composition: a name is a node and a property, and the value is whatever the
// board's own description says. That is what makes it the other half of a
// cross-tree rule - an address Linux is given, compared against the address an
// RTOS was built for.
//
// No Emit: the kernel builds a device tree, and silt states claims about it
// rather than producing it.

type DeviceTreeCfg struct {
	// Tree may be nil, as for Kconfig: no dtb given, nothing answerable.
	Tree *dt.Tree
}

var DeviceTree = plugin.Interp[NodeClaim, DeviceTreeCfg, DeviceTreeCfg]{
	Open: func(c DeviceTreeCfg) (DeviceTreeCfg, error) { return c, nil },

	Answer: func(c DeviceTreeCfg, k plan.Ask[NodeClaim]) plan.Answer {
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

// findNode accepts a full path or a bare node name, because a rule reads
// better naming rpmsg@88000000 than /soc/rpmsg@88000000 and there is rarely
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

// property renders a property as a string a rule can compare.
//
// An address is rendered as 0x-prefixed hex, because that is how it is written
// in the configurations on the other side of the comparison. Two cells is an
// address and a size, and a rule about where something lives means the first.
func property(n *dt.Node, prop string) (string, bool) {
	raw, ok := n.Props[prop]
	if !ok {
		return "", false
	}
	switch len(raw) {
	case 4:
		return "0x" + strconv.FormatUint(uint64(be32(raw)), 16), true
	case 8:
		return "0x" + strconv.FormatUint(uint64(be32(raw[:4])), 16), true
	}
	if s := strings.TrimRight(string(raw), "\x00"); s != "" {
		return s, true
	}
	return "", false
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
