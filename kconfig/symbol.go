package kconfig

// Type is a Kconfig symbol's declared type.
//
// Buildroot uses no tristate at all — verified, zero occurrences across its
// whole Config.in set. Tristate is a Linux-tree concern, so the agonising in
// DESIGN.md §8.2 applies only to the kernel half.
type Type uint8

const (
	Unknown Type = iota
	Bool
	Tristate
	String
	Int
	Hex
)

func (t Type) String() string {
	switch t {
	case Bool:
		return "bool"
	case Tristate:
		return "tristate"
	case String:
		return "string"
	case Int:
		return "int"
	case Hex:
		return "hex"
	}
	return "unknown"
}

// Solvable reports whether this type enters the constraint model.
//
// String, int and hex do not: their values pass through, and only their
// emptiness is modelled (DESIGN.md §8.3).
func (t Type) Solvable() bool { return t == Bool || t == Tristate }

// Default is a default value with its optional condition.
type Default struct {
	Value string
	Cond  *Expr
}

// Select is a forced enable. Kconfig does NOT visit the target's own
// dependencies when selecting, which is why this is stored separately from
// Depends rather than folded into it.
type Select struct {
	Target string
	Cond   *Expr
}

// Symbol is one imported Kconfig symbol.
type Symbol struct {
	Name    string
	Type    Type
	Prompt  string
	Depends *Expr // own depends-on, conjoined with enclosing if/menu guards
	// decls holds one dependency per declaration. Kconfig lets a symbol be
	// declared more than once, and each declaration carries its own guards
	// and depends-on lines; the symbol's dependency is their disjunction, as
	// the C implementation computes it. Depends is built from these once
	// parsing finishes.
	decls    []*Expr
	Selects  []Select
	Implies  []Select
	Defaults []Default
	RangeLo  string
	RangeHi  string
	Choice   string // name of the enclosing choice, if any
	EnvVar   string // option env="..."
	File     string
	Line     int
}

// Tree is an imported Kconfig tree.
type Tree struct {
	Root    string
	Symbols map[string]*Symbol
	Order   []string // source order, which matters for first-match defaults
	Choices []*ChoiceGroup
}

// ChoiceGroup is a choice block: at most one member may be y.
type ChoiceGroup struct {
	Name     string
	Optional bool
	Members  []string
	Defaults []Default
	Depends  *Expr
	File     string
	Line     int
}

func newTree(root string) *Tree {
	return &Tree{Root: root, Symbols: map[string]*Symbol{}}
}

// Get returns a symbol, creating the record if this is a forward reference.
func (t *Tree) Get(name string) *Symbol {
	if s, ok := t.Symbols[name]; ok {
		return s
	}
	s := &Symbol{Name: name}
	t.Symbols[name] = s
	t.Order = append(t.Order, name)
	return s
}
