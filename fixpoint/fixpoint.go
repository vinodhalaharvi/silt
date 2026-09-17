// Package fixpoint is DESIGN.md §10's oracle: compare what Silt asked for with
// the .config kbuild actually produced, symbol by symbol.
//
// Any difference is Silt's model being wrong at that symbol, or an exclusion
// that should have been declared. Today the prediction is the stated
// constraints of a composition; once completion (rung 7) produces a total
// assignment, the same comparison runs over all of it.
package fixpoint

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/lang"
)

// Config is one tree's .config as kbuild wrote it: symbol name to raw value,
// with "is not set" lines recorded as "n". A symbol with no line at all is
// absent from the map.
type Config map[string]string

var (
	assignRe = regexp.MustCompile(`^([A-Za-z0-9_]+)=(.*)$`)
	unsetRe  = regexp.MustCompile(`^# ([A-Za-z0-9_]+) is not set$`)
)

// Read parses a .config. The prefix is not interpreted: whichever tree's conf
// wrote the file, a line is NAME=VALUE.
func Read(r io.Reader) (Config, error) {
	cfg := Config{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := unsetRe.FindStringSubmatch(line); m != nil {
			cfg[m[1]] = "n"
		} else if m := assignRe.FindStringSubmatch(line); m != nil {
			cfg[m[1]] = m[2]
		}
	}
	return cfg, sc.Err()
}

// Mismatch is a stated constraint the .config does not honour.
type Mismatch struct {
	Want lang.Constraint
	Got  string // raw value, or "" when the symbol is absent
}

func (m Mismatch) Error() string {
	got := m.Got
	if got == "" {
		got = "absent"
	}
	return fmt.Sprintf("%s: asked for %s, kbuild wrote %s  (%s at %s)",
		m.Want.Sym, want(m.Want), got, m.Want.From, m.Want.Pos.Short())
}

func want(c lang.Constraint) string {
	switch {
	case c.IsValue:
		return fmt.Sprintf("%q", c.Value)
	case c.AtLeast:
		return "at least " + c.Want.String()
	}
	return c.Want.String()
}

// Holds reports whether one constraint is honoured by a .config.
//
// Absent equals n. kbuild does not write "# X is not set" for a symbol whose
// dependencies are unmet; it deletes the line. (n BR2_PACKAGE_SYSTEMD) in a
// musl image comes back absent, and that is the request satisfied, not
// ignored. Treating absent as a failure would make every image with negative
// intent fail this check forever (DESIGN.md §12.1).
//
// The converse does not hold: absent never satisfies y, m, or a value.
func Holds(c lang.Constraint, cfg Config) bool {
	got, present := cfg[c.Sym.Name]
	if c.IsValue {
		if !present {
			return false
		}
		return unquote(got) == c.Value
	}
	have := lang.N
	if present {
		switch got {
		case "y":
			have = lang.Y
		case "m":
			have = lang.M
		case "n":
		default:
			return false // a bool stated, a value written: never equal
		}
	}
	if c.AtLeast {
		return have >= c.Want
	}
	return have == c.Want
}

// unquote reverses kconfig's string escaping: only \" and \\ are escaped.
// An unquoted value (int, hex) is returned as is.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	s = s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Check compares one tree of a composition with the .config kbuild produced
// for that tree. Hard constraints, rule-derived constraints and opaque values
// are compared; soft preferences are not predictions yet, and symbols covered
// by (unmanaged ...) are skipped because the image declared that kbuild owns
// them.
func Check(res *compose.Result, tree lang.Scope, cfg Config) []Mismatch {
	var out []Mismatch
	consider := func(c lang.Constraint) {
		if c.Soft || lang.Scope(c.Sym.Tree) != tree || unmanaged(c.Sym, res.Unmanaged) {
			return
		}
		if !Holds(c, cfg) {
			out = append(out, Mismatch{Want: c, Got: cfg[c.Sym.Name]})
		}
	}
	for _, c := range res.Constraints[tree] {
		consider(c)
	}
	for _, c := range res.Opaque {
		consider(c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Want.Sym.Name < out[j].Want.Sym.Name })
	return out
}

func unmanaged(id lang.SymbolID, patterns []lang.SymbolID) bool {
	for _, p := range patterns {
		if p.Tree != id.Tree {
			continue
		}
		if strings.HasSuffix(p.Name, "*") {
			if strings.HasPrefix(id.Name, strings.TrimSuffix(p.Name, "*")) {
				return true
			}
		} else if p.Name == id.Name {
			return true
		}
	}
	return false
}
