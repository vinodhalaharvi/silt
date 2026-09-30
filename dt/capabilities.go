package dt

import (
	"fmt"
	"sort"
	"strings"
)

// Capability derivation: what a board has, read from the device tree rather
// than asserted by whoever wrote the fragment.
//
// A capability in silt is a hardware fact no Kconfig symbol can express -
// whether an RS485 transceiver is fitted, whether the second Ethernet is
// populated, whether there is a modem slot. Today those are written by hand,
// and two of the CM5 carriers here carry capabilities taken from a product page
// and marked UNVERIFIED, which is exactly the kind of claim that is wrong for a
// year before anyone notices.
//
// The device tree knows. It is the board vendor's own description of what is
// wired to what, it ships with the kernel, and it is the thing the kernel
// itself believes.
//
// What this cannot do is see a transceiver that the device tree does not
// mention. A board with an RS485 chip nobody wrote a node for looks, from here,
// like a board without one. So this reports and explains rather than generating
// a fragment to paste in unread: the output is an argument, and a person still
// decides.

// Rule is one way a capability can be recognised in a tree.
type Rule struct {
	Capability string
	Why        string
	// Match reports whether this node demonstrates the capability.
	Match func(n *Node) bool
}

// hasCompatiblePrefix is the common shape: a node whose compatible strings
// include one containing a substring. Substring rather than exact, because a
// vendor ships twenty part numbers for one function and the family name is in
// all of them.
func hasCompatible(subs ...string) func(*Node) bool {
	return func(n *Node) bool {
		for _, c := range n.Compatible() {
			lc := strings.ToLower(c)
			for _, s := range subs {
				if strings.Contains(lc, s) {
					return true
				}
			}
		}
		return false
	}
}

func nodeNamed(names ...string) func(*Node) bool {
	return func(n *Node) bool {
		l := n.Label()
		for _, s := range names {
			if l == s {
				return true
			}
		}
		return false
	}
}

func both(a, b func(*Node) bool) func(*Node) bool {
	return func(n *Node) bool { return a(n) && b(n) }
}

func hasProp(props ...string) func(*Node) bool {
	return func(n *Node) bool {
		for _, p := range props {
			if _, ok := n.Props[p]; ok {
				return true
			}
		}
		return false
	}
}

// Rules is the table, and it is the content of this file: parsing a tree is
// mechanical, deciding what a node means is not. Each entry says why it is
// here, because a rule without a reason is a rule nobody can correct.
var Rules = []Rule{
	{
		Capability: "can-bus",
		Why:        "a can@ node, or a CAN controller by compatible string",
		Match: func(n *Node) bool {
			return nodeNamed("can")(n) ||
				hasCompatible("mcp2515", "mcp251x", "mcp2518", "-can", "can-controller")(n)
		},
	},
	{
		Capability: "rs485",
		// The property rather than the chip: a plain UART becomes RS485 when a
		// transceiver is wired to its direction pin, and that is what these
		// properties record. A sc16is7xx expander with no rs485 property is a
		// board with four extra UARTs and no transceivers, which is a real
		// configuration and not this capability.
		Why:   "a uart with rs485 direction control described",
		Match: hasProp("rs485-rts-active-low", "rs485-rts-active-high", "linux,rs485-enabled-at-boot-time", "rs485-rx-during-tx"),
	},
	{
		Capability: "nvme",
		// A PCIe root port is not an NVMe drive, and the tree cannot know
		// whether one is plugged in. What it tells you is that the slot exists
		// and is enabled, which is what the capability is for: a feature that
		// wants NVMe can be composed onto this board.
		Why:   "an enabled pcie root port, which is where an M.2 drive would go",
		Match: both(nodeNamed("pcie", "pcie-ep"), func(n *Node) bool { return n.Enabled() }),
	},
	{
		Capability: "sdcard",
		Why:        "an mmc or sdhci controller",
		Match: func(n *Node) bool {
			return nodeNamed("mmc", "sdhci", "mmc-slot")(n) ||
				hasCompatible("sdhci", "mmc-host", "dw-mshc")(n)
		},
	},
	{
		Capability: "csi-camera",
		Why:        "a csi receiver node",
		Match: func(n *Node) bool {
			return nodeNamed("csi", "csi2", "mipi-csi", "camera")(n) ||
				hasCompatible("csi2", "mipi-csi", "-csi")(n)
		},
	},
	{
		Capability: "serial-console",
		// Any enabled UART. Whether the console is on it is a cmdline
		// question, not a device tree one, so this is the weaker claim: there
		// is a serial port to put one on.
		Why:   "an enabled uart",
		Match: hasCompatible("uart", "pl011", "8250", "ns16550", "sc16is"),
	},
	{
		Capability: "wifi-sdio",
		Why:        "a wifi part on an mmc bus, or a brcmfmac-style node",
		Match:      hasCompatible("brcmf", "bcm4329", "bcm4330", "bcm43", "mwifiex", "esp,esp32"),
	},
	{
		Capability: "cellular",
		// Rarely in the tree at all: a modem on USB appears only if someone
		// described the port, and a modem on M.2 usually does not appear. So
		// absence here is weak evidence and the report says so.
		Why:   "a modem node, or a usb port described as one",
		Match: hasCompatible("quectel", "sierra,", "telit", "u-blox,", "simcom", "cinterion"),
	},
}

