package dt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func carrier(t *testing.T) *Tree {
	t.Helper()
	dtb := filepath.Join("testdata", "carrier.dtb")
	if dtc, err := exec.LookPath("dtc"); err == nil {
		out, err := exec.Command(dtc, "-I", "dts", "-O", "dtb", "-o", dtb,
			filepath.Join("testdata", "carrier.dts")).CombinedOutput()
		if err != nil {
			t.Fatalf("dtc: %v\n%s", err, out)
		}
	}
	if _, err := os.Stat(dtb); err != nil {
		t.Skip("no fixture and no dtc to build one")
	}
	tree, err := ReadFile(dtb)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func derived(t *testing.T, tree *Tree) map[string][]string {
	t.Helper()
	m := map[string][]string{}
	for _, f := range Derive(tree) {
		m[f.Capability] = f.Nodes
	}
	return m
}

func TestDeriveFindsWhatTheTreeDescribes(t *testing.T) {
	got := derived(t, carrier(t))

	for _, c := range []string{"can-bus", "nvme", "sdcard", "rs485", "dual-ethernet", "serial-console"} {
		if _, ok := got[c]; !ok {
			t.Errorf("did not find %s", c)
		}
	}
	if len(got["can-bus"]) != 1 || got["can-bus"][0] != "/soc/spi@7d004000/can@0" {
		t.Errorf("can-bus nodes = %v", got["can-bus"])
	}
}

// The rs485 property sits on a port of a four-port expander: a child node with
// no compatible string of its own. An earlier version walked only the nodes
// that name a compatible and missed it entirely, which is the difference
// between "this board has RS485" and "this board does not".
func TestRS485IsFoundOnANodeWithNoCompatible(t *testing.T) {
	got := derived(t, carrier(t))
	nodes := got["rs485"]
	if len(nodes) != 1 {
		t.Fatalf("rs485 nodes = %v", nodes)
	}
	if nodes[0] != "/soc/spi@7d004000/sc16is752@1/serial@0" {
		t.Errorf("rs485 found at %s", nodes[0])
	}
}

// A disabled node describes hardware that exists and is deliberately not
// brought up. Counting it would read past the board's own statement - and the
// csi node in the fixture is disabled for exactly this test.
func TestDisabledHardwareIsNotACapability(t *testing.T) {
	got := derived(t, carrier(t))
	if nodes, ok := got["csi-camera"]; ok {
		t.Errorf("claimed csi-camera from a disabled node: %v", nodes)
	}
}

// And a child of a disabled parent is unreachable however its own status
// reads, which is how a tree turns off a whole subtree at its root.
func TestDisabledParentHidesItsChildren(t *testing.T) {
	tree := carrier(t)
	for _, n := range tree.All {
		if n.Path == "/soc/spi@7d004000" {
			n.Props["status"] = []byte("disabled\x00")
		}
	}
	got := derived(t, tree)
	if _, ok := got["can-bus"]; ok {
		t.Error("found can-bus under a disabled spi controller")
	}
	if _, ok := got["rs485"]; ok {
		t.Error("found rs485 under a disabled spi controller")
	}
	// The pcie port is elsewhere and still counts.
	if _, ok := got["nvme"]; !ok {
		t.Error("lost nvme, which is not under the disabled node")
	}
}

// Dual ethernet is a count rather than a match: one controller is not the
// capability, two are.
func TestDualEthernetNeedsTwo(t *testing.T) {
	tree := carrier(t)
	if _, ok := derived(t, tree)["dual-ethernet"]; !ok {
		t.Fatal("two controllers and no dual-ethernet")
	}

	for _, n := range tree.All {
		if n.Path == "/soc/ethernet@1f00110000" {
			n.Props["status"] = []byte("disabled\x00")
		}
	}
	if _, ok := derived(t, tree)["dual-ethernet"]; ok {
		t.Error("one enabled controller still claimed dual-ethernet")
	}
}

// What was looked for and not found is as much of the answer as what was: a
// fragment claiming rs485 on a board whose tree describes none is the case
// worth catching, and it only reads as a contradiction if the tool says which
// capabilities it knows how to look for.
func TestNotFoundNamesWhatWasSearchedFor(t *testing.T) {
	found := Derive(carrier(t))
	missing := NotFound(found)
	want := map[string]bool{"cellular": true, "csi-camera": true, "wifi-sdio": true}
	for _, m := range missing {
		delete(want, m)
	}
	if len(want) != 0 {
		t.Errorf("NotFound did not mention: %v", want)
	}
	for _, m := range missing {
		if m == "can-bus" || m == "rs485" {
			t.Errorf("NotFound listed %s, which was found", m)
		}
	}
}
