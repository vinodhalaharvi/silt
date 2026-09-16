// Package kconfig imports Kconfig source into Silt's representation.
//
// It implements the language as the C parser behaves, not as the
// documentation describes (DESIGN.md §8.1): select is recorded separately from
// depends so both the implementation-faithful and spec-faithful readings stay
// expressible over the same imported data.
package kconfig

import (
	"fmt"
	"strings"
)

// ExprOp is the node type of a Kconfig expression.
type ExprOp uint8

const (
	ExprSym ExprOp = iota // a symbol, or the constants y/n/m
	ExprNot
	ExprAnd
	ExprOr
	ExprEq  // sym = "literal" or sym = sym
	ExprNeq // sym != ...
)

// Expr is a Kconfig condition.
type Expr struct {
	Op    ExprOp
	Sym   string
	Lit   string // right-hand side of = and !=
	IsLit bool   // right-hand side was a quoted literal
	Args  []*Expr
}

func (e *Expr) String() string {
	if e == nil {
		return "y"
	}
	switch e.Op {
	case ExprSym:
		return e.Sym
	case ExprNot:
		return "!" + e.Args[0].String()
	case ExprAnd:
		return "(" + e.Args[0].String() + " && " + e.Args[1].String() + ")"
	case ExprOr:
		return "(" + e.Args[0].String() + " || " + e.Args[1].String() + ")"
	case ExprEq:
		return e.Sym + " = " + quoted(e.Lit, e.IsLit)
	case ExprNeq:
		return e.Sym + " != " + quoted(e.Lit, e.IsLit)
	}
	return "?"
}

func quoted(s string, isLit bool) string {
	if isLit {
		return `"` + s + `"`
	}
	return s
}

// Symbols returns every symbol mentioned, for dependency indexing.
func (e *Expr) Symbols(into map[string]bool) {
	if e == nil {
		return
	}
	switch e.Op {
	case ExprSym, ExprEq, ExprNeq:
		if e.Sym != "" && !isConst(e.Sym) {
			into[e.Sym] = true
		}
	}
	for _, a := range e.Args {
		a.Symbols(into)
	}
}

func isConst(s string) bool { return s == "y" || s == "n" || s == "m" }

// And joins two conditions, dropping nils. Used to fold enclosing "if" blocks
// and menu visibility into each symbol's own dependency.
func And(a, b *Expr) *Expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return &Expr{Op: ExprAnd, Args: []*Expr{a, b}}
}

// Or joins two conditions. Unlike And, a nil operand means "always", so it
// absorbs the other: y || x is y.
func Or(a, b *Expr) *Expr {
	if a == nil || b == nil {
		return nil
	}
	return &Expr{Op: ExprOr, Args: []*Expr{a, b}}
}

// parseExpr reads a Kconfig condition.
//
// Precedence, loosest first: || then && then ! then comparison.
type exprParser struct {
	toks []string
	pos  int
}

func parseExpr(s string) (*Expr, error) {
	toks, err := tokenizeExpr(s)
	if err != nil {
		return nil, err
	}
	if len(toks) == 0 {
		return nil, nil
	}
	p := &exprParser{toks: toks}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("trailing %q in expression %q", p.toks[p.pos], s)
	}
	return e, nil
}

func (p *exprParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *exprParser) or() (*Expr, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.peek() == "||" {
		p.pos++
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = &Expr{Op: ExprOr, Args: []*Expr{left, right}}
	}
	return left, nil
}

func (p *exprParser) and() (*Expr, error) {
	left, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.peek() == "&&" {
		p.pos++
		right, err := p.unary()
		if err != nil {
			return nil, err
		}
		left = &Expr{Op: ExprAnd, Args: []*Expr{left, right}}
	}
	return left, nil
}

func (p *exprParser) unary() (*Expr, error) {
	if p.peek() == "!" {
		p.pos++
		sub, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &Expr{Op: ExprNot, Args: []*Expr{sub}}, nil
	}
	return p.primary()
}

func (p *exprParser) primary() (*Expr, error) {
	t := p.peek()
	if t == "" {
		return nil, fmt.Errorf("unexpected end of expression")
	}
	if t == "(" {
		p.pos++
		e, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		return e, nil
	}
	p.pos++
	sym := t
	// Comparison against a symbol or a literal. 154 of these exist in
	// Buildroot inside real depends-on clauses, so they are not optional.
	if op := p.peek(); op == "=" || op == "!=" {
		p.pos++
		rhs := p.peek()
		if rhs == "" {
			return nil, fmt.Errorf("missing right-hand side of %s", op)
		}
		p.pos++
		isLit := strings.HasPrefix(rhs, `"`)
		kind := ExprEq
		if op == "!=" {
			kind = ExprNeq
		}
		return &Expr{Op: kind, Sym: sym, Lit: strings.Trim(rhs, `"`), IsLit: isLit}, nil
	}
	return &Expr{Op: ExprSym, Sym: strings.Trim(sym, `"`)}, nil
}

func tokenizeExpr(s string) ([]string, error) {
	var out []string
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '#':
			return out, nil // trailing comment
		case c == '(' || c == ')' || c == '!':
			if c == '!' && i+1 < len(s) && s[i+1] == '=' {
				out = append(out, "!=")
				i += 2
				continue
			}
			out = append(out, string(c))
			i++
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
			out = append(out, "&&")
			i += 2
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			out = append(out, "||")
			i += 2
		case c == '=':
			out = append(out, "=")
			i++
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string in expression")
			}
			out = append(out, s[i:j+1])
			i = j + 1
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t()!&|=#\"", rune(s[j])) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out, nil
}
