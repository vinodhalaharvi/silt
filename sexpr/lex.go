package sexpr

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokKind uint8

const (
	tokEOF tokKind = iota
	tokLParen
	tokRParen
	tokSymbol
	tokString
	tokInt
)

type token struct {
	kind tokKind
	text string
	pos  Pos
}

type lexer struct {
	src  string
	file string
	off  int
	line int
	col  int
}

func newLexer(src, file string) *lexer {
	return &lexer{src: src, file: file, line: 1, col: 1}
}

func (l *lexer) pos() Pos { return Pos{File: l.file, Line: l.line, Col: l.col} }

func (l *lexer) advance(n int) {
	for i := 0; i < n && l.off < len(l.src); i++ {
		if l.src[l.off] == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
		l.off++
	}
}

// skipSpace consumes whitespace and ";" comments.
func (l *lexer) skipSpace() {
	for l.off < len(l.src) {
		c := l.src[l.off]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			l.advance(1)
		case c == ';':
			for l.off < len(l.src) && l.src[l.off] != '\n' {
				l.advance(1)
			}
		default:
			return
		}
	}
}

// symbolRune reports whether r may appear in a symbol.
//
// "?" is included so that set? lexes as one token, and "*" so that the
// unmanaged patterns of GRAMMAR.md (BR2_TARGET_UBOOT_*) do too.
func symbolRune(r rune) bool {
	switch r {
	case '(', ')', ';', '"':
		return false
	}
	return !unicode.IsSpace(r)
}

func (l *lexer) next() (token, error) {
	l.skipSpace()
	if l.off >= len(l.src) {
		return token{kind: tokEOF, pos: l.pos()}, nil
	}
	start := l.pos()
	switch c := l.src[l.off]; {
	case c == '(':
		l.advance(1)
		return token{kind: tokLParen, text: "(", pos: start}, nil
	case c == ')':
		l.advance(1)
		return token{kind: tokRParen, text: ")", pos: start}, nil
	case c == '"':
		return l.lexString(start)
	default:
		return l.lexAtom(start)
	}
}

func (l *lexer) lexString(start Pos) (token, error) {
	l.advance(1) // opening quote
	var b strings.Builder
	for {
		if l.off >= len(l.src) {
			return token{}, fmt.Errorf("%s: unterminated string", start)
		}
		c := l.src[l.off]
		switch c {
		case '"':
			l.advance(1)
			return token{kind: tokString, text: b.String(), pos: start}, nil
		case '\\':
			if l.off+1 >= len(l.src) {
				return token{}, fmt.Errorf("%s: unterminated escape", l.pos())
			}
			esc := l.src[l.off+1]
			switch esc {
			case '"', '\\':
				b.WriteByte(esc)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return token{}, fmt.Errorf("%s: unknown escape \\%c", l.pos(), esc)
			}
			l.advance(2)
		case '\n':
			return token{}, fmt.Errorf("%s: newline in string", l.pos())
		default:
			b.WriteByte(c)
			l.advance(1)
		}
	}
}

func (l *lexer) lexAtom(start Pos) (token, error) {
	begin := l.off
	for l.off < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[l.off:])
		if !symbolRune(r) {
			break
		}
		l.advance(size)
	}
	text := l.src[begin:l.off]
	if text == "" {
		return token{}, fmt.Errorf("%s: unexpected character %q", start, l.src[l.off])
	}
	if isIntLiteral(text) {
		return token{kind: tokInt, text: text, pos: start}, nil
	}
	return token{kind: tokSymbol, text: text, pos: start}, nil
}

func isIntLiteral(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' || s[0] == '+' {
		if len(s) == 1 {
			return false
		}
		i = 1
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
