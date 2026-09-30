package dt

import "testing"

// A small kernel-shaped tree rather than a real one: the real thing is a
// gigabyte and the interesting cases are all reproducible in seven files. Each
// file here exists for a Makefile shape that appears in the kernel and that an
// earlier version of this scanner got wrong.
func drivers(t *testing.T) *Drivers {
	t.Helper()
	d, err := ScanKernel("testdata/fakekernel")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestScanFindsTheObviousCase(t *testing.T) {
	d := drivers(t)
	got := d.For("arm,pl011")
	if len(got) != 1 {
		t.Fatalf("arm,pl011 → %v", got)
	}
	if got[0].Symbol != "CONFIG_SERIAL_AMBA_PL011" {
		t.Errorf("symbol = %s", got[0].Symbol)
	}
	if got[0].Source != "drivers/tty/serial/amba-pl011.c" {
		t.Errorf("source = %s", got[0].Source)
	}
}

// One file, several compatible strings, all of them the same driver.
func TestOneDriverManyParts(t *testing.T) {
	d := drivers(t)
	for _, c := range []string{"microchip,mcp2510", "microchip,mcp2515"} {
		got := d.For(c)
		if len(got) != 1 || got[0].Symbol != "CONFIG_CAN_MCP251X" {
			t.Errorf("%s → %v", c, got)
		}
	}
}

// A composite module: sc16is7xx.o is built from sc16is7xx-core.o and
// sc16is7xx-spi.o, and the compatible string is in the -spi part. Joining on
// the file name alone finds no symbol for it, which would later look exactly
// like a driver that does not exist - the worst kind of wrong answer, because
// it accuses the configuration of something the scanner got wrong.
func TestCompositeModule(t *testing.T) {
	d := drivers(t)
	got := d.For("nxp,sc16is752")
	if len(got) != 1 {
		t.Fatalf("nxp,sc16is752 → %v (composite module not resolved)", got)
	}
	if got[0].Symbol != "CONFIG_SERIAL_SC16IS7XX" {
		t.Errorf("symbol = %s, want CONFIG_SERIAL_SC16IS7XX", got[0].Symbol)
	}
}

func TestUnknownIsUnknown(t *testing.T) {
	d := drivers(t)
	if d.Known("acme,nonexistent") {
		t.Error("claimed to know a made-up compatible string")
	}
	if len(d.For("acme,nonexistent")) != 0 {
		t.Error("returned a match for a made-up compatible string")
	}
}

// Binding walks the node's compatible strings in order and takes the first one
// any driver claims, which is what the kernel does and why the list is written
// most specific first.
func TestBindPrefersTheFirstClaimedString(t *testing.T) {
	d := drivers(t)

	n := &Node{Name: "serial@0", Props: map[string][]byte{
		// An unknown specific string, then a known generic one: the kernel
		// binds on the generic, and so should this.
		"compatible": []byte("acme,super-uart\x00arm,pl011\x00"),
	}}
	m, ok := d.Bind(n)
	if !ok || m.Symbol != "CONFIG_SERIAL_AMBA_PL011" {
		t.Errorf("bind = %v %v, want CONFIG_SERIAL_AMBA_PL011", m, ok)
	}

	none := &Node{Name: "mystery@0", Props: map[string][]byte{
		"compatible": []byte("acme,mystery\x00"),
	}}
	if _, ok := d.Bind(none); ok {
		t.Error("bound a node nothing claims")
	}
}

// The end-to-end shape: a real device tree against the driver map, which is
// what the check command does.
func TestTreeAgainstDrivers(t *testing.T) {
	tree := fixture(t)
	d := drivers(t)

	type finding struct {
		path   string
		symbol string
		bound  bool
	}
	var found []finding
	for _, n := range tree.Devices() {
		if !n.Enabled() {
			continue
		}
		m, ok := d.Bind(n)
		found = append(found, finding{n.Path, m.Symbol, ok})
	}

	want := map[string]string{
		"/soc/serial@7d001000":    "CONFIG_SERIAL_AMBA_PL011",
		"/soc/spi@7d004000":       "CONFIG_SPI_BCM2835",
		"/soc/spi@7d004000/can@0": "CONFIG_CAN_MCP251X",
		"/":                       "", // test,board: no driver, and none expected
		"/soc":                    "", // simple-bus: handled by the core
	}
	for _, f := range found {
		w, ok := want[f.path]
		if !ok {
			t.Errorf("unexpected device %s", f.path)
			continue
		}
		if f.symbol != w {
			t.Errorf("%s → %q, want %q", f.path, f.symbol, w)
		}
		delete(want, f.path)
	}
	// i2c@7d005000 is disabled and must not appear: a disabled node needs no
	// driver, which is the whole point of status.
	if _, still := want["/soc/i2c@7d005000"]; still {
		t.Error("a disabled node was checked")
	}
	delete(want, "/soc/i2c@7d005000")
	if len(want) != 0 {
		t.Errorf("never saw: %v", want)
	}
}
