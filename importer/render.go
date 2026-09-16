package importer

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vinodhalaharvi/silt/kconfig"
)

// Imported is the result of importing one defconfig.
type Imported struct {
	Name    string // fragment and image name
	Source  string // path of the defconfig, as given
	Version string // buildroot release it was read against
	Entries []Classified
	// Provides lists capabilities the target declares. Import only fills it
	// from kbuild's own .config of the source, for capabilities the library
	// ties to a symbol; a capability with no symbol is a claim nobody can
	// derive, so it is left for a person to add.
	Provides []string
}

// Classified is an entry with its class and the rule that chose it.
type Classified struct {
	Entry
	Class  Class
	Reason string
	Type   kconfig.Type
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// NameFor derives a fragment name from a defconfig path:
// configs/raspberrypi4_64_defconfig becomes raspberrypi4-64.
func NameFor(path string) string {
	n := filepath.Base(path)
	n = strings.TrimSuffix(n, "_defconfig")
	n = strings.TrimSuffix(n, "defconfig")
	n = strings.ToLower(strings.ReplaceAll(n, "_", "-"))
	n = strings.Trim(n, "-")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "imported-" + n
	}
	return n
}

// Import checks every entry against the tree and classifies it. Unknown
// symbols and values of the wrong shape are errors, collected rather than
// stopping at the first, since a legacy defconfig tends to have several.
func Import(name, source, version string, entries []Entry, tree *kconfig.Tree) (*Imported, error) {
	if !nameRe.MatchString(name) {
		return nil, fmt.Errorf("%q is not a valid fragment name (lowercase letters, digits, hyphens)", name)
	}
	im := &Imported{Name: name, Source: source, Version: version}
	var problems []string
	for _, e := range entries {
		sym := tree.Symbols[e.Symbol]
		if sym == nil || sym.Type == kconfig.Unknown {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is not a symbol in buildroot %s",
				source, e.Line, e.Symbol, version))
			continue
		}
		if msg := shapeError(e, sym.Type); msg != "" {
			problems = append(problems, fmt.Sprintf("%s:%d: %s is %s, but %s",
				source, e.Line, e.Symbol, sym.Type, msg))
			continue
		}
		cl, why := Classify(e.Symbol, sym)
		im.Entries = append(im.Entries, Classified{Entry: e, Class: cl, Reason: why, Type: sym.Type})
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return im, nil
}

func shapeError(e Entry, t kconfig.Type) string {
	switch {
	case t.Solvable() && e.Unset:
		return ""
	case t.Solvable() && (e.Raw == "y" || e.Raw == "m"):
		if e.Raw == "m" && t != kconfig.Tristate {
			return "is assigned m"
		}
		return ""
	case t.Solvable():
		return fmt.Sprintf("is assigned %s", e.Raw)
	case e.Unset:
		return "is written as not set"
	case t == kconfig.String && !e.IsString():
		return fmt.Sprintf("is assigned unquoted %s", e.Raw)
	case t != kconfig.String && e.IsString():
		return fmt.Sprintf("is assigned a quoted string %s", e.Raw)
	}
	return ""
}

// Count returns how many entries landed in each class.
func (im *Imported) Count(c Class) int {
	n := 0
	for _, e := range im.Entries {
		if e.Class == c {
			n++
		}
	}
	return n
}

// Fragment renders one class as a fragment file. Entries keep defconfig order,
// which is Kconfig declaration order when the defconfig came from
// savedefconfig, and each carries the line it was read from.
func (im *Imported) Fragment(c Class) string {
	var b strings.Builder
	fmt.Fprintf(&b, ";; Imported by silt import from %s, buildroot %s.\n", im.Source, im.Version)
	fmt.Fprintf(&b, ";; The %s/profile split is a first cut by rule; the rule is noted on\n", "target")
	fmt.Fprintf(&b, ";; every line. Move lines between the two fragments freely.\n\n")
	fmt.Fprintf(&b, "(fragment %s:%s\n", c, im.Name)
	fmt.Fprintf(&b, "  (doc %s)\n", quote(fmt.Sprintf("%s part of %s", c, filepath.Base(im.Source))))
	var lines []string
	for _, e := range im.Entries {
		if e.Class != c {
			continue
		}
		lines = append(lines, fmt.Sprintf("    %-58s ; :%d %s", form(e), e.Line, e.Reason))
	}
	if c == Target && len(im.Provides) > 0 {
		b.WriteString("\n  ;; Derived from kbuild's .config of the source, not asserted.\n  (provides")
		for _, p := range im.Provides {
			fmt.Fprintf(&b, "\n    (capability %s)", p)
		}
		b.WriteString(")\n")
	}
	if len(lines) > 0 {
		b.WriteString("\n  (buildroot\n")
		b.WriteString(strings.Join(lines, "\n"))
		// Every line ends in a comment, so the closing parens need a line of
		// their own or they are commented out.
		b.WriteString("\n  )")
	}
	b.WriteString(")\n")
	return b.String()
}

// Image renders an image composing the two fragments.
func (im *Imported) Image() string {
	var b strings.Builder
	fmt.Fprintf(&b, ";; Imported by silt import from %s.\n", im.Source)
	fmt.Fprintf(&b, ";; silt import --kbuild checks this composes back to the source exactly.\n\n")
	fmt.Fprintf(&b, "(image %s\n", im.Name)
	fmt.Fprintf(&b, "  (compose\n    target:%s\n    profile:%s)\n", im.Name, im.Name)
	fmt.Fprintf(&b, "  (verified-against (buildroot %s)))\n", quote(im.Version))
	return b.String()
}

func form(e Classified) string {
	switch {
	case e.Unset:
		return fmt.Sprintf("(n %s)", e.Symbol)
	case e.Type.Solvable():
		return fmt.Sprintf("(%s %s)", e.Raw, e.Symbol)
	case e.IsString():
		return fmt.Sprintf("(value %s %s)", e.Symbol, quote(e.String()))
	}
	return fmt.Sprintf("(value %s %s)", e.Symbol, quote(e.Raw))
}

// quote writes a Silt string: only backslash and double quote are escaped,
// which is also all kconfig escapes.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
