// Package sexpr implements Silt's S-expression representation: parsing,
// canonicalization and content hashing.
//
// The representation is data. There is no evaluator here and there must never
// be one: see DESIGN.md Invariant 1 and GRAMMAR.md.
package sexpr

import (
	"fmt"
	"strings"
)

// Kind distinguishes the four things a Node can be.
//
// Atoms are not collapsed into a single string type. GRAMMAR.md draws a
// distinction between (version "0.4.1") and (version 0.4.1), and a frontend
// that stores both as a string cannot make that distinction later without
// re-lexing.
type Kind uint8

const (
	KindList Kind = iota
	KindSymbol
	KindString
	KindInt
)

func (k Kind) String() string {
	switch k {
	case KindList:
		return "list"
	case KindSymbol:
		return "symbol"
	case KindString:
		return "string"
	case KindInt:
		return "int"
	}
	return "invalid"
}

// Pos is a source location. Every Node carries one.
//
// Provenance is Invariant 3: every constraint must trace back to a file and
// line, because that is what makes an explanation possible. Retrofitting this
// means touching every stage, so it is present from the first commit.
type Pos struct {
	File string
	Line int
	Col  int
}

func (p Pos) String() string {
	if p.File == "" {
		return fmt.Sprintf("%d:%d", p.Line, p.Col)
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// Short renders a position as file:line, which is the form used in
// explanations and error messages.
func (p Pos) Short() string {
	if p.File == "" {
		return fmt.Sprintf("%d", p.Line)
	}
	return fmt.Sprintf("%s:%d", p.File, p.Line)
}

// Node is an S-expression. Lists have Items; atoms have Text.
type Node struct {
	Kind  Kind
	Text  string // symbol name, string contents (unescaped), or integer literal
	Items []*Node
	Pos   Pos
}

func Symbol(s string) *Node { return &Node{Kind: KindSymbol, Text: s} }
func String(s string) *Node { return &Node{Kind: KindString, Text: s} }
func Int(s string) *Node    { return &Node{Kind: KindInt, Text: s} }
func List(items ...*Node) *Node {
	return &Node{Kind: KindList, Items: items}
}

// IsList reports whether n is a list.
func (n *Node) IsList() bool { return n != nil && n.Kind == KindList }

// Head returns the head symbol of a list, or "" if n is not a list whose first
// element is a symbol.
//
// Every list in the language begins with a fixed keyword (GRAMMAR.md), so Head
// is how nearly all dispatch is done.
func (n *Node) Head() string {
	if !n.IsList() || len(n.Items) == 0 {
		return ""
	}
	if n.Items[0].Kind != KindSymbol {
		return ""
	}
	return n.Items[0].Text
}

// Args returns everything after the head.
func (n *Node) Args() []*Node {
	if !n.IsList() || len(n.Items) == 0 {
		return nil
	}
	return n.Items[1:]
}

// String renders n in canonical form. See Canonical.
func (n *Node) String() string {
	var b strings.Builder
	n.writeCanonical(&b)
	return b.String()
}
