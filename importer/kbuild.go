package importer

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Kbuild runs Buildroot's own configuration targets in one output directory.
//
// It is the authority on what a defconfig means. Silt's model can say whether
// a configuration is possible; only kbuild says what it becomes, and in
// particular only savedefconfig says which lines are derivable. The model
// cannot answer that yet: `A depends on B` makes B forced by A in the model,
// but kbuild will not turn B on for A, so B must stay in the defconfig.
type Kbuild struct {
	Root string // buildroot checkout
	Out  string // O= directory; kconfig's conf is built once and reused
}

func (k *Kbuild) make(args ...string) error {
	cmd := exec.Command("make", append([]string{"-s", "-C", k.Root, "O=" + k.Out}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("make %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

// Defconfig loads a defconfig, producing a full .config.
func (k *Kbuild) Defconfig(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// make defconfig with a missing BR2_DEFCONFIG exits zero and configures
	// Buildroot's defaults instead. That cost a build once already.
	if _, err := os.Stat(abs); err != nil {
		return err
	}
	return k.make("defconfig", "BR2_DEFCONFIG="+abs)
}

// Savedefconfig returns the minimal defconfig for the current .config.
func (k *Kbuild) Savedefconfig() (string, error) {
	dst := filepath.Join(k.Out, "silt-saved-defconfig")
	if err := k.make("savedefconfig", "BR2_DEFCONFIG="+dst); err != nil {
		return "", err
	}
	data, err := os.ReadFile(dst)
	return string(data), err
}

// Config reads the current .config into symbol -> raw value. Symbols written
// as "is not set" map to "n"; symbols absent from the file are absent here,
// which kbuild treats the same as n (NEXT-SESSION §5).
func (k *Kbuild) Config() (map[string]string, error) {
	f, err := os.Open(filepath.Join(k.Out, ".config"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := unsetRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = "n"
		} else if m := assignRe.FindStringSubmatch(line); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out, sc.Err()
}

// Normalize runs a defconfig or full .config through kbuild and returns what
// savedefconfig makes of it.
func (k *Kbuild) Normalize(path string) (string, error) {
	if err := k.Defconfig(path); err != nil {
		return "", err
	}
	return k.Savedefconfig()
}

// Holds reports whether an entry's value survived into the current .config.
func Holds(e Entry, cfg map[string]string) bool {
	got, ok := cfg[e.Symbol]
	if e.Unset || e.Raw == "n" {
		return !ok || got == "n"
	}
	return ok && got == e.Raw
}
