package kconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func menuOf(t *testing.T, files map[string]string, env map[string]string) *Menu {
	t.Helper()
	dir := t.TempDir()
	for name, src := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(src), 0o644)
	}
	m, err := LoadMenu("Config.in", Options{Root: dir, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func eval(t *testing.T, m *Menu, defconfig string) map[string]string {
	t.Helper()
	as, err := ReadAssignments(strings.NewReader(defconfig))
	if err != nil {
		t.Fatal(err)
	}
	got := m.Evaluate(as).Config()
	if conf := os.Getenv("SILT_KCONF"); conf != "" {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "def"), []byte(defconfig), 0o644)
		cmd := exec.Command(conf, "--defconfig=def", filepath.Join(m.root, "Config.in"))
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "KCONFIG_CONFIG=.config")
		for k, v := range m.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("conf: %v\n%s", err, out)
		}
		f, _ := os.Open(filepath.Join(dir, ".config"))
		as, _ := ReadAssignments(f)
		f.Close()
		want := map[string]string{}
		for _, a := range as {
			want[a.Name] = a.Value
		}
		for _, k := range append(Names(want), Names(got)...) {
			if want[k] != got[k] {
				t.Errorf("conf disagrees on %s for %q: conf %q, silt %q", k, defconfig, want[k], got[k])
			}
		}
	}
	return got
}

func expect(t *testing.T, cfg map[string]string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		got, ok := cfg[k]
		if v == "" {
			if ok {
				t.Errorf("%s: want absent, got %s", k, got)
			}
			continue
		}
		if got != v {
			t.Errorf("%s: want %s, got %q (present=%v)", k, v, got, ok)
		}
	}
}

// The semantics a defconfig depends on, each checked in isolation. With
// SILT_KCONF pointing at Buildroot's conf binary (output/build/buildroot-config/
// conf), every case is also run through conf and the whole .config compared,
// so an expectation written from memory cannot pass by agreeing with a bug.
// The first draft of these tests expected an unpicked optional choice's
// member to be written as n; conf does not write it at all.
func TestEvaluateBasics(t *testing.T) {
	m := menuOf(t, map[string]string{"Config.in": `
config A
	bool "a"

config B
	bool "b"
	depends on A
	default y

config HIDDEN
	bool
	default y if A

config SELECTED
	bool "selected"
	depends on NEVER

config SELECTOR
	bool "selector"
	select SELECTED

config NAME
	string "name"
	default "x" if A
	default "fallback"

config COPY
	string
	default NAME

config EQ
	bool
	default y if NAME = "fallback"

menu "m"
	depends on A
config IN_MENU
	bool "in menu"
	default y
endmenu

if A
config IN_IF
	bool "in if"
endif
`}, nil)

	cfg := eval(t, m, "")
	expect(t, cfg, map[string]string{
		"A": "n", "B": "", "HIDDEN": "", "SELECTED": "", "NAME": `"fallback"`,
		"COPY": `"fallback"`, "EQ": "y", "IN_MENU": "", "IN_IF": "",
	})

	cfg = eval(t, m, "A=y\nSELECTOR=y\n")
	expect(t, cfg, map[string]string{
		"A": "y", "B": "y", "HIDDEN": "y", "NAME": `"x"`, "COPY": `"x"`, "EQ": "",
		"IN_MENU": "y", "IN_IF": "n",
		// select ignores the target's dependencies, and kbuild writes it.
		"SELECTED": "y",
	})

	// A user value for an invisible symbol is ignored, not honoured.
	cfg = eval(t, m, "B=y\n# A is not set\n")
	expect(t, cfg, map[string]string{"B": "", "A": "n"})
}

// Choices: the user's pick if visible, else the first visible default, else
// the first visible member; members nested in if blocks are members, entries
// under a prompted member are not.
func TestEvaluateChoices(t *testing.T) {
	m := menuOf(t, map[string]string{"Config.in": `
config ARM
	bool "arm"

choice
	prompt "cpu"
	default C2 if ARM
if ARM
config C1
	bool "c1"
config C2
	bool "c2"
endif
config C3
	bool "c3"
if C3
config C3_OPTION
	bool "c3 option"
	default y
endif
endchoice

choice
	prompt "optional"
	optional
config O1
	bool "o1"
endchoice
`}, nil)

	expect(t, eval(t, m, ""), map[string]string{
		// An optional choice with nothing picked is n, and its members are
		// not written at all.
		"C1": "", "C2": "", "C3": "y", "C3_OPTION": "y", "O1": "",
	})
	expect(t, eval(t, m, "ARM=y\n"), map[string]string{
		"C1": "n", "C2": "y", "C3": "n", "C3_OPTION": "",
	})
	expect(t, eval(t, m, "ARM=y\nC1=y\n"), map[string]string{"C1": "y", "C2": "n", "C3": "n"})
	// The user's pick is invisible, so the default takes over.
	expect(t, eval(t, m, "C1=y\n"), map[string]string{"C3": "y"})
	expect(t, eval(t, m, "O1=y\n"), map[string]string{"O1": "y"})
}

// option env symbols take the environment's value and are never written;
// a comparison against them is an ordinary string comparison.
func TestEvaluateEnv(t *testing.T) {
	m := menuOf(t, map[string]string{"Config.in": `
config GCC
	string
	option env="HOST_GCC_VERSION"

config AT_LEAST_13
	bool
	default y if GCC = "13"

config AT_LEAST_14
	bool
	default y if GCC = "14"
`}, map[string]string{"HOST_GCC_VERSION": "13"})
	expect(t, eval(t, m, ""), map[string]string{"GCC": "", "AT_LEAST_13": "y", "AT_LEAST_14": ""})
}

func TestHostEnvDerivation(t *testing.T) {
	for in, want := range map[string]string{
		"gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0\n": "13",
		"gcc (GCC) 4.9.4\n":                             "4 9",
		"gcc (GCC) 17.1.0\n":                            "15",
	} {
		if got := HostGCCVersion(in, 15); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Target: x86_64-linux-gnu\n":     "x86_64",
		"Target: i686-linux-gnu\n":       "x86",
		"Target: aarch64-linux-gnu\n":    "aarch64",
		"Target: armv7l-unknown-linux\n": "arm",
	} {
		if got := HostArch(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