// Ethernet is counted rather than matched, because the capability is about how
// many there are rather than whether there is one.
var ethernetCompatibles = []string{"ethernet", "-gmac", "eqos", "stmmac", "macb", "fec", "-emac", "genet", "eth-"}

// Finding is one capability the tree demonstrates, with the nodes that show it.
type Finding struct {
	Capability string
	Why        string
	Nodes      []string
}

// enabledHere reports whether this node and every node above it is enabled.
// A child of a disabled controller is not reachable however its own status
// reads, which is the tree's way of turning off a whole subtree at its root.
func enabledHere(n *Node) bool {
	for p := n; p != nil; p = p.Parent {
		if !p.Enabled() {
			return false
		}
	}
	return true
}

// Derive reads a tree and reports the capabilities it demonstrates.
//
// Only enabled nodes count, and a node under a disabled parent does not count
// either. A node with status = "disabled" describes hardware that exists and is
// deliberately not brought up - the board saying "not this one" - and taking it
// as a capability would be reading past the board's own statement.
//
// Every node is considered, not only the ones with a compatible string. The
// rs485 rule is why: the property that says a transceiver is wired sits on the
// port, and a port of a four-port expander is a child node with no compatible
// of its own. Looking only at devices missed it, on the fixture written to
// catch exactly that.
func Derive(t *Tree) []Finding {
	found := map[string]*Finding{}
	add := func(cap, why, path string) {
		f := found[cap]
		if f == nil {
			f = &Finding{Capability: cap, Why: why}
			found[cap] = f
		}
		f.Nodes = append(f.Nodes, path)
	}

	eth := 0
	for _, n := range t.All {
		if !enabledHere(n) {
			continue
		}
		for _, r := range Rules {
			if r.Match(n) {
				add(r.Capability, r.Why, n.Path)
			}
		}
		if nodeNamed("ethernet", "eth", "mac")(n) || hasCompatible(ethernetCompatibles...)(n) {
			eth++
		}
	}
	if eth >= 2 {
		add("dual-ethernet", fmt.Sprintf("%d enabled ethernet controllers", eth), "")
	}

	var out []Finding
	for _, f := range found {
		sort.Strings(f.Nodes)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
	return out
}

// NotFound lists the capabilities this package knows how to look for and did
// not find, which is as much of the answer as the list that was found: a board
// claiming rs485 whose tree describes none is the case worth catching.
func NotFound(found []Finding) []string {
	have := map[string]bool{}
	for _, f := range found {
		have[f.Capability] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range Rules {
		if !have[r.Capability] && !seen[r.Capability] {
			seen[r.Capability] = true
			out = append(out, r.Capability)
		}
	}
	if !have["dual-ethernet"] {
		out = append(out, "dual-ethernet")
	}
	sort.Strings(out)
	return out
}
