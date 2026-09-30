// Package dt reads flattened device trees: the .dtb files a kernel build
// produces and the firmware hands to the kernel at boot.
//
// It exists because a device tree is the third configuration in an embedded
// Linux image, and the one nothing checks. Buildroot's .config says what is
// built; the kernel's .config says what is compiled in; the device tree says
// what hardware exists and whether it is turned on. A driver compiled in with
// no node to bind to is dead weight, and a node enabled with no driver is
// hardware that silently does not work - and neither Kconfig knows anything
// about the other side.
//
// The flattened format rather than the source: a .dts runs through the C
// preprocessor, pulls in .dtsi includes and uses macros from the kernel's
// headers, so reading one properly means reproducing a piece of the kernel
// build. A .dtb is the output of all that, it is what actually ships on the
// card, and its format is a header and three blocks.
package dt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
)

const (
	magic = 0xd00dfeed

	tokenBeginNode = 0x1
	tokenEndNode   = 0x2
	tokenProp      = 0x3
	tokenNop       = 0x4
	tokenEnd       = 0x9
)

// Node is one node of the tree, with its properties and children.
type Node struct {
	Name     string
	Path     string
	Props    map[string][]byte
	Children []*Node
	Parent   *Node
}

// Tree is a parsed device tree.
type Tree struct {
	Root *Node
	// Nodes in the order they appear, which is the order the kernel walks
	// them. Every node including the root.
	All []*Node
}

type header struct {
	Magic           uint32
	TotalSize       uint32
	OffDtStruct     uint32
	OffDtStrings    uint32
	OffMemRsvmap    uint32
	Version         uint32
	LastCompVersion uint32
	BootCPUIDPhys   uint32
	SizeDtStrings   uint32
	SizeDtStruct    uint32
}

// ReadFile parses a .dtb.
func ReadFile(path string) (*Tree, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads a flattened device tree from memory.
func Parse(b []byte) (*Tree, error) {
	if len(b) < 40 {
		return nil, fmt.Errorf("not a device tree: %d bytes", len(b))
	}
	var h header
	if err := binary.Read(bytes.NewReader(b[:40]), binary.BigEndian, &h); err != nil {
		return nil, err
	}
	if h.Magic != magic {
		return nil, fmt.Errorf("not a device tree: magic 0x%08x, want 0x%08x", h.Magic, magic)
	}
	if int(h.OffDtStruct+h.SizeDtStruct) > len(b) || int(h.OffDtStrings+h.SizeDtStrings) > len(b) {
		return nil, fmt.Errorf("device tree truncated: header says %d bytes, file is %d", h.TotalSize, len(b))
	}

	strs := b[h.OffDtStrings : h.OffDtStrings+h.SizeDtStrings]
	st := b[h.OffDtStruct : h.OffDtStruct+h.SizeDtStruct]

	t := &Tree{}
	var stack []*Node
	i := 0
	read32 := func() uint32 {
		v := binary.BigEndian.Uint32(st[i : i+4])
		i += 4
		return v
	}

	for i+4 <= len(st) {
		switch read32() {
		case tokenNop:
			continue

		case tokenBeginNode:
			// A NUL-terminated name, padded to four bytes.
			end := bytes.IndexByte(st[i:], 0)
			if end < 0 {
				return nil, fmt.Errorf("unterminated node name at %d", i)
			}
			name := string(st[i : i+end])
			i += end + 1
			i = (i + 3) &^ 3

			n := &Node{Name: name, Props: map[string][]byte{}}
			if len(stack) == 0 {
				n.Path = "/"
				t.Root = n
			} else {
				p := stack[len(stack)-1]
				n.Parent = p
				if p.Path == "/" {
					n.Path = "/" + name
				} else {
					n.Path = p.Path + "/" + name
				}
				p.Children = append(p.Children, n)
			}
			stack = append(stack, n)
			t.All = append(t.All, n)

		case tokenEndNode:
			if len(stack) == 0 {
				return nil, fmt.Errorf("END_NODE with no open node at %d", i)
			}
			stack = stack[:len(stack)-1]

		case tokenProp:
			if i+8 > len(st) {
				return nil, fmt.Errorf("truncated property header at %d", i)
			}
			size := read32()
			nameOff := read32()
			if int(nameOff) >= len(strs) {
				return nil, fmt.Errorf("property name offset %d past the strings block", nameOff)
			}
			end := bytes.IndexByte(strs[nameOff:], 0)
			name := string(strs[nameOff : int(nameOff)+end])
			if i+int(size) > len(st) {
				return nil, fmt.Errorf("property %q claims %d bytes, %d left", name, size, len(st)-i)
			}
			val := st[i : i+int(size)]
			i += int(size)
			i = (i + 3) &^ 3
			if len(stack) == 0 {
				return nil, fmt.Errorf("property %q outside any node", name)
			}
			stack[len(stack)-1].Props[name] = val

		case tokenEnd:
			if t.Root == nil {
				return nil, fmt.Errorf("device tree has no root node")
			}
			return t, nil

		default:
			return nil, fmt.Errorf("unknown token at %d", i-4)
		}
	}
	return nil, fmt.Errorf("device tree ended without an END token")
}

// String reads a property as a NUL-terminated string.
func (n *Node) String(prop string) string {
	v, ok := n.Props[prop]
	if !ok {
		return ""
	}
	return string(bytes.TrimRight(v, "\x00"))
}

// Strings reads a property as a list of NUL-terminated strings, which is how
// compatible is stored: "raspberrypi,5-model-b\0brcm,bcm2712\0".
func (n *Node) Strings(prop string) []string {
	v, ok := n.Props[prop]
	if !ok {
		return nil
	}
	var out []string
	for _, s := range bytes.Split(bytes.TrimRight(v, "\x00"), []byte{0}) {
		if len(s) > 0 {
			out = append(out, string(s))
		}
	}
	return out
}

// Compatible is the node's compatible strings, most specific first. This is
// what binds a node to a driver: the kernel walks the tree and, for each node,
// looks for a driver whose of_match_table names one of these.
func (n *Node) Compatible() []string { return n.Strings("compatible") }

// Enabled reports whether the kernel will try to bind a driver to this node.
//
// A node with no status is enabled: absence means okay, which is the opposite
// of how it reads. "disabled" is the mechanism for "this hardware exists and we
// do not want it brought up" - the way a board with a serial console it does
// not want turns the console off, which is a security property living in the
// device tree rather than in any Kconfig.
func (n *Node) Enabled() bool {
	switch n.String("status") {
	case "", "okay", "ok":
		return true
	default:
		return false
	}
}

// Label returns the node's name without its unit address: "serial@7d001000"
// is the node "serial" at address 7d001000, and the part before the @ is what
// a binding document calls it.
func (n *Node) Label() string {
	if i := strings.IndexByte(n.Name, '@'); i > 0 {
		return n.Name[:i]
	}
	return n.Name
}

// Model is the board's model string, from the root node.
func (t *Tree) Model() string { return t.Root.String("model") }

// Devices returns every node that names a compatible string, which is every
// node describing a piece of hardware. Nodes without one are structural: buses,
// containers, the chosen and aliases nodes.
func (t *Tree) Devices() []*Node {
	var out []*Node
	for _, n := range t.All {
		if len(n.Compatible()) > 0 {
			out = append(out, n)
		}
	}
	return out
}
