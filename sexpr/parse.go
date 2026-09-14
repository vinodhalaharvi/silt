package sexpr

import "fmt"

// Parse reads every top-level S-expression in src. file is used for
// positions and may be empty.
func Parse(src, file string) ([]*Node, error) {
	p := &parser{lex: newLexer(src, file)}
	if err := p.fill(); err != nil {
		return nil, err
	}
	var out []*Node
	for p.tok.kind != tokEOF {
		n, err := p.parseNode()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// ParseOne reads exactly one top-level S-expression and errors if there is
// more than one or none.
func ParseOne(src, file string) (*Node, error) {
	ns, err := Parse(src, file)
	if err != nil {
		return nil, err
	}
	switch len(ns) {
	case 1:
		return ns[0], nil
	case 0:
		return nil, fmt.Errorf("%s: expected one expression, found none", file)
	default:
		return nil, fmt.Errorf("%s: expected one expression, found %d", file, len(ns))
	}
}

type parser struct {
	lex *lexer
	tok token
}

func (p *parser) fill() error {
	t, err := p.lex.next()
	if err != nil {
		return err
	}
	p.tok = t
	return nil
}

func (p *parser) parseNode() (*Node, error) {
	t := p.tok
	switch t.kind {
	case tokLParen:
		if err := p.fill(); err != nil {
			return nil, err
		}
		list := &Node{Kind: KindList, Pos: t.pos}
		for p.tok.kind != tokRParen {
			if p.tok.kind == tokEOF {
				return nil, fmt.Errorf("%s: unclosed list", t.pos)
			}
			item, err := p.parseNode()
			if err != nil {
				return nil, err
			}
			list.Items = append(list.Items, item)
		}
		if err := p.fill(); err != nil { // consume ")"
			return nil, err
		}
		return list, nil

	case tokRParen:
		return nil, fmt.Errorf("%s: unexpected )", t.pos)

	case tokSymbol, tokString, tokInt:
		if err := p.fill(); err != nil {
			return nil, err
		}
		k := KindSymbol
		switch t.kind {
		case tokString:
			k = KindString
		case tokInt:
			k = KindInt
		}
		return &Node{Kind: k, Text: t.text, Pos: t.pos}, nil
	}
	return nil, fmt.Errorf("%s: unexpected token", t.pos)
}
