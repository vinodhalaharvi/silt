package kconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file builds the menu model that kbuild itself evaluates.
//
// The Tree in parse.go is shaped for lowering to CNF: one merged dependency
// per symbol, selects and defaults with their own conditions only. That is
// enough to ask whether a configuration can exist. It is not enough to say
// what kbuild will make of a defconfig, because kbuild's answer depends on
// things the CNF view throws away: which declaration a prompt or default came
// from, what its enclosing menus required, and whether a prompt is visible at
// all. Those are kept here, per property, in the shape of
// support/kconfig/menu.c, and finalised the way menu_finalize does.

// PropKind is the kind of a symbol property.
type PropKind uint8

const (
	PropPrompt PropKind = iota
	PropDefault
	PropSelect
	PropImply
)

// Prop is one property line, belonging to one declaration of a symbol.
type Prop struct {
	Kind  PropKind
	Value *Expr  // default value, or the selected symbol as an ExprSym
	Cond  *Expr  // its own "if"
	Vis   *Expr  // finalised: the declaration's dependency && Cond
	Entry *Entry // the declaration it belongs to
}

// EntryKind is the kind of a menu node.
type EntryKind uint8

const (
	EntryConfig EntryKind = iota
	EntryMenu
	EntryIf
	EntryChoice
	EntryComment
)

// Entry is a node of the menu tree: a config declaration, a menu, an if
// block, a choice or a comment.
type Entry struct {
	Kind     EntryKind
	Sym      *KSym // config and choice entries
	Parent   *Entry
	Children []*Entry
	OwnDep   *Expr // depends on lines and, for if, the condition
	Dep      *Expr // finalised: parent dependency && OwnDep
	Prompt   *Prop
	// Visibility is a menu's "visible if". It gates the prompts of entries
	// inside the menu, not their dependencies.
	Visibility *Expr
	File       string
	Line       int
}

// KSym is a symbol as kbuild evaluates it.
type KSym struct {
	Name     string // empty for a choice
	Type     Type
	Props    []*Prop
	Entries  []*Entry
	DirDep   *Expr // or over declarations' dependencies
	RevDep   *Expr // or over selectors
	Implied  *Expr
	IsChoice bool
	Optional bool    // choice marked optional
	Choice   *KSym   // for a choice member, its choice
	Members  []*KSym // for a choice
	Env      string  // option env=
	Auto     bool    // never written: choices, env and defconfig_list symbols
}

// Menu is a Kconfig tree as kbuild evaluates it.
type Menu struct {
	root    string
	Root    *Entry
	Syms    map[string]*KSym
	Choices []*KSym
	Env     map[string]string
}

// LoadMenu parses a Kconfig tree into the menu model.
func LoadMenu(file string, opts Options) (*Menu, error) {
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	m := &Menu{root: opts.Root, Syms: map[string]*KSym{}, Env: opts.Env, Root: &Entry{Kind: EntryMenu}}
	p := &menuParser{m: m, opts: opts}
	if err := p.file(file, m.Root); err != nil {
		return nil, err
	}
	m.finalize(m.Root, nil)
	return m, nil
}

func (m *Menu) sym(name string) *KSym {
	if s, ok := m.Syms[name]; ok {
		return s
	}
	s := &KSym{Name: name}
	m.Syms[name] = s
	return s
}

type menuParser struct {
	m    *Menu
	opts Options
}

// file parses one Kconfig file as children of parent. Unlike the CNF
// importer, a file is not keyed on its guard: sourcing the same file twice
// declares its symbols twice, which is exactly what kbuild does.
func (p *menuParser) file(rel string, parent *Entry) error {
	data, err := os.ReadFile(filepath.Join(p.opts.Root, rel))
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return p.lines(rel, joinContinuations(strings.Split(string(data), "\n")), parent)
}

