package kconfig

import (
	"fmt"
	"strconv"
)

// Because names the mechanism that decided a symbol's value.
//
// The order below is the order calc considers them, which is the order
// sym_calc_value considers them. A reason that listed them in any other order
// would be a plausible story rather than what happened.
type Because uint8

const (
	BecauseUnknown         Because = iota
	BecauseNotConfigurable         // no type: a comment, a menu, a choice header
	BecauseChoiceMember            // the choice picked it, or picked another
	BecauseUserValue               // a .config line, visible, so it stood
	BecauseUserOverridden          // a .config line the prompt's invisibility discarded
	BecauseSelected                // forced on by another symbol's select
	BecauseDefault                 // the first default whose condition held
	BecauseImplied                 // raised by an imply
	BecauseNoDefault               // nothing said anything; n
)

func (b Because) String() string {
	switch b {
	case BecauseNotConfigurable:
		return "not a configurable symbol"
	case BecauseChoiceMember:
		return "decided by its choice group"
	case BecauseUserValue:
		return "set in the configuration"
	case BecauseUserOverridden:
		return "set in the configuration, but not selectable"
	case BecauseSelected:
		return "forced on by a select"
	case BecauseDefault:
		return "took a default"
	case BecauseImplied:
		return "raised by an imply"
	case BecauseNoDefault:
		return "nothing set it"
	}
	return "unknown"
}

// Reason is why one symbol has the value it has.
//
// silt's own why answers for symbols a fragment stated or a rule derived. That
// is a small minority: an image states twenty symbols and kbuild writes four
// hundred. The evaluator knows why every one of them has its value, because
// deciding that is what it does.
type Reason struct {
	Symbol  string
	Value   string
	Because Because
	// Detail is the specific thing responsible: the selecting symbol, the
	// default's expression, the chosen choice member.
	Detail string
	// File and Line locate the property that decided it, when one did.
	File string
	Line int
	// Visible is the symbol's prompt visibility. A symbol with no visible
	// prompt cannot be set by hand however much a fragment asks, which is the
	// most common surprise in a Kconfig tree.
	Visible string
	// Alternatives are the other defaults that did not fire, in order, with
	// the condition that excluded each. Kconfig takes the first whose
	// condition holds, so the ones above it are the interesting part of a
	// wrong answer.
	Alternatives []string
}

// Why explains one symbol, mirroring the order calc decides it in.
func (ev *Evaluation) Why(name string) Reason {
	r := Reason{Symbol: name, Because: BecauseUnknown}
	s := ev.m.Syms[name]
	if s == nil {
		return r
	}
	ev.calc(s)
	st := ev.st(s)
	r.Value = ev.str(s)
	r.Visible = triName(st.visible)

	if s.Type != Bool && s.Type != Tristate && s.Type != String && s.Type != Int && s.Type != Hex {
		r.Because = BecauseNotConfigurable
		return r
	}

	// A choice member's value is the choice's business, not its own.
	if s.Choice != nil {
		r.Because = BecauseChoiceMember
		cs := ev.st(s.Choice)
		if cs.sel != nil {
			r.Detail = "the choice selected " + cs.sel.Name
		} else {
			r.Detail = "the choice selected nothing"
		}
		for _, p := range s.Choice.Props {
			if p.Kind == PropPrompt {
				r.File, r.Line = p.Entry.File, p.Entry.Line
				break
			}
		}
		return r
	}

	// A user value stands only while the prompt is visible. When it is not,
	// the value in the .config is discarded — which is exactly the silent drop
	// that motivates this project, seen from the other side.
	if st.user != nil {
		if st.visible != No {
			r.Because = BecauseUserValue
			r.Detail = "a .config line, and the prompt is visible"
		} else {
			r.Because = BecauseUserOverridden
			r.Detail = "the prompt is not visible, so the configured value was discarded"
		}
		if p := promptProp(s); p != nil {
			r.File, r.Line = p.Entry.File, p.Entry.Line
		}
		return r
	}

	if st.revDep != No {
		r.Because = BecauseSelected
		r.Detail = selectorOf(ev, s)
		return r
	}

	if p := ev.defaultProp(s); p != nil {
		r.Because = BecauseDefault
		r.Detail = "default " + p.Value.String()
		if ev.tri(p.Vis) != Yes {
			r.Detail += " if " + p.Vis.String()
		}
		r.File, r.Line = p.Entry.File, p.Entry.Line
		r.Alternatives = skippedDefaults(ev, s, p)
		return r
	}

	if st.implied != No {
		r.Because = BecauseImplied
		return r
	}

	r.Because = BecauseNoDefault
	r.Alternatives = skippedDefaults(ev, s, nil)
	return r
}

// skippedDefaults lists the defaults Kconfig passed over before the one that
// fired, with the condition that excluded each.
//
// This is the part a person actually needs: a symbol with five defaults and the
// wrong one firing is a question about the four above it.
func skippedDefaults(ev *Evaluation, s *KSym, taken *Prop) []string {
	var out []string
	for _, p := range s.Props {
		if p.Kind != PropDefault {
			continue
		}
		if p == taken {
			break
		}
		out = append(out, fmt.Sprintf("default %s if %s — condition false (%s:%d)",
			p.Value.String(), p.Vis.String(), p.Entry.File, p.Entry.Line))
	}
	return out
}

func selectorOf(ev *Evaluation, s *KSym) string {
	if s.RevDep == nil {
		return "something selects it"
	}
	var found string
	var walk func(e *Expr)
	walk = func(e *Expr) {
		if found != "" {
			return
		}
		if e.Op == ExprOr {
			walk(e.Args[0])
			walk(e.Args[1])
			return
		}
		if ev.tri(e) != No {
			found = "selected by " + e.String()
		}
	}
	walk(s.RevDep)
	if found == "" {
		return "selected by " + s.RevDep.String()
	}
	return found
}

func promptProp(s *KSym) *Prop {
	for _, p := range s.Props {
		if p.Kind == PropPrompt {
			return p
		}
	}
	return nil
}

func triName(t Tri) string { return [...]string{"n", "m", "y"}[t] }

// Format renders a reason for a person.
func (r Reason) Format() string {
	if r.Because == BecauseUnknown {
		return r.Symbol + " is not in this tree\n"
	}
	out := fmt.Sprintf("%s = %s\n  %s", r.Symbol, r.Value, r.Because)
	if r.Detail != "" {
		out += ": " + r.Detail
	}
	if r.File != "" {
		out += "\n  " + r.File + ":" + strconv.Itoa(r.Line)
	}
	if r.Visible == "n" {
		out += "\n  no visible prompt: this symbol cannot be set by hand"
	}
	for _, a := range r.Alternatives {
		out += "\n  passed over: " + a
	}
	return out + "\n"
}
