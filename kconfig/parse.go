package kconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Options controls the import.
//
// Env is explicit rather than read from the process environment. The shape of
// the Kconfig tree itself depends on it — Buildroot's top-level Config.in does
// `source "$BR2_BASE_DIR/.br2-external.in.paths"` — so leaving it implicit
// would make the imported model depend on invisible state (DESIGN.md §14.2).
type Options struct {
	Root string            // directory the tree is rooted at
	Env  map[string]string // values for option env= and $VAR in source paths
	// Prefix is what conf puts in front of a symbol's name when it writes a
	// .config, and strips when it reads one. Buildroot builds its conf with
	// the prefix emptied and spells BR2_ in the Kconfig itself; the kernel
	// declares EXT4_FS and writes CONFIG_EXT4_FS. Reading a kernel defconfig
	// without this matched nothing at all.
	Prefix string
}

// Load parses a Kconfig tree starting at file, relative to opts.Root.
func Load(file string, opts Options) (*Tree, error) {
	if opts.Env == nil {
		opts.Env = map[string]string{}
	}
	t := newTree(opts.Root)
	t.Env = opts.Env
	p := &parser{tree: t, opts: opts, seen: map[string]bool{}}
	if err := p.file(file, nil); err != nil {
		return nil, err
	}
	// A symbol declared twice depends on either declaration's conditions,
	// not both. Conjoining them turned Buildroot's
	//
	//	if BR2_PACKAGE_BUSYBOX / config BR2_PACKAGE_BUSYBOX_SHOW_OTHERS
	//	if !BR2_PACKAGE_BUSYBOX / config BR2_PACKAGE_BUSYBOX_SHOW_OTHERS
	//
	// into BUSYBOX && !BUSYBOX, which is unsatisfiable; systemd selects the
	// symbol, so every systemd image came back UNSAT from solve and complete.
	for _, s := range t.Symbols {
		for i, d := range s.decls {
			if i == 0 {
				s.Depends = d
				continue
			}
			s.Depends = Or(s.Depends, d)
		}
	}
	return t, nil
}

type parser struct {
	tree *Tree
	opts Options
	seen map[string]bool
}

// scope is one enclosing "if" or "menu" block. Kconfig folds these into every
// symbol inside them, which is why an enclosing guard is a prerequisite and
// never derivable from the symbol itself (DESIGN.md §12.1).
type scope struct {
	cond *Expr
}

// file parses one Kconfig file under an enclosing condition.
//
// The guard must be threaded through `source`, because Kconfig scoping is
// lexical across file boundaries: `if COND / source "x" / endif` puts
// everything in x inside COND. Resetting scopes per file drops that guard and
// makes a choice group's at-least-one clause fire unconditionally, which
// forces unrelated symbols false at decision level zero.
func (p *parser) file(rel string, outer *Expr) error {
	abs := filepath.Join(p.opts.Root, rel)
	key := abs + "|" + outer.String()
	if p.seen[key] {
		return nil // legitimately re-sourced under the same condition
	}
	p.seen[key] = true

	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return p.lines(rel, joinContinuations(strings.Split(string(data), "\n")), outer)
}

