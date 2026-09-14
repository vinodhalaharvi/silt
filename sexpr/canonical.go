package sexpr

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Canonical renders n in canonical form: one fixed spelling per meaning.
//
// Two expressions that mean the same thing must produce identical bytes and
// therefore an identical hash (Invariant 4). Canonicalization does three
// things:
//
//   - drops comments and all incidental whitespace;
//   - normalizes string escaping;
//   - sorts the members of unordered forms (see sortable).
//
// It deliberately does NOT sort every list. Order is meaning in several forms:
// compose order is not significant but (when cond a b) has a condition in a
// fixed position, and Kconfig defaults are first-match-wins, so reordering
// them would change the model.
func Canonical(n *Node) string {
	var b strings.Builder
	n.writeCanonical(&b)
	return b.String()
}

// Hash returns the content address of n: sha256 over its canonical form.
func Hash(n *Node) string {
	sum := sha256.Sum256([]byte(Canonical(n)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// sortable reports whether the members of a list headed by head may be
// reordered without changing meaning.
//
// Conservative by design: a form is listed here only when reordering is known
// to be safe. Anything unlisted keeps source order, so an omission costs a
// missed hash match and never a wrong one.
func sortable(head string) bool {
	switch head {
	case "buildroot", "linux", // constraint sets within a scope
		"provides", "requires", // capability sets
		"opaque", "unmanaged", "environment", // declaration sets
		"override":
		return true
	}
	return false
}

func (n *Node) writeCanonical(b *strings.Builder) {
	if n == nil {
		return
	}
	switch n.Kind {
	case KindSymbol, KindInt:
		b.WriteString(n.Text)
	case KindString:
		b.WriteByte('"')
		for _, r := range n.Text {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			case '\t':
				b.WriteString(`\t`)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
	case KindList:
		b.WriteByte('(')
		items := n.Items
		if len(items) > 1 && sortable(n.Head()) {
			rest := make([]*Node, len(items)-1)
			copy(rest, items[1:])
			sort.SliceStable(rest, func(i, j int) bool {
				return Canonical(rest[i]) < Canonical(rest[j])
			})
			items = append([]*Node{items[0]}, rest...)
		}
		for i, it := range items {
			if i > 0 {
				b.WriteByte(' ')
			}
			it.writeCanonical(b)
		}
		b.WriteByte(')')
	}
}