func (p *menuParser) lines(file string, lines []string, top *Entry) error {
	// stack holds the open menu, if and choice blocks; its last element is
	// where new entries go. cur is the entry whose attributes are being read.
	stack := []*Entry{top}
	var cur *Entry
	add := func(e *Entry) {
		parent := stack[len(stack)-1]
		e.Parent = parent
		parent.Children = append(parent.Children, e)
		cur = e
	}
	expr := func(i int, s string) (*Expr, error) {
		e, err := parseExpr(s)
		if err != nil {
			return nil, posErr(file, i, err)
		}
		return e, nil
	}

	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(stripComment(lines[i]))
		if line == "" {
			continue
		}
		kw, rest := cut(line)
		switch kw {
		case "config", "menuconfig":
			s := p.m.sym(firstField(rest))
			e := &Entry{Kind: EntryConfig, Sym: s, File: file, Line: i + 1}
			add(e)
			s.Entries = append(s.Entries, e)

		case "choice":
			s := &KSym{IsChoice: true, Auto: true}
			p.m.Choices = append(p.m.Choices, s)
			e := &Entry{Kind: EntryChoice, Sym: s, File: file, Line: i + 1}
			add(e)
			s.Entries = append(s.Entries, e)
			stack = append(stack, e)

		case "endchoice", "endmenu", "endif":
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			cur = nil

		case "menu":
			e := &Entry{Kind: EntryMenu, File: file, Line: i + 1}
			add(e)
			e.Prompt = &Prop{Kind: PropPrompt, Entry: e}
			p.inheritVisibility(e.Prompt)
			stack = append(stack, e)

		case "if":
			c, err := expr(i, rest)
			if err != nil {
				return err
			}
			e := &Entry{Kind: EntryIf, OwnDep: c, File: file, Line: i + 1}
			add(e)
			stack = append(stack, e)
			cur = nil

		case "comment":
			e := &Entry{Kind: EntryComment, File: file, Line: i + 1}
			add(e)
			e.Prompt = &Prop{Kind: PropPrompt, Entry: e}

		case "source":
			path := p.expand(strings.Trim(strings.TrimSpace(rest), `"`))
			if strings.Contains(path, "$") {
				return fmt.Errorf("%s:%d: source path %q needs an environment value not provided", file, i+1, path)
			}
			if err := p.file(path, stack[len(stack)-1]); err != nil {
				if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
					continue
				}
				return err
			}
			cur = nil

		case "bool", "tristate", "string", "int", "hex", "def_bool", "def_tristate":
			if cur == nil || cur.Sym == nil {
				continue
			}
			cur.Sym.Type = typeOf(kw)
			pr := strings.TrimSpace(rest)
			if pr == "" {
				continue
			}
			if strings.HasPrefix(kw, "def_") {
				if err := p.property(cur, PropDefault, pr, file, i); err != nil {
					return err
				}
			} else if err := p.prompt(cur, pr, file, i); err != nil {
				return err
			}

		case "prompt":
			if cur != nil {
				if err := p.prompt(cur, rest, file, i); err != nil {
					return err
				}
			}

		case "default":
			if cur != nil && cur.Sym != nil {
				if err := p.property(cur, PropDefault, rest, file, i); err != nil {
					return err
				}
			}

		case "select", "imply":
			if cur != nil && cur.Sym != nil {
				k := PropSelect
				if kw == "imply" {
					k = PropImply
				}
				if err := p.property(cur, k, rest, file, i); err != nil {
					return err
				}
			}

		case "depends":
			if cur == nil {
				continue
			}
			c, err := expr(i, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "on")))
			if err != nil {
				return err
			}
			cur.OwnDep = And(cur.OwnDep, c)

		case "visible":
			if cur == nil || cur.Kind != EntryMenu {
				continue
			}
			c, err := expr(i, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "if")))
			if err != nil {
				return err
			}
			cur.Visibility = And(cur.Visibility, c)

		case "optional":
			if cur != nil && cur.Kind == EntryChoice {
				cur.Sym.Optional = true
			}

		case "option":
			if cur == nil || cur.Sym == nil {
				continue
			}
			v := strings.TrimSpace(rest)
			switch {
			case strings.HasPrefix(v, "env="):
				name := strings.Trim(strings.TrimPrefix(v, "env="), `"`)
				cur.Sym.Env, cur.Sym.Auto = name, true
				// kbuild gives the symbol a default of the variable's value.
				cur.Sym.Props = append(cur.Sym.Props, &Prop{Kind: PropDefault,
					Value: &Expr{Op: ExprSym, Sym: p.opts.Env[name]}, Entry: cur})
			case v == "defconfig_list":
				cur.Sym.Auto = true
			}

		case "help", "---help---":
			i = skipHelp(lines, i)

		case "range", "mainmenu":
			// range has two uses in Buildroot, both on symbols no shipped
			// defconfig sets out of range; mainmenu configures nothing.
		}
	}
	return nil
}

