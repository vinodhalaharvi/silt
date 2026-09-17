package kconfig

import (
	"bufio"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Tri is a Kconfig tristate value. Buildroot has no modules, so m only ever
// appears transiently and is promoted to y, as kbuild promotes it.
type Tri uint8

const (
	No Tri = iota
	Mod
	Yes
)

func triMin(a, b Tri) Tri {
	if a < b {
		return a
	}
	return b
}

func triMax(a, b Tri) Tri {
	if a > b {
		return a
	}
	return b
}

// userValue is what a defconfig said about a symbol.
type userValue struct {
	tri Tri
	str string
}

type symState struct {
	valid bool
	// tainted marks a symbol one of whose own conditions is a macro call:
	// $(cc-option,...) and friends, which kbuild evaluates by running the
	// compiler. Silt cannot, so it reads them as no and says which symbols
	// those were. Only direct mentions are marked: taint followed through
	// dependencies reaches almost every symbol in the kernel, which is true
	// and useless.
	tainted bool
	tri     Tri
	str     string
	visible Tri
	revDep  Tri
	implied Tri
	write   bool
	user    *userValue
	sel     *KSym // for a choice: the selected member
	selUser *KSym // for a choice: the member the defconfig set to y
}

// Evaluation is kbuild's sym_calc_value over a Menu with a defconfig loaded:
// the lazy, memoised, recursive calculation of support/kconfig/symbol.c. It
// is not a fixed point and not a declaration-order pass; each symbol is
// computed on demand from the symbols its visibility, defaults and reverse
// dependencies mention, and a symbol reached again while it is being computed
// reads as its type's zero value, exactly as in C.
type Evaluation struct {
	m     *Menu
	state map[*KSym]*symState
	// modules is the value of the "option modules" symbol. Without one, m
	// cannot exist and every m is promoted to y, which is why Buildroot's
	// tristates behave like booleans.
	modules Tri
	// stack is the symbols being computed, innermost last, so a macro
	// reached while evaluating one taints it.
	stack []*KSym
}

// Evaluate loads assignments as user values and returns the evaluation. The
// assignments are in defconfig form: name to "y", "n", a quoted string or a
// bare number; later entries for the same name replace earlier ones.
func (m *Menu) Evaluate(assign []Assignment) *Evaluation {
	ev := &Evaluation{m: m, state: map[*KSym]*symState{}}
	for _, c := range m.Choices {
		m.inheritChoiceType(c)
		// conf_read_simple marks every choice as having a user value before
		// reading a single line.
		ev.st(c).user = &userValue{}
	}
	for _, a := range assign {
		s, ok := m.Syms[strings.TrimPrefix(a.Name, m.Prefix)]
		if !ok || s.Type == Unknown {
			continue // kbuild ignores lines for symbols it does not know
		}
		st := ev.st(s)
		switch s.Type {
		case Bool, Tristate:
			t, ok := map[string]Tri{"y": Yes, "m": Mod, "n": No}[a.Value]
			if !ok {
				continue
			}
			st.user = &userValue{tri: t}
			if c := s.Choice; c != nil {
				cs := ev.st(c)
				if t == Yes {
					cs.selUser = s
				}
				cs.user.tri = triMax(cs.user.tri, t)
			}
		default:
			st.user = &userValue{str: unescape(a.Value)}
		}
	}
	if m.Modules != nil {
		// kbuild computes this first and recomputes it whenever it changes;
		// everything else's promotion of m depends on it.
		ev.calc(m.Modules)
		ev.modules = ev.st(m.Modules).tri
	}
	return ev
}

func (m *Menu) inheritChoiceType(c *KSym) {
	if c.Type == Unknown {
		for _, s := range c.Members {
			if s.Type != Unknown {
				c.Type = s.Type
				break
			}
		}
	}
	for _, s := range c.Members {
		if s.Type == Unknown {
			s.Type = c.Type
		}
	}
}

// Assignment is one defconfig line.
type Assignment struct{ Name, Value string }

var (
	evAssignRe = regexp.MustCompile(`^([A-Za-z0-9_]+)=(.*)$`)
	evUnsetRe  = regexp.MustCompile(`^# ([A-Za-z0-9_]+) is not set$`)
)

// ReadAssignments parses a defconfig or .config, with prefix "" as Buildroot
// uses it.
func ReadAssignments(r io.Reader) ([]Assignment, error) {
	var out []Assignment
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := evUnsetRe.FindStringSubmatch(line); m != nil {
			out = append(out, Assignment{m[1], "n"})
		} else if m := evAssignRe.FindStringSubmatch(line); m != nil {
			out = append(out, Assignment{m[1], m[2]})
		}
	}
	return out, sc.Err()
}