func (p *parser) lines(file string, lines []string, outer *Expr) error {
	scopes := []scope{{cond: outer}}
	var cur *Symbol
	var choice *ChoiceGroup
	// A comment block takes its own depends-on lines. They belong to the
	// comment's visibility and to nothing else, so they must not land on the
	// preceding symbol or the enclosing choice.
	inComment := false

	guard := func() *Expr {
		var e *Expr
		for _, s := range scopes {
			e = And(e, s.cond)
		}
		return e
	}

	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(stripComment(raw))
		if line == "" {
			continue
		}
		kw, rest := cut(line)

		switch kw {
		case "config", "menuconfig":
			inComment = false
			name := firstField(rest)
			cur = p.tree.Get(name)
			cur.File, cur.Line = file, i+1
			cur.decls = append(cur.decls, guard())
			if choice != nil {
				cur.Choice = choice.Name
				choice.Members = append(choice.Members, name)
			}

		case "bool", "tristate", "string", "int", "hex", "def_bool", "def_tristate":
			if cur == nil {
				continue
			}
			cur.Type = typeOf(kw)
			if pr := strings.TrimSpace(rest); pr != "" {
				if strings.HasPrefix(kw, "def_") {
					// def_bool VALUE [if COND] is a type plus a default
					if err := p.addDefault(cur, pr); err != nil {
						return posErr(file, i, err)
					}
				} else {
					cur.Prompt = strings.Trim(firstQuoted(pr), `"`)
				}
			}

		case "prompt":
			if cur != nil {
				cur.Prompt = strings.Trim(firstQuoted(rest), `"`)
			}

		case "depends":
			if inComment {
				continue
			}
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "on"))
			e, err := parseExpr(rest)
			if err != nil {
				return posErr(file, i, err)
			}
			switch {
			case cur != nil:
				last := len(cur.decls) - 1
				cur.decls[last] = And(cur.decls[last], e)
			case choice != nil:
				choice.Depends = And(choice.Depends, e)
			}

		case "select", "imply":
			if cur == nil {
				continue
			}
			target, cond, err := parseSelect(rest)
			if err != nil {
				return posErr(file, i, err)
			}
			s := Select{Target: target, Cond: cond}
			if kw == "select" {
				cur.Selects = append(cur.Selects, s)
			} else {
				cur.Implies = append(cur.Implies, s)
			}

		case "default":
			// A choice block has its own `default MEMBER`, naming which member
			// is selected when nothing else decides. cur is nil there, so
			// dropping the line would lose 190 defaults across Buildroot.
			switch {
			case cur != nil:
				if err := p.addDefault(cur, rest); err != nil {
					return posErr(file, i, err)
				}
			case choice != nil:
				val, condStr := splitIf(rest)
				var cond *Expr
				if condStr != "" {
					e, err := parseExpr(condStr)
					if err != nil {
						return posErr(file, i, err)
					}
					cond = e
				}
				choice.Defaults = append(choice.Defaults,
					Default{Value: strings.TrimSpace(val), Cond: cond})
			}

		case "range":
			if cur == nil {
				continue
			}
			f := strings.Fields(rest)
			if len(f) >= 2 {
				cur.RangeLo, cur.RangeHi = f[0], f[1]
			}

		case "option":
			if cur == nil {
				continue
			}
			if v := strings.TrimSpace(rest); strings.HasPrefix(v, "env=") {
				cur.EnvVar = strings.Trim(strings.TrimPrefix(v, "env="), `"`)
			}

		case "help", "---help---":
			i = skipHelp(lines, i)

		case "if":
			inComment = false
			e, err := parseExpr(rest)
			if err != nil {
				return posErr(file, i, err)
			}
			scopes = append(scopes, scope{cond: e})
			cur = nil

		case "endif", "endmenu":
			inComment = false
			if len(scopes) > 0 {
				scopes = scopes[:len(scopes)-1]
			}
			cur = nil

		case "menu":
			inComment = false
			// A menu's own visibility does not gate its contents the way "if"
			// does; only "visible if" and "depends on" inside it do. Push an
			// empty scope so endmenu stays balanced.
			scopes = append(scopes, scope{})
			cur = nil

		case "visible":
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), "if"))
			e, err := parseExpr(rest)
			if err != nil {
				return posErr(file, i, err)
			}
			if len(scopes) > 0 {
				scopes[len(scopes)-1].cond = And(scopes[len(scopes)-1].cond, e)
			}

		case "choice":
			inComment = false
			choice = &ChoiceGroup{
				Name:    fmt.Sprintf("choice@%s:%d", file, i+1),
				Depends: guard(),
				File:    file,
				Line:    i + 1,
			}
			p.tree.Choices = append(p.tree.Choices, choice)
			cur = nil

		case "endchoice":
			inComment = false
			choice = nil
			cur = nil

		case "optional":
			if choice != nil {
				choice.Optional = true
			}

		case "source":
			path := p.expand(strings.Trim(strings.TrimSpace(rest), `"`))
			if strings.Contains(path, "$") {
				// An unresolved variable means the caller did not supply the
				// environment. Skipping silently would make the import depend
				// on invisible state, so it is an error.
				return fmt.Errorf("%s:%d: source path %q needs an environment value not provided",
					file, i+1, path)
			}
			if err := p.file(path, guard()); err != nil {
				// Generated files (br2-external) legitimately may not exist.
				if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
					continue
				}
				return err
			}

		case "comment":
			// A comment contributes no symbol, but it may carry depends-on
			// lines of its own. Leaving cur and choice alone made those lines
			// attach to whatever came before: BR2_STATIC_LIBS picked up
			// "depends on BR2_TOOLCHAIN_USES_GLIBC" from the comment after it,
			// giving it !GLIBC && GLIBC — a contradiction that made assuming
			// that symbol instantly unsatisfiable.
			inComment = true
			cur = nil

		case "mainmenu":
			// no constraint content
		}
	}
	return nil
}