func contains(list []*KSym, s *KSym) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p *menuParser) expand(s string) string {
	for k, v := range p.opts.Env {
		s = strings.ReplaceAll(s, "$"+k, v)
		s = strings.ReplaceAll(s, "${"+k+"}", v)
	}
	return s
}

func (p *menuParser) prompt(e *Entry, rest, file string, i int) error {
	text, condStr := splitIf(rest)
	_ = text
	pr := &Prop{Kind: PropPrompt, Entry: e}
	if condStr != "" {
		c, err := parseExpr(condStr)
		if err != nil {
			return posErr(file, i, err)
		}
		pr.Cond = c
	}
	p.inheritVisibility(pr)
	e.Prompt = pr
	if e.Sym != nil {
		e.Sym.Props = append(e.Sym.Props, pr)
	}
	return nil
}

// inheritVisibility applies every enclosing menu's "visible if" to a prompt,
// as menu_add_prompt does. The loop starts at the entry's parent, so a menu's
// own visible-if does not gate its own prompt.
func (p *menuParser) inheritVisibility(pr *Prop) {
	for m := pr.Entry.Parent; m != nil; m = m.Parent {
		if m.Visibility != nil {
			pr.Cond = And(pr.Cond, m.Visibility)
		}
	}
}

func (p *menuParser) property(e *Entry, k PropKind, rest, file string, i int) error {
	val, condStr := splitIf(rest)
	pr := &Prop{Kind: k, Entry: e}
	var err error
	if k == PropSelect || k == PropImply {
		pr.Value = &Expr{Op: ExprSym, Sym: firstField(val)}
	} else if pr.Value, err = parseValue(val); err != nil {
		return posErr(file, i, err)
	}
	if condStr != "" {
		if pr.Cond, err = parseExpr(condStr); err != nil {
			return posErr(file, i, err)
		}
	}
	e.Sym.Props = append(e.Sym.Props, pr)
	return nil
}

// parseValue reads a default's value: a quoted constant or an expression.
func parseValue(s string) (*Expr, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && strings.IndexByte(s[1:], '"') == len(s)-2 {
		return &Expr{Op: ExprSym, Sym: s[1 : len(s)-1], IsLit: true}, nil
	}
	return parseExpr(s)
}

