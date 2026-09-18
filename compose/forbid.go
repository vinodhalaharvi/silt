package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/lang"
)

// Violation is a forbidden capability whose symbol is on in the configuration
// that was actually predicted.
type Violation struct {
	Capability string
	Symbol     lang.SymbolID
	Forbidder  lang.ID
	Pos        string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s is on, which provides forbidden capability %q\n  forbidden by %s at %s",
		v.Symbol, v.Capability, v.Forbidder, v.Pos)
}

// CheckForbidden tests an absence claim against a whole predicted
// configuration rather than against what fragments happened to state.
//
// This is the check that means something. Compose-time forbidding only catches
// a fragment that declares it provides the capability; a package pulled in by
// somebody else's select, or left on by a Kconfig default, provides it just as
// effectively and says nothing. on is every symbol the evaluator predicts to be
// enabled.
func (r *Result) CheckForbidden(declared map[string]lang.CapabilityDecl, on func(lang.SymbolID) bool) []Violation {
	var out []Violation
	names := make([]string, 0, len(r.Forbidden))
	for n := range r.Forbidden {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		cap := r.Forbidden[name]
		decl, ok := declared[name]
		if !ok {
			continue
		}
		syms := decl.Symbols
		if len(syms) == 0 && decl.Symbol != (lang.SymbolID{}) {
			syms = []lang.SymbolID{decl.Symbol}
		}
		for _, s := range syms {
			if on(s) {
				out = append(out, Violation{
					Capability: name, Symbol: s,
					Forbidder: r.forbidder[name], Pos: cap.Pos.Short(),
				})
			}
		}
	}
	return out
}

// Unbound reports forbidden capabilities that name no symbols, because those
// claims cannot be checked against a configuration at all.
//
// Reporting them is the difference between an absence claim that is verified
// and one that merely looks it.
func (r *Result) Unbound(declared map[string]lang.CapabilityDecl) []string {
	var out []string
	for name := range r.Forbidden {
		d, ok := declared[name]
		if !ok || (len(d.Symbols) == 0 && d.Symbol == (lang.SymbolID{})) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Explain renders violations for a command to print.
func ExplainViolations(vs []Violation) string {
	if len(vs) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n%d forbidden capability violation(s):\n", len(vs))
	for _, v := range vs {
		fmt.Fprintf(&b, "  %s\n", v)
	}
	return b.String()
}
