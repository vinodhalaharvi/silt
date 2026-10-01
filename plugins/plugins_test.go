package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/dt"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugin"
)

func stated(pairs ...string) Stated {
	s := Stated{}
	for i := 0; i+1 < len(pairs); i += 2 {
		tree, name, _ := strings.Cut(pairs[i], ":")
		s[lang.SymbolID{Tree: tree, Name: name}] = lang.Constraint{
			Sym:     lang.SymbolID{Tree: tree, Name: name},
			Value:   pairs[i+1],
			IsValue: true,
		}
	}
	return s
}

func ask(tree, name string) plan.Ask {
	return plan.Ask{Op: plan.Settled, Tree: tree, Name: name}
}

// A device tree answers from a file; the other kinds answer from what the
// library states. The point of the Settled question is that both come back as
// a string, so a rule can compare them without knowing either shape.
func dtb(t *testing.T) *dt.Tree {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "amp.dts")
	out := filepath.Join(dir, "amp.dtb")
	if err := writeFile(src, `/dts-v1/;
/ { model = "AMP";
    #address-cells = <1>;
    #size-cells = <1>;
    soc {
        compatible = "simple-bus";
        #address-cells = <1>;
        #size-cells = <1>;
        rpmsg@88000000 {
            compatible = "fsl,imx8mp-rpmsg";
            reg = <0x88000000 0x10000>;
            status = "okay";
        };
        uart@30890000 {
            compatible = "fsl,imx-uart";
            reg = <0x30890000 0x100>;
            current-speed = <115200>;
        };
    };
};
`); err != nil {
		t.Fatal(err)
	}
	dtc, err := exec.LookPath("dtc")
	if err != nil {
		t.Skip("no dtc")
	}
	if b, err := exec.Command(dtc, "-I", "dts", "-O", "dtb", "-o", out, src).CombinedOutput(); err != nil {
		t.Fatalf("dtc: %v\n%s", err, b)
	}
	tree, err := dt.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func TestDeviceTreeSettlesAnAddress(t *testing.T) {
	reg := plugin.Registry{"devicetree": plugin.Erase(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})}

	// A reg property is rendered as hex, because that is how the value is
	// written in every configuration it would be compared against.
	got, err := plugin.Run(plan.Lift(ask("devicetree", "rpmsg@88000000.reg"),
		func(a plan.Answer) plan.Answer { return a }), reg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "0x88000000" {
		t.Errorf("reg settled to %q, want 0x88000000 (err %v)", got.Value, got.Err)
	}
}

// A node can be named without its full path: a rule reads better naming
// rpmsg@88000000 than /soc/rpmsg@88000000.
func TestDeviceTreeFindsANodeByShortName(t *testing.T) {
	reg := plugin.Registry{"devicetree": plugin.Erase(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})}
	for _, name := range []string{"rpmsg@88000000", "/soc/rpmsg@88000000"} {
		got, err := plugin.Run(plan.Lift(ask("devicetree", name+".reg"),
			func(a plan.Answer) plan.Answer { return a }), reg)
		if err != nil {
			t.Fatal(err)
		}
		if got.Value != "0x88000000" {
			t.Errorf("%s settled to %q", name, got.Value)
		}
	}
}

func TestDeviceTreeReportsAMissingNode(t *testing.T) {
	reg := plugin.Registry{"devicetree": plugin.Erase(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})}
	got, _ := plugin.Run(plan.Lift(ask("devicetree", "nosuch@0.reg"),
		func(a plan.Answer) plan.Answer { return a }), reg)
	if !got.Failed() {
		t.Error("a missing node settled to something")
	}
}

// Without a tree, every question about it is unanswerable rather than failed -
// which is what `silt check` without --linux has always meant, now in one
// place for every kind.
func TestNoTreeIsUnanswerable(t *testing.T) {
	reg := plugin.Registry{"devicetree": plugin.Erase(DeviceTree, DeviceTreeCfg{Tree: nil})}
	got, _ := plugin.Run(plan.Lift(ask("devicetree", "rpmsg@88000000.reg"),
		func(a plan.Answer) plan.Answer { return a }), reg)
	if !got.Unknown {
		t.Error("a tree that was not given answered anyway")
	}
	if got.Failed() {
		t.Error("a tree that was not given reported a failure")
	}
}

// The end to end: one address stated in a structured tree, the same address in
// a device tree, compared without either kind knowing the other exists.
func TestSameValueAcrossKinds(t *testing.T) {
	tree := dtb(t)

	run := func(base string) plan.V[plan.Unit] {
		reg := plugin.Registry{
			"esphome":    plugin.Erase(ESPHome, ESPHomeCfg{Stated: stated("esphome:shm.base", base)}),
			"devicetree": plugin.Erase(DeviceTree, DeviceTreeCfg{Tree: tree}),
		}
		p := plan.SameValue(
			plan.Ask{Tree: "esphome", Name: "shm.base"},
			plan.Ask{Tree: "devicetree", Name: "rpmsg@88000000.reg"},
			"both ends must agree where the ring is")
		got, err := plugin.Run(p, reg)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	if v := run("0x88000000"); v.Failed() {
		t.Errorf("matching addresses reported as a mismatch: %v", v.Err())
	}

	v := run("0x88100000")
	if !v.Failed() {
		t.Fatal("mismatched addresses not reported")
	}
	msg := v.Err().Error()
	for _, want := range []string{"0x88100000", "0x88000000", "both ends must agree"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

// A tristate settles to its letter, so a flag spelled "y" in one tree and
// something else in another is a comparison that can be made at all.
func TestTristateSettlesToItsLetter(t *testing.T) {
	s := Stated{}
	id := lang.SymbolID{Tree: "buildroot", Name: "BR2_X"}
	s[id] = lang.Constraint{Sym: id, Want: lang.Y}

	reg := plugin.Registry{"buildroot": plugin.Erase(Kconfig, KconfigCfg{Stated: s})}
	got, _ := plugin.Run(plan.Lift(ask("buildroot", "BR2_X"),
		func(a plan.Answer) plan.Answer { return a }), reg)
	if got.Value != "y" {
		t.Errorf("tristate settled to %q, want y", got.Value)
	}
}

// A string property comes back as a string: not every cross-tree fact is an
// address.
func TestStringProperty(t *testing.T) {
	reg := plugin.Registry{"devicetree": plugin.Erase(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})}
	got, _ := plugin.Run(plan.Lift(ask("devicetree", "rpmsg@88000000.status"),
		func(a plan.Answer) plan.Answer { return a }), reg)
	if got.Value != "okay" {
		t.Errorf("status settled to %q, want okay", got.Value)
	}
}

func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o644) }