func unescape(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	s = strings.TrimSuffix(s[1:], `"`)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// taint marks the symbol currently being computed as depending on something
// only a compiler could answer.
func (ev *Evaluation) taint() {
	if n := len(ev.stack); n > 0 {
		ev.st(ev.stack[n-1]).tainted = true
	}
}

func (ev *Evaluation) st(s *KSym) *symState {
	st := ev.state[s]
	if st == nil {
		st = &symState{}
		ev.state[s] = st
	}
	return st
}

// resolve maps a name inside an expression to a symbol. nil means a constant
// or an undefined symbol, whose value is its own name.
func (ev *Evaluation) resolve(name string) *KSym {
	if strings.HasPrefix(name, "\x00choice") {
		for _, c := range ev.m.Choices {
			if symRef(c) == name {
				return c
			}
		}
	}
	s := ev.m.Syms[name]
	if s == nil || (s.Type == Unknown && !s.IsChoice) {
		return nil
	}
	return s
}

func (ev *Evaluation) tri(e *Expr) Tri {
	if e == nil {
		return Yes
	}
	switch e.Op {
	case ExprSym:
		if s := ev.resolve(e.Sym); s != nil && !e.IsLit {
			ev.calc(s)
			return ev.st(s).tri
		}
		switch e.Sym {
		case "y":
			return Yes
		case "m":
			return Mod
		}
		if strings.HasPrefix(e.Sym, "$(") {
			ev.taint()
		}
		return No
	case ExprNot:
		return Yes - ev.tri(e.Args[0])
	case ExprAnd:
		return triMin(ev.tri(e.Args[0]), ev.tri(e.Args[1]))
	case ExprOr:
		return triMax(ev.tri(e.Args[0]), ev.tri(e.Args[1]))
	case ExprEq, ExprNeq, ExprLt, ExprLe, ExprGt, ExprGe:
		c := ev.compare(e)
		var ok bool
		switch e.Op {
		case ExprEq:
			ok = c == 0
		case ExprNeq:
			ok = c != 0
		case ExprLt:
			ok = c < 0
		case ExprLe:
			ok = c <= 0
		case ExprGt:
			ok = c > 0
		case ExprGe:
			ok = c >= 0
		}
		if ok {
			return Yes
		}
		return No
	}
	return No
}

// operand returns a comparison side's string value and Kconfig type.
func (ev *Evaluation) operand(name string, lit bool) (string, Type) {
	if !lit {
		if s := ev.resolve(name); s != nil {
			ev.calc(s)
			return ev.str(s), s.Type
		}
	}
	return name, Unknown
}

// compare follows expr_calc_value: if either side is not a string symbol,
// both are parsed as numbers of their types, and compared numerically when
// both parse; otherwise they are compared as strings.
func (ev *Evaluation) compare(e *Expr) int {
	s1, t1 := ev.operand(e.Sym, false)
	s2, t2 := ev.operand(e.Lit, e.IsLit)
	if t1 != String || t2 != String {
		v1, k1 := parseNum(s1, t1)
		v2, k2 := parseNum(s2, t2)
		if k1 && k2 {
			switch {
			case v1 < v2:
				return -1
			case v1 > v2:
				return 1
			}
			return 0
		}
	}
	return strings.Compare(s1, s2)
}

func parseNum(s string, t Type) (int64, bool) {
	switch t {
	case Bool, Tristate:
		switch s {
		case "n":
			return 0, true
		case "m":
			return 1, true
		case "y":
			return 2, true
		}
		return -1, true
	case Int:
		v, err := strconv.ParseInt(s, 10, 64)
		return v, err == nil
	case Hex:
		v, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X"), 16, 64)
		return int64(v), err == nil && s != ""
	default:
		v, err := strconv.ParseInt(s, 0, 64)
		return v, err == nil
	}
}

func (ev *Evaluation) str(s *KSym) string {
	st := ev.st(s)
	switch s.Type {
	case Bool, Tristate:
		return [...]string{"n", "m", "y"}[st.tri]
	}
	return st.str
}

func (ev *Evaluation) visibility(s *KSym) {
	st := ev.st(s)
	t := No
	for _, p := range s.Props {
		if p.Kind == PropPrompt {
			t = triMax(t, ev.tri(p.Vis))
		}
	}
	if t == Mod && (s.Type != Tristate || ev.modules == No) {
		t = Yes
	}
	st.visible = t
	if s.Choice != nil {
		return
	}
	r := No
	if s.RevDep != nil {
		r = ev.tri(s.RevDep)
	}
	if r == Mod && s.Type == Bool {
		r = Yes
	}
	st.revDep = r
	im := No
	if s.Implied != nil {
		im = ev.tri(s.Implied)
	}
	if im == Mod && s.Type == Bool {
		im = Yes
	}
	st.implied = im
}

func (ev *Evaluation) defaultProp(s *KSym) *Prop {
	for _, p := range s.Props {
		if p.Kind == PropDefault && ev.tri(p.Vis) != No {
			return p
		}
	}
	return nil
}

// calc is sym_calc_value.
func (ev *Evaluation) calc(s *KSym) {
	st := ev.st(s)
	if st.valid {
		return
	}
	if s.Choice != nil {
		ev.calc(s.Choice)
	}
	st.valid = true
	ev.stack = append(ev.stack, s)
	defer func() { ev.stack = ev.stack[:len(ev.stack)-1] }()
	st.tri, st.str = No, ""
	if s.Type != Bool && s.Type != Tristate && s.Type != String && s.Type != Int && s.Type != Hex {
		return
	}
	st.write = false
	ev.visibility(s)
	if st.visible != No {
		st.write = true
	}

	tri, str := No, ""
	switch s.Type {
	case Bool, Tristate:
		if s.Choice != nil && st.visible == Yes {
			if ev.st(s.Choice).sel == s {
				tri = Yes
			}
			break
		}
		if st.visible != No && st.user != nil {
			tri = triMin(st.user.tri, st.visible)
		} else {
			if st.revDep != No {
				st.write = true
			}
			if !s.IsChoice {
				if p := ev.defaultProp(s); p != nil {
					tri = triMin(ev.tri(p.Value), ev.tri(p.Vis))
					if tri != No {
						st.write = true
					}
				}
				if st.implied != No {
					st.write = true
					tri = triMax(tri, st.implied)
				}
			}
		}
		tri = triMax(tri, st.revDep)
		if tri == Mod && (s.Type == Bool || st.implied == Yes) {
			tri = Yes
		}
	default:
		if st.visible != No && st.user != nil {
			str = st.user.str
			break
		}
		if p := ev.defaultProp(s); p != nil {
			st.write = true
			str = ev.valueString(p.Value)
		}
	}
	st.tri, st.str = tri, str

	if s.IsChoice {
		if tri == Yes {
			st.sel = ev.calcChoice(s)
		}
		for _, mbr := range s.Members {
			if st.write && ev.st(mbr).visible != No {
				ev.st(mbr).write = true
			}
		}
	}
	if s.Auto {
		st.write = false
	}
}

// valueString is a string default's value: a constant, or the value of the
// symbol it names.
func (ev *Evaluation) valueString(e *Expr) string {
	if e.Op == ExprSym {
		if s := ev.resolve(e.Sym); s != nil && !e.IsLit {
			ev.calc(s)
			return ev.str(s)
		}
		if strings.HasPrefix(e.Sym, "$(") {
			ev.taint()
		}
		return e.Sym
	}
	return [...]string{"n", "m", "y"}[ev.tri(e)]
}

// calcChoice is sym_calc_choice: the member the defconfig chose if it is
// visible, else the first visible default, else the first visible member.
func (ev *Evaluation) calcChoice(c *KSym) *KSym {
	for _, mbr := range c.Members {
		ev.visibility(mbr)
	}
	cs := ev.st(c)
	if u := cs.selUser; u != nil && ev.st(u).visible != No {
		return u
	}
	for _, p := range c.Props {
		if p.Kind != PropDefault || ev.tri(p.Vis) == No {
			continue
		}
		if d := ev.m.Syms[p.Value.Sym]; d != nil && ev.st(d).visible != No {
			return d
		}
	}
	for _, mbr := range c.Members {
		if ev.st(mbr).visible != No {
			return mbr
		}
	}
	cs.tri = No
	return nil
}

// Config returns what conf_write would write: every symbol flagged for
// writing, in menu order, as its .config line value — "y", "n" for
// "is not set", or the escaped quoted string.
func (ev *Evaluation) Config() map[string]string {
	out := map[string]string{}
	var walk func(e *Entry)
	walk = func(e *Entry) {
		if s := e.Sym; s != nil && !s.IsChoice && s.Name != "" {
			ev.calc(s)
			st := ev.st(s)
			name := ev.m.Prefix + s.Name
			if _, done := out[name]; !done && st.write {
				switch s.Type {
				case Bool, Tristate:
					out[name] = ev.str(s)
				case String:
					out[name] = `"` + escape(st.str) + `"`
				case Int, Hex:
					out[name] = st.str
				}
			}
		} else if s != nil && s.IsChoice {
			ev.calc(s)
		}
		for _, c := range e.Children {
			walk(c)
		}
	}
	walk(ev.m.Root)
	return out
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// Tainted lists the symbols whose value depended on a macro call,
// sorted. Their values are what Silt would compute with every compiler probe
// answering no; kbuild will answer some of them yes.
func (ev *Evaluation) Tainted() []string {
	var out []string
	for s, st := range ev.state {
		if st.tainted && s.Name != "" && !s.IsChoice {
			out = append(out, ev.m.Prefix+s.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Names returns the written symbols sorted, for diffable output.
func Names(cfg map[string]string) []string {
	out := make([]string, 0, len(cfg))
	for k := range cfg {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Explain describes how a symbol's value was reached, for debugging a
// disagreement with kbuild.
func (ev *Evaluation) Explain(name string) string {
	s := ev.m.Syms[name]
	if s == nil {
		return name + ": unknown"
	}
	ev.calc(s)
	st := ev.st(s)
	var b strings.Builder
	fmtTri := func(t Tri) string { return [...]string{"n", "m", "y"}[t] }
	b.WriteString(name + " = " + ev.str(s) + " write=" + strconv.FormatBool(st.write) +
		" visible=" + fmtTri(st.visible) + " rev_dep=" + fmtTri(st.revDep) + "\n")
	for _, p := range s.Props {
		kind := [...]string{"prompt", "default", "select", "imply"}[p.Kind]
		b.WriteString("  " + kind + " " + p.Value.String() + " vis=" + fmtTri(ev.tri(p.Vis)) +
			" at " + p.Entry.File + ":" + strconv.Itoa(p.Entry.Line) + "\n")
	}
	if s.RevDep != nil {
		var walk func(e *Expr)
		walk = func(e *Expr) {
			if e.Op == ExprOr {
				walk(e.Args[0])
				walk(e.Args[1])
				return
			}
			if t := ev.tri(e); t != No {
				b.WriteString("  selected by " + e.String() + "\n")
			}
		}
		walk(s.RevDep)
	}
	if s.Choice != nil {
		cs := ev.st(s.Choice)
		sel := "(none)"
		if cs.sel != nil {
			sel = cs.sel.Name
		}
		b.WriteString("  choice value " + fmtTri(cs.tri) + " selects " + sel + "\n")
	}
	return b.String()
}
