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

func dtb(t *testing.T) *dt.Tree {
	t.Helper()
	dir := t.TempDir()
	src, out := filepath.Join(dir, "amp.dts"), filepath.Join(dir, "amp.dtb")
	if err := os.WriteFile(src, []byte(`/dts-v1/;
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
    };
};
`), 0o644); err != nil {
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

func settledIn[C, Cfg, D any](t *testing.T, b *plugin.Bound[C, Cfg, D], tree, name string) plan.Answer {
	t.Helper()
	got, err := plugin.Run(plan.Lift(
		plan.Ask[C]{Op: plan.Settled, Tree: tree, Name: name},
		func(a plan.Answer) plan.Answer { return a }), b)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An address is rendered as hex, because that is how it is written in every
// configuration it would be compared against.
func TestDeviceTreeSettlesAnAddress(t *testing.T) {
	b := plugin.Bind(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})
	if got := settledIn(t, b, "devicetree", "rpmsg@88000000.reg"); got.Value != "0x88000000" {
		t.Errorf("reg settled to %q (err %v)", got.Value, got.Err)
	}
}

// A node can be named without its full path: a rule reads better naming
// rpmsg@88000000 than /soc/rpmsg@88000000.
func TestDeviceTreeShortNames(t *testing.T) {
	b := plugin.Bind(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})
	for _, n := range []string{"rpmsg@88000000", "/soc/rpmsg@88000000"} {
		if got := settledIn(t, b, "devicetree", n+".reg"); got.Value != "0x88000000" {
			t.Errorf("%s settled to %q", n, got.Value)
		}
	}
}

func TestDeviceTreeMissingNode(t *testing.T) {
	b := plugin.Bind(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})
	if got := settledIn(t, b, "devicetree", "nosuch@0.reg"); !got.Failed() {
		t.Error("a missing node settled to something")
	}
}

// A string property comes back as a string: not every cross-tree fact is an
// address.
func TestDeviceTreeStringProperty(t *testing.T) {
	b := plugin.Bind(DeviceTree, DeviceTreeCfg{Tree: dtb(t)})
	if got := settledIn(t, b, "devicetree", "rpmsg@88000000.status"); got.Value != "okay" {
		t.Errorf("status settled to %q", got.Value)
	}
}

// Without a tree, every question is unanswerable rather than failed - which is
// what silt check without --linux has always meant.
func TestNoTreeIsUnanswerable(t *testing.T) {
	b := plugin.Bind(DeviceTree, DeviceTreeCfg{Tree: nil})
	got := settledIn(t, b, "devicetree", "rpmsg@88000000.reg")
	if !got.Unknown || got.Failed() {
		t.Error("a tree that was not given answered anyway")
	}
}

// A tristate settles to its letter, so a flag spelled "y" in one tree and
// something else in another is a comparison that can be made at all.
func TestTristateSettlesToItsLetter(t *testing.T) {
	id := lang.SymbolID{Tree: "buildroot", Name: "BR2_X"}
	b := plugin.Bind(Kconfig, KconfigCfg{
		Scope:  "buildroot",
		Stated: map[string]lang.Constraint{"BR2_X": {Sym: id, Want: lang.Y}},
	})
	if got := settledIn(t, b, "buildroot", "BR2_X"); got.Value != "y" {
		t.Errorf("tristate settled to %q, want y", got.Value)
	}
}

// The end to end: one address stated in a structured tree, the same address in
// a device tree, compared without either kind knowing the other exists - and,
// because they are two kinds, through two separately typed runners.
func TestCompareAcrossKinds(t *testing.T) {
	tree := dtb(t)

	compare := func(base string) plan.V[plan.Unit] {
		esp := plugin.Bind(ESPHome, ESPHomeCfg{Stated: map[string]string{"shm.base": base}})
		dtB := plugin.Bind(DeviceTree, DeviceTreeCfg{Tree: tree})
		return plan.Compare(
			settledIn(t, esp, "esphome", "shm.base"),
			settledIn(t, dtB, "devicetree", "rpmsg@88000000.reg"),
			"esphome:shm.base and devicetree:rpmsg@88000000.reg are %s and %s",
			"both ends must agree where the ring is")
	}

	if v := compare("0x88000000"); v.Failed() {
		t.Errorf("matching addresses reported as a mismatch: %v", v.Err())
	}
	v := compare("0x88100000")
	if !v.Failed() {
		t.Fatal("mismatched addresses not reported")
	}
	for _, want := range []string{"0x88100000", "0x88000000", "both ends must agree"} {
		if !strings.Contains(v.Err().Error(), want) {
			t.Errorf("message lacks %q:\n%v", want, v.Err())
		}
	}
}

// The cache's policy, now a function on the kind that knows why each entry is
// on the list.
func TestAffects(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"BR2_PACKAGE_MOSQUITTO", true},
		{"BR2_TARGET_GENERIC_HOSTNAME", true},
		{"BR2_ROOTFS_OVERLAY", false},
		{"BR2_JLEVEL", false},
		{"BR2_EXTERNAL_TAILSCALE_PATH", false},
		{"BR2_EXTERNAL_TAILSCALE_VERSION", false},
		{"BR2_TARGET_ROOTFS_EXT2_SIZE", false},
	} {
		if got := Kconfig.Affects(c.name); got != c.want {
			t.Errorf("Affects(%s) = %v, want %v", c.name, got, c.want)
		}
	}

	// A kind that does not say is assumed to have everything matter.
	if ESPHome.Affects != nil {
		t.Error("the esphome kind claims a build-affecting policy it does not have")
	}
}
