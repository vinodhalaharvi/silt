// Package lang is the typed layer over sexpr: it turns generic S-expression
// nodes into the forms GRAMMAR.md specifies, rejecting anything outside them.
//
// Nothing here evaluates. Every list in the language begins with a fixed
// keyword, so this whole package is dispatch on Node.Head.
package lang

import "github.com/vinodhalaharvi/silt/sexpr"

// Tristate is n < m < y. Modelled in full rather than collapsing m into y,
// because the difference between a module and a built-in decides what lands in
// the rootfs (DESIGN.md 8.2).
type Tristate uint8

const (
	N Tristate = iota
	M
	Y
)

func (t Tristate) String() string {
	switch t {
	case N:
		return "n"
	case M:
		return "m"
	}
	return "y"
}

// Scope names one of the two Kconfig trees Silt models.
type Scope string

const (
	Buildroot Scope = "buildroot"
	Linux     Scope = "linux"
)

// Kind is the sort of a fragment. Profiles are mutually exclusive; features
// are additive. That distinction is enforced in compose, not here.
type Kind string

const (
	Target  Kind = "target"
	Profile Kind = "profile"
	Feature Kind = "feature"
	// KindImage names another image, usable only inside compose. A family of
	// images differing by a feature or two otherwise means repeating the base
	// in each, which is the copy-paste this project exists to remove.
	KindImage Kind = "image"
)

// ID is a fragment identifier such as target:qemu-aarch64-virt.
type ID struct {
	Kind Kind
	Name string
}

func (id ID) String() string { return string(id.Kind) + ":" + id.Name }

// Constraint is one requirement on one symbol.
type Constraint struct {
	Sym     SymbolID
	Want    Tristate
	AtLeast bool // (at-least m X): Want or stronger
	Soft    bool // (prefer ...): yields to hard constraints
	Value   string
	IsValue bool // (value X "s"): passed through, emptiness modelled
	// IsPath marks (path X "rel"): a value naming a file the pack carries,
	// rather than a string. Value holds the path as written, relative to the
	// pack root, until the pack loader rewrites it into the
	// $(BR2_EXTERNAL_NAME_PATH)/... form Buildroot expands; Resolved is where
	// it actually is, so the file can be checked and hashed.
	IsPath   bool
	Resolved string
	// Template is how the resolved path appears in the emitted value, with
	// {} standing for the path. It exists because some symbols take a path
	// inside a longer string: BR2_ROOTFS_POST_SCRIPT_ARGS is
	// "-c <the genimage config>", and a value that merely contains a path is
	// exactly as unchecked as one that is a path.
	Template string
	// List marks a symbol Buildroot reads as a space-separated list rather
	// than one value: BR2_ROOTFS_OVERLAY, BR2_GLOBAL_PATCH_DIR, the
	// *_CONFIG_FRAGMENT_FILES symbols. Two fragments each contributing one
	// is the normal case for those, and treating them as single values made
	// a board that carries an overlay and a feature that carries one unable
	// to compose at all.
	List bool
	Pos  sexpr.Pos
	From string // fragment or image this came from
}

// Guarded is (when condition consequent...). It lowers to an implication and
// is never executed.
type Guarded struct {
	Cond  *Cond
	Then  []Constraint
	Pos   sexpr.Pos
	From  string
	Scope Scope // empty when the rule crosses trees
}

// Cond is a condition inside a when.
type Cond struct {
	Op    string // "constraint", "set?", "equal?", "and", "or", "not"
	C     *Constraint
	Sym   SymbolID
	Value string // for equal?
	Args  []*Cond
	Pos   sexpr.Pos
}

// Capability is the abstraction that makes a feature portable across targets.
type Capability struct {
	Name string
	Pos  sexpr.Pos
}

// CapabilityDecl is a capability's one authoritative declaration.
//
// Without a declared vocabulary, provides and requires are matched by string
// alone: a name typed the same way on both sides passes and means nothing.
// Declaring them once turns a typo into an error and gives each capability a
// place to record what it means and, where one exists, the symbol that backs it.
type CapabilityDecl struct {
	Name   string
	Doc    string
	Symbol SymbolID // optional symbol this capability corresponds to
	// Symbols are every symbol whose presence means the capability is
	// available. Repeating (symbol ...) appends here.
	//
	// One symbol is enough to say what a capability corresponds to. Forbidding
	// needs all of them: "nothing that provides a remote shell" is only
	// checkable if dropbear, openssh and busybox's telnetd are all named.
	// A capability with one symbol bound and three implementations in the tree
	// gives an absence claim that reads as verified and is not.
	Symbols []SymbolID
	Pos     sexpr.Pos
}

// Capabilities is a declaration block, one per library.
type Capabilities struct {
	Decls []CapabilityDecl
	Pos   sexpr.Pos
}

// Fragment is a target, profile or feature.
type Fragment struct {
	ID       ID
	Doc      string
	Provides []Capability
	Requires []Capability
	// Forbids is the third relation. provides and requires say what a system
	// can do; forbids says what it must not be able to do, which is the claim
	// a hardened appliance actually makes. It is not a synonym for (n SYMBOL):
	// the point is an absence checkable against everything, rather than one
	// symbol named by hand.
	Forbids     []Capability
	Constraints map[Scope][]Constraint
	Guards      []Guarded
	Version     string // (linux (custom-version "..."))
	Pos         sexpr.Pos
}

// Rules is a cross-tree rule block: implications neither Kconfig tree can
// state, because they span both.
type Rules struct {
	Name   string
	Guards []Guarded
	Pos    sexpr.Pos
}

// Delegation hands a whole Kconfig tree back to its native tooling.
type Delegation struct {
	Tree          string
	CustomConfig  string
	FragmentFiles []string
	Pos           sexpr.Pos
}

// Policy ranks repairs. Only statically known quantities appear here.
type Policy struct {
	Minimize []string
	Keep     []string
	Baseline string
}

// Image is a composition plus everything Silt must declare about its own
// blind spots. Those declarations belong to a concrete build on a concrete
// host, which is why they cannot live in a reusable fragment.
type Image struct {
	Name        string
	Doc         string
	Compose     []ID
	Constraints map[Scope][]Constraint
	Override    []Constraint
	Opaque      []Constraint
	Unmanaged   []SymbolID // Name may end in *
	Delegate    []Delegation
	Environment []Constraint
	Policy      Policy
	Version     string
	// VerifiedAgainst pins the tree release this image's claims were checked
	// against. A fragment asserts things that are true of a release, not of
	// Buildroot in general.
	VerifiedAgainst map[string]string
	// ExpectProblems marks an image checked in to fail, with the reason.
	//
	// edge-camera exists to show that libcamera and static libs cannot hold
	// together. Without a way to say so, one deliberate fixture made check's
	// exit code useless for CI, the one place the check earns its keep. The
	// converse is reported too: a fixture that stops failing has stopped
	// testing what it was checked in for, a quieter way to lose a test than
	// deleting it. Not inherited: an image derived from a fixture is not
	// itself one, and inheriting the marker would excuse its failures.
	ExpectProblems string
	Pos            sexpr.Pos
}

// File is everything parsed out of one .sx file.
type File struct {
	Path         string
	Fragments    []*Fragment
	Rules        []*Rules
	Images       []*Image
	Capabilities []*Capabilities
	Trees        []*TreeDecl
	Packs        []*PackDecl
}