// joinContinuations folds backslash-continued lines into one, keeping the
// slice length stable so reported line numbers still point at the first line
// of the statement.
func joinContinuations(lines []string) []string {
	out := make([]string, len(lines))
	copy(out, lines)
	for i := 0; i < len(out); i++ {
		j := i
		for {
			trimmed := strings.TrimRight(out[i], " \t")
			if !strings.HasSuffix(trimmed, "\\") || j+1 >= len(out) {
				break
			}
			j++
			out[i] = trimmed[:len(trimmed)-1] + " " + strings.TrimSpace(out[j])
			out[j] = ""
		}
	}
	return out
}

func (p *parser) addDefault(s *Symbol, rest string) error {
	val, condStr := splitIf(rest)
	var cond *Expr
	if condStr != "" {
		e, err := parseExpr(condStr)
		if err != nil {
			return err
		}
		cond = e
	}
	s.Defaults = append(s.Defaults, Default{Value: strings.TrimSpace(val), Cond: cond})
	return nil
}

func (p *parser) expand(s string) string { return expandVars(s, p.opts.Env) }

// expandVars substitutes environment variables written $VAR, ${VAR} or
// $(VAR). The last spelling is the kernel's: Buildroot's Kconfig uses option
// env and $VAR, the kernel uses $(SRCARCH) and friends, and both trees have to
// be importable by the same parser.
//
// A $(name,args) call is a macro, not a variable, and is left alone: kbuild
// evaluates those by running the compiler.
func expandVars(s string, env map[string]string) string {
	for k, v := range env {
		s = strings.ReplaceAll(s, "$("+k+")", v)
		s = strings.ReplaceAll(s, "${"+k+"}", v)
		s = strings.ReplaceAll(s, "$"+k, v)
	}
	return s
}

func parseSelect(rest string) (string, *Expr, error) {
	target, condStr := splitIf(rest)
	target = firstField(target)
	if condStr == "" {
		return target, nil, nil
	}
	e, err := parseExpr(condStr)
	return target, e, err
}

// splitIf divides "VALUE if COND" at the top-level "if".
func splitIf(s string) (string, string) {
	depth, inStr := 0, false
	for i := 0; i+2 <= len(s); i++ {
		switch s[i] {
		case '"':
			inStr = !inStr
		case '(':
			if !inStr {
				depth++
			}
		case ')':
			if !inStr {
				depth--
			}
		}
		if inStr || depth != 0 {
			continue
		}
		if s[i] == 'i' && i+1 < len(s) && s[i+1] == 'f' &&
			(i == 0 || s[i-1] == ' ' || s[i-1] == '\t') &&
			(i+2 == len(s) || s[i+2] == ' ' || s[i+2] == '\t') {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:])
		}
	}
	return strings.TrimSpace(s), ""
}

// skipHelp consumes a help block, which ends at the first line indented less
// than the help text itself.
func skipHelp(lines []string, i int) int {
	j := i + 1
	for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
		j++
	}
	if j >= len(lines) {
		return j
	}
	base := indentOf(lines[j])
	for ; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		if indentOf(lines[j]) < base {
			return j - 1
		}
	}
	return j - 1
}

func indentOf(s string) int {
	n := 0
	for _, c := range s {
		switch c {
		case ' ':
			n++
		case '\t':
			n += 8
		default:
			return n
		}
	}
	return n
}

// stripComment removes a trailing # comment that is not inside a string.
func stripComment(s string) string {
	inStr := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inStr = !inStr
		case '#':
			if !inStr {
				return s[:i]
			}
		}
	}
	return s
}

// cut splits a statement into keyword and remainder.
//
// A quote ends the keyword as surely as a space does: Buildroot contains
// `bool"selftests"` with no separator, and treating that as one token silently
// drops the symbol's type.
func cut(line string) (string, string) {
	i := strings.IndexAny(line, " \t\"")
	if i < 0 {
		return line, ""
	}
	if line[i] == '"' {
		return line[:i], strings.TrimSpace(line[i:])
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

func firstField(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

func firstQuoted(s string) string {
	if i := strings.IndexByte(s, '"'); i >= 0 {
		if j := strings.IndexByte(s[i+1:], '"'); j >= 0 {
			return s[i : i+j+2]
		}
	}
	return s
}

func typeOf(kw string) Type {
	switch kw {
	case "bool", "def_bool":
		return Bool
	case "tristate", "def_tristate":
		return Tristate
	case "string":
		return String
	case "int":
		return Int
	case "hex":
		return Hex
	}
	return Unknown
}

func posErr(file string, i int, err error) error {
	return fmt.Errorf("%s:%d: %w", file, i+1, err)
}
