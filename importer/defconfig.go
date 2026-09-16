// Package importer turns a Buildroot defconfig into Silt fragments.
//
// Every fragment in the library before this package existed was written from
// memory of Buildroot and verified afterwards, and most of the build failures
// the project has hit were claims about a release nobody had checked. Importing
// inverts that: the tree and a real defconfig are the source, and the fragment
// is derived from them.
//
// What an importer cannot do is decide intent. Whether a line belongs to the
// hardware or to userspace policy is a judgment about what is meant to vary,
// and nothing in a defconfig records it. Classify makes a first cut by rules
// that are printed alongside every symbol, so the cut can be argued with
// rather than trusted.
package importer

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Entry is one assignment read from a defconfig.
type Entry struct {
	Symbol string
	// Unset is a "# SYM is not set" line.
	Unset bool
	// Raw is the right-hand side exactly as written: y, m, 42, 0x10, or a
	// quoted string including its quotes.
	Raw  string
	Line int
}

// IsString reports whether the value was written quoted.
func (e Entry) IsString() bool { return strings.HasPrefix(e.Raw, `"`) }

// String returns the unquoted, unescaped string value.
func (e Entry) String() string {
	s := strings.TrimSuffix(strings.TrimPrefix(e.Raw, `"`), `"`)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var (
	assignRe = regexp.MustCompile(`^(BR2_[A-Za-z0-9_]+)=(.*)$`)
	unsetRe  = regexp.MustCompile(`^# (BR2_[A-Za-z0-9_]+) is not set$`)
)

// ParseDefconfig reads assignments in file order. Comments other than "is not
// set" lines are ignored; anything else that is not an assignment is an error,
// because silently skipping a line is how a config quietly loses a setting.
func ParseDefconfig(r io.Reader, name string) ([]Entry, error) {
	var out []Entry
	seen := map[string]int{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), " \t\r")
		var e Entry
		switch {
		case line == "":
			continue
		case unsetRe.MatchString(line):
			e = Entry{Symbol: unsetRe.FindStringSubmatch(line)[1], Unset: true, Line: n}
		case strings.HasPrefix(line, "#"):
			continue
		case assignRe.MatchString(line):
			m := assignRe.FindStringSubmatch(line)
			e = Entry{Symbol: m[1], Raw: m[2], Line: n}
		default:
			return nil, fmt.Errorf("%s:%d: not a defconfig assignment: %q", name, n, line)
		}
		if prev, ok := seen[e.Symbol]; ok {
			// kconfig takes the last one and warns. A source for fragments
			// should not contain a disagreement nobody resolved.
			return nil, fmt.Errorf("%s:%d: %s already assigned at line %d", name, n, e.Symbol, prev)
		}
		seen[e.Symbol] = n
		out = append(out, e)
	}
	return out, sc.Err()
}
