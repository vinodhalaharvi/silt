package component

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Symbol maps an imported interface name to the symbol policy names it by.
//
//	silt:modbus/read@0.1.0  →  SILT__MODBUS__READ
//	wasi:clocks/monotonic-clock@0.3.0  →  WASI__CLOCKS__MONOTONIC_CLOCK
//
// This mapping is permanent: every pack's policy is written in its output, so
// it is chosen to be unambiguous rather than pretty. The two structural
// separators become a double underscore and a hyphen a single one. WIT labels
// cannot contain a double hyphen, so no two interface names map to one symbol;
// a single underscore for everything would map a-b:c/d and a:b-c/d to the
// same symbol, and a policy forbidding one would silently forbid the other.
//
// The version is dropped. A policy says which interfaces a component may
// reach, not which releases of them; the exact bytes are pinned by the
// solution hash, not by the symbol.
//
// A name that is not an interface — a plain function or instance name — has
// no symbol, and the caller reports it rather than guessing: policy written
// against interfaces cannot reason about it.
func Symbol(name string) (string, error) {
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	colon := strings.IndexByte(name, ':')
	slash := strings.IndexByte(name, '/')
	if colon <= 0 || slash <= colon+1 || slash == len(name)-1 ||
		strings.Count(name, ":") != 1 || strings.Count(name, "/") != 1 {
		return "", fmt.Errorf("%q is not a WIT interface name (namespace:package/interface)", name)
	}
	var b strings.Builder
	for i, r := range name {
		switch {
		case r == ':' || r == '/':
			b.WriteString("__")
		case r == '-':
			if i > 0 && name[i-1] == '-' {
				return "", fmt.Errorf("%q has a double hyphen, which WIT labels cannot", name)
			}
			b.WriteByte('_')
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			return "", fmt.Errorf("%q has %q, which WIT labels cannot", name, r)
		}
	}
	return b.String(), nil
}

// Interface is one interface a pack's WIT defines: the vocabulary a policy
// may name.
type Interface struct {
	Name   string // namespace:package/interface, without version
	Symbol string
	File   string
	Line   int
}

// Vocabulary reads every interface defined under the given WIT files and
// directories.
//
// This is a scan for two declarations, not a WIT parser: a package's name,
// and the interfaces declared directly in it. Everything else — worlds,
// functions, types, use and include — says nothing about which interfaces
// exist and is skipped by brace depth. An inline interface inside a world is
// deeper than its package and is therefore not vocabulary, which is correct:
// nothing can import it by name.
func Vocabulary(paths []string) (map[string]Interface, error) {
	var files []string
	for _, p := range paths {
		err := filepath.Walk(p, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && strings.HasSuffix(path, ".wit") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no .wit files under %s", strings.Join(paths, ", "))
	}

	out := map[string]Interface{}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		found, err := scanWIT(string(src), file)
		if err != nil {
			return nil, err
		}
		for _, it := range found {
			if prev, ok := out[it.Symbol]; ok && prev.Name != it.Name {
				return nil, fmt.Errorf("%s:%d: %s and %s (%s:%d) map to one symbol %s",
					it.File, it.Line, it.Name, prev.Name, prev.File, prev.Line, it.Symbol)
			}
			if _, ok := out[it.Symbol]; !ok {
				out[it.Symbol] = it
			}
		}
	}
	return out, nil
}

// scanWIT finds package names and the interfaces declared directly in them.
func scanWIT(src, file string) ([]Interface, error) {
	toks := tokenize(src)
	var out []Interface

	// filePkg is set by `package a:b;`. A `package a:b { ... }` block
	// pushes a nested package whose interfaces sit one level deeper.
	var filePkg string
	type nested struct {
		name  string
		depth int
	}
	var stack []nested
	depth := 0
	pkgAt := func() (string, int) {
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			return top.name, top.depth
		}
		return filePkg, 0
	}

	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.text {
		case "{":
			depth++
			continue
		case "}":
			depth--
			if len(stack) > 0 && depth < stack[len(stack)-1].depth {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if i+1 >= len(toks) {
			break
		}
		next := toks[i+1]
		switch t.text {
		case "package":
			name := next.text
			if at := strings.IndexByte(name, '@'); at >= 0 {
				name = name[:at]
			}
			if i+2 < len(toks) && toks[i+2].text == "{" {
				stack = append(stack, nested{name: name, depth: depth + 1})
			} else if depth == 0 {
				filePkg = name
			}
		case "interface":
			pkg, want := pkgAt()
			if depth != want || i+2 >= len(toks) || toks[i+2].text != "{" {
				continue
			}
			if pkg == "" {
				return nil, fmt.Errorf("%s:%d: interface %s is outside any package",
					file, t.line, next.text)
			}
			name := pkg + "/" + strings.TrimPrefix(next.text, "%")
			sym, err := Symbol(name)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", file, t.line, err)
			}
			out = append(out, Interface{Name: name, Symbol: sym, File: file, Line: t.line})
		}
	}
	return out, nil
}

type token struct {
	text string
	line int
}

// tokenize splits WIT into words and the punctuation that matters for depth,
// dropping comments. A word runs until whitespace or one of {};,()<>=.
func tokenize(src string) []token {
	var out []token
	line := 1
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case strings.HasPrefix(src[i:], "/*"):
			i += 2
			for i < len(src) && !strings.HasPrefix(src[i:], "*/") {
				if src[i] == '\n' {
					line++
				}
				i++
			}
			i += 2
		case strings.IndexByte("{};,()<>=", c) >= 0:
			out = append(out, token{string(c), line})
			i++
		default:
			start := i
			for i < len(src) && !strings.ContainsRune(" \t\r\n{};,()<>=", rune(src[i])) &&
				!strings.HasPrefix(src[i:], "//") && !strings.HasPrefix(src[i:], "/*") {
				i++
			}
			out = append(out, token{src[start:i], line})
		}
	}
	return out
}