// finalize propagates dependencies down the tree, as menu_finalize does.
//
// Each entry's dependency is its parent's dependency and its own depends-on.
// For the members of a choice, the parent dependency is the choice symbol
// itself; for a menu, it is the menu prompt's visibility; for an if, it is
// the condition. Each property's visibility is its entry's dependency and its
// own condition. A symbol's direct dependency is the disjunction of its
// declarations', and every select adds (selector && visibility) to its
// target's reverse dependency.
func (m *Menu) finalize(e *Entry, parentDep *Expr) {
	e.Dep = And(parentDep, e.OwnDep)
	if e.Prompt != nil && e.Sym == nil {
		e.Prompt.Vis = And(e.Dep, e.Prompt.Cond)
	}
	if s := e.Sym; s != nil {
		for _, pr := range s.Props {
			if pr.Entry != e {
				continue
			}
			pr.Vis = And(e.Dep, pr.Cond)
			if pr.Kind == PropSelect || pr.Kind == PropImply {
				t := m.sym(pr.Value.Sym)
				term := And(&Expr{Op: ExprSym, Sym: symRef(s)}, pr.Vis)
				if pr.Kind == PropSelect {
					t.RevDep = orNil(t.RevDep, term)
				} else {
					t.Implied = orNil(t.Implied, term)
				}
			}
		}
		if e.Kind == EntryConfig {
			s.DirDep = orNil(s.DirDep, yesIfNil(e.Dep))
		}
	}

	var childDep *Expr
	switch e.Kind {
	case EntryChoice:
		childDep = &Expr{Op: ExprSym, Sym: symRef(e.Sym)}
	case EntryMenu:
		if e.Prompt != nil {
			childDep = e.Prompt.Vis
		} else {
			childDep = e.Dep
		}
	default:
		childDep = e.Dep
	}
	for _, c := range e.Children {
		m.finalize(c, childDep)
	}

	if e.Kind == EntryChoice {
		for _, mbr := range choiceMembers(e.Children) {
			if mbr.Sym.Choice == nil {
				mbr.Sym.Choice = e.Sym
				e.Sym.Members = append(e.Sym.Members, mbr.Sym)
			}
		}
	}

	// A non-optional choice cannot be set to n while its prompt is visible.
	if e.Kind == EntryChoice && !e.Sym.Optional && e.Prompt != nil {
		e.Sym.RevDep = orNil(e.Sym.RevDep, And(e.Prompt.Vis, &Expr{Op: ExprSym, Sym: "m"}))
	}
}

func orNil(a, b *Expr) *Expr {
	if a == nil {
		return b
	}
	return &Expr{Op: ExprOr, Args: []*Expr{a, b}}
}

func yesIfNil(e *Expr) *Expr {
	if e == nil {
		return &Expr{Op: ExprSym, Sym: "y"}
	}
	return e
}

// symRef names a symbol inside an expression. Choices have no name, so they
// are referred to by a key the evaluator resolves back to the choice.
func symRef(s *KSym) string {
	if s.IsChoice {
		return fmt.Sprintf("\x00choice%p", s)
	}
	return s.Name
}

// choiceMembers returns the config entries kbuild marks as values of a
// choice, reproducing the two tree rewrites menu_finalize performs first.
//
// If blocks are flattened, so a config inside an if inside a choice is a
// member. Taking only direct children made Buildroot's ARM CPU choice, whose
// members sit inside if BR2_arm, look like a choice of four, and picked
// cortex-A32 for a cortex-A5 board.
//
// Automatic submenus are not flattened: entries that follow a prompted config
// and depend on it become its children, and children are not members. The
// openssl choice sources package/libopenssl/Config.in right after
// BR2_PACKAGE_LIBOPENSSL, whose if BR2_PACKAGE_LIBOPENSSL block would
// otherwise make thirty openssl options values of the ssl-library choice.
func choiceMembers(list []*Entry) []*Entry {
	var out []*Entry
	for i := 0; i < len(list); i++ {
		e := list[i]
		switch e.Kind {
		case EntryConfig:
			out = append(out, e)
			if e.Prompt == nil {
				continue // kbuild undoes submenus under promptless symbols
			}
			for i+1 < len(list) && dependsOn(list[i+1], e.Sym.Name) {
				i++
			}
		case EntryIf:
			out = append(out, choiceMembers(e.Children)...)
		}
	}
	return out
}

// dependsOn is expr_depends_symbol: the entry's dependency has the symbol as a
// conjunct, as sym, sym = y or sym != n.
func dependsOn(e *Entry, name string) bool {
	dep := e.Dep
	if e.Prompt != nil && e.Prompt.Vis != nil {
		dep = e.Prompt.Vis
	}
	var conj func(x *Expr) bool
	conj = func(x *Expr) bool {
		if x == nil {
			return false
		}
		switch x.Op {
		case ExprAnd:
			return conj(x.Args[0]) || conj(x.Args[1])
		case ExprSym:
			return x.Sym == name && !x.IsLit
		case ExprEq:
			return x.Sym == name && x.Lit == "y"
		case ExprNeq:
			return x.Sym == name && x.Lit == "n"
		}
		return false
	}
	return conj(dep)
}
