package kconfig_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/kconfig"
)

// The evaluator against the kernel's own conf, the same oracle the Buildroot
// tests use. SILT_LINUX must be a checkout complete enough to build
// scripts/kconfig (Kconfig files, Makefiles, scripts/ and arch configs), and
// make, flex and bison must be present.
//
// What this cannot match is the compiler probes: kbuild answers
// $(cc-option,...) by running the compiler, and for a cross build that is a
// compiler this machine may not have. Those symbols are read as no and
// reported, not guessed at.
func TestEvaluatorMatchesKernelConf(t *testing.T) {
	root := os.Getenv("SILT_LINUX")
	if root == "" || os.Getenv("SILT_KBUILD") == "" {
		t.Skip("set SILT_LINUX and SILT_KBUILD=1")
	}
	arch := "arm64"
	out := t.TempDir()
	cmd := exec.Command("make", "-s", "ARCH="+arch, "O="+out, "defconfig")
	cmd.Dir = root
	if o, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("the kernel's own conf could not be built here: %v\n%s", err, o)
	}

	env, err := kconfig.LinuxEnv(root, arch)
	if err != nil {
		t.Fatal(err)
	}
	m, err := kconfig.LoadMenu("Kconfig", kconfig.Options{Root: root, Env: env, Prefix: "CONFIG_"})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, "arch", arch, "configs", "defconfig"))
	if err != nil {
		t.Fatal(err)
	}
	as, err := kconfig.ReadAssignments(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	ev := m.Evaluate(as)
	got := ev.Config()

	g, err := os.Open(filepath.Join(out, ".config"))
	if err != nil {
		t.Fatal(err)
	}
	confAs, err := kconfig.ReadAssignments(g)
	g.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, a := range confAs {
		want[a.Name] = a.Value
	}

	probe := map[string]bool{}
	for _, n := range ev.Tainted() {
		probe[n] = true
	}
	names := map[string]bool{}
	for k := range want {
		names[k] = true
	}
	for k := range got {
		names[k] = true
	}
	var diffs, probeDiffs []string
	for k := range names {
		w, wok := want[k]
		v, vok := got[k]
		if wok == vok && w == v {
			continue
		}
		if !wok {
			w = "(absent)"
		}
		if !vok {
			v = "(absent)"
		}
		line := k + ": conf " + w + ", silt " + v
		if probe[k] {
			probeDiffs = append(probeDiffs, line)
		} else {
			diffs = append(diffs, line)
		}
	}
	sort.Strings(diffs)
	t.Logf("%s %s: conf wrote %d symbols, silt wrote %d; %d differ, %d of them compiler probes",
		env["KERNELVERSION"], arch, len(want), len(got), len(diffs)+len(probeDiffs), len(probeDiffs))
	t.Logf("%d symbols mention a probe in their own conditions", len(probe))

	// Modules are the kernel's alone: Buildroot has no tristate at all, and
	// the arm64 defconfig sets over a thousand symbols to m.
	mods := 0
	for _, v := range got {
		if v == "m" {
			mods++
		}
	}
	if mods < 500 {
		t.Errorf("only %d symbols came out as modules; option modules is not being honoured", mods)
	}

	// The rest are downstream of the probes: CC_HAS_* symbols and the
	// choices that depend on them. They are logged every run so a real
	// regression shows up as a jump rather than as one more line nobody
	// reads. Two rounds of genuine bugs came out this way before the count
	// settled here: the write prefix (a kernel defconfig says CONFIG_X, the
	// tree declares X) and relational comparisons (NR_CPUS >= 4), which
	// together accounted for about eight thousand of them.
	if len(diffs) > 0 {
		show := diffs
		if len(show) > 25 {
			show = show[:25]
		}
		t.Logf("%d differences in the probes' shadow:\n  %s", len(diffs), strings.Join(show, "\n  "))
	}
	const tolerated = 30
	if len(diffs) > tolerated {
		t.Errorf("%d differences that are not compiler probes, was %d: the model regressed",
			len(diffs), tolerated)
	}
}
