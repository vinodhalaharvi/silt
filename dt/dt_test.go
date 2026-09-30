package dt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The fixture is built by dtc rather than hand-assembled, so the test reads
// what a kernel build actually produces rather than what this package's author
// believed the format to be. Rebuilt here when dtc is available, so a change to
// the .dts is picked up; otherwise the committed .dtb is used.
func fixture(t *testing.T) *Tree {
	t.Helper()
	dtb := filepath.Join("testdata", "board.dtb")
	if dtc, err := exec.LookPath("dtc"); err == nil {
		out, err := exec.Command(dtc, "-I", "dts", "-O", "dtb", "-o", dtb,
			filepath.Join("testdata", "board.dts")).CombinedOutput()
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

func TestParse(t *testing.T) {
	tree := fixture(t)

	if got := tree.Model(); got != "Test Board" {
		t.Errorf("model = %q, want %q", got, "Test Board")
	}
	if got := tree.Root.Compatible(); len(got) != 2 || got[0] != "test,board" {
		t.Errorf("root compatible = %v", got)
	}

	// Paths, so a node can be named in a message someone has to act on.
	want := map[string]bool{
		"/":                       true,
		"/soc":                    true,
		"/soc/serial@7d001000":    true,
		"/soc/spi@7d004000":       true,
		"/soc/spi@7d004000/can@0": true,
		"/soc/i2c@7d005000":       true,
		"/chosen":                 true,
	}
	for _, n := range tree.All {
		delete(want, n.Path)
	}
	if len(want) != 0 {
		t.Errorf("never saw: %v", want)
	}
}

func TestDevicesAreNodesWithCompatible(t *testing.T) {
	tree := fixture(t)
	// /chosen has no compatible and is not a device; /soc has one
	// ("simple-bus") and is, which is correct - it is a bus with a driver.
	var paths []string
	for _, n := range tree.Devices() {
		paths = append(paths, n.Path)
	}
	for _, p := range paths {
		if p == "/chosen" {
			t.Error("/chosen has no compatible and should not be a device")
		}
	}
	if len(paths) != 6 {
		t.Errorf("got %d devices: %v", len(paths), paths)
	}
}

// Absence of status means enabled, which is the opposite of how it reads and
// the sort of thing worth pinning in a test.
func TestEnabledDefaultsToOn(t *testing.T) {
	tree := fixture(t)
	byPath := map[string]*Node{}
	for _, n := range tree.All {
		byPath[n.Path] = n
	}

	cases := []struct {
		path string
		want bool
	}{
		{"/soc", true},                    // no status at all
		{"/soc/serial@7d001000", true},    // status = "okay"
		{"/soc/i2c@7d005000", false},      // status = "disabled"
		{"/soc/spi@7d004000/can@0", true}, // nested, okay
	}
	for _, c := range cases {
		n := byPath[c.path]
		if n == nil {
			t.Fatalf("no node %s", c.path)
		}
		if got := n.Enabled(); got != c.want {
			t.Errorf("%s enabled = %v, want %v (status %q)", c.path, got, c.want, n.String("status"))
		}
	}
}

func TestCompatibleIsAListMostSpecificFirst(t *testing.T) {
	tree := fixture(t)
	for _, n := range tree.All {
		if n.Path != "/soc/serial@7d001000" {
			continue
		}
		got := n.Compatible()
		if len(got) != 2 || got[0] != "arm,pl011" || got[1] != "arm,primecell" {
			t.Errorf("compatible = %v, want [arm,pl011 arm,primecell]", got)
		}
		if n.Label() != "serial" {
			t.Errorf("label = %q, want serial", n.Label())
		}
		return
	}
	t.Fatal("no serial node")
}

func TestRejectsNonsense(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"empty", nil},
		{"short", []byte{0xd0, 0x0d}},
		{"bad magic", make([]byte, 64)},
	} {
		if _, err := Parse(c.in); err == nil {
			t.Errorf("%s: parsed without error", c.name)
		}
	}
}
