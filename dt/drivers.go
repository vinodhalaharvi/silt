package dt

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Drivers maps a compatible string to the kernel configuration symbols that
// build a driver claiming it.
//
// The mapping is not written down anywhere as data - it is spread across the
// kernel source in two halves, and this reads both:
//
//	drivers/net/can/spi/mcp251x.c:
//	    { .compatible = "microchip,mcp2515", .data = ... },
//
//	drivers/net/can/spi/Makefile:
//	    obj-$(CONFIG_CAN_MCP251X) += mcp251x.o
//
// So: a file names a compatible string, and a Makefile line says which symbol
// builds that file. Join them on the file name and you have "this hardware
// needs this symbol", which is the fact that lets a device tree and a kernel
// configuration be checked against each other.
//
// This is the same shape as the cross-tree rules, and better in one way: those
// are hand-written from documentation and go stale silently, and these are
// derived from the tree they describe.
type Drivers struct {
	// byCompatible maps a compatible string to the symbols that can claim it.
	// More than one is normal: several drivers may support the same part, and
	// a driver may be buildable under either of two symbols.
	byCompatible map[string][]Match
	// Files scanned, for reporting how much was read.
	Files int
}

// Match is one driver claiming one compatible string.
type Match struct {
	Compatible string
	Symbol     string // CONFIG_CAN_MCP251X
	Source     string // drivers/net/can/spi/mcp251x.c, relative to the kernel root
}

var (
	// .compatible = "vendor,part"  in an of_device_id table. Spacing varies
	// and the field is sometimes written without the leading dot in older
	// code, hence the optional one.
	reCompatible = regexp.MustCompile(`\.?compatible\s*=\s*"([^"]+)"`)
	// obj-$(CONFIG_FOO) += bar.o, and the -y and -m forms, and the
	// several-objects-on-one-line form.
	reObj = regexp.MustCompile(`^\s*obj-\$\(\s*(CONFIG_[A-Za-z0-9_]+)\s*\)\s*[+:]?=\s*(.*)$`)
	// foo-objs := a.o b.o, and foo-y += c.o - a module assembled from
	// several files, where the compatible string may be in any of them.
	reComposite = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)-(?:objs|y)\s*[+:]?=\s*(.*)$`)
)

// ScanKernel reads a kernel source tree and builds the mapping.
//
// It walks drivers/, sound/, arch/*/mach-* and a few other places that contain
// of_device_id tables. The whole tree would work and takes several times as
// long for the sake of directories that contain none.
func ScanKernel(root string) (*Drivers, error) {
	d := &Drivers{byCompatible: map[string][]Match{}}

	// First pass: which symbol builds which object file, per directory.
	// Makefile scope is per directory, so the map is keyed by directory and
	// then by object name.
	symbolOf := map[string]map[string]string{}
	// Composite modules: which module an object belongs to, so a compatible
	// string in one file of a multi-file module gets the module's symbol.
	partOf := map[string]map[string]string{}

	dirs := []string{"drivers", "sound", "net", "fs", "arch"}
	for _, top := range dirs {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || e.Name() != "Makefile" {
				return nil
			}
			dir := filepath.Dir(path)
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer f.Close()
			if symbolOf[dir] == nil {
				symbolOf[dir] = map[string]string{}
				partOf[dir] = map[string]string{}
			}
			s := bufio.NewScanner(f)
			s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
			for s.Scan() {
				line := s.Text()
				if m := reObj.FindStringSubmatch(line); m != nil {
					for _, o := range strings.Fields(m[2]) {
						if strings.HasSuffix(o, ".o") {
							symbolOf[dir][strings.TrimSuffix(o, ".o")] = m[1]
						}
					}
					continue
				}
				if m := reComposite.FindStringSubmatch(line); m != nil {
					for _, o := range strings.Fields(m[2]) {
						if strings.HasSuffix(o, ".o") {
							partOf[dir][strings.TrimSuffix(o, ".o")] = m[1]
						}
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// Second pass: the compatible strings themselves.
	for _, top := range dirs {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(path, ".c") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			ms := reCompatible.FindAllSubmatch(b, -1)
			if len(ms) == 0 {
				return nil
			}
			d.Files++

			dir := filepath.Dir(path)
			stem := strings.TrimSuffix(filepath.Base(path), ".c")
			sym := symbolOf[dir][stem]
			if sym == "" {
				// Part of a composite module: find the module it belongs to,
				// then the symbol that builds the module.
				if mod := partOf[dir][stem]; mod != "" {
					sym = symbolOf[dir][mod]
				}
			}
			if sym == "" {
				// Built unconditionally, or built by a Makefile pattern this
				// does not understand. Recording it with no symbol would look
				// like a missing driver later, which is worse than silence.
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			for _, m := range ms {
				c := string(m[1])
				d.byCompatible[c] = append(d.byCompatible[c], Match{
					Compatible: c, Symbol: sym, Source: rel,
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return d, nil
}

// For returns the drivers that claim a compatible string.
func (d *Drivers) For(compatible string) []Match { return d.byCompatible[compatible] }

// Known reports whether any driver in the tree claims this string. A node whose
// compatible strings are all unknown is not necessarily broken: it may be
// handled by a bus driver, be a fixed regulator or clock, or describe something
// with no driver by design.
func (d *Drivers) Known(compatible string) bool { return len(d.byCompatible[compatible]) > 0 }

// Compatibles is how many distinct strings were found, for reporting.
func (d *Drivers) Compatibles() int { return len(d.byCompatible) }

// Bind decides what a node needs, taking the compatible strings in order.
//
// The kernel binds on the first compatible string any driver claims, which is
// why the list is written most specific first: a node compatible with
// "microchip,mcp2515" and then "microchip,mcp251x" binds to whichever the
// kernel knows, preferring the specific one. So the answer is the first match
// found walking the list, and "no driver" only when none of them is claimed.
func (d *Drivers) Bind(n *Node) (Match, bool) {
	for _, c := range n.Compatible() {
		if ms := d.byCompatible[c]; len(ms) > 0 {
			return ms[0], true
		}
	}
	return Match{}, false
}

func (m Match) String() string {
	return fmt.Sprintf("%s (%s, %s)", m.Symbol, m.Compatible, m.Source)
}
