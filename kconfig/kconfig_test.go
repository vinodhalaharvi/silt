package kconfig

import (
	"os"
	"strings"
	"testing"
)

func TestExprPrecedence(t *testing.T) {
	e, err := parseExpr("A && B || C")
	if err != nil {
		t.Fatal(err)
	}
	if e.Op != ExprOr {
		t.Fatalf("|| must bind loosest, got %s", e)
	}
}

func TestExprComparisons(t *testing.T) {
	e, err := parseExpr(`BR2_X != "" && !BR2_Y`)
	if err != nil {
		t.Fatal(err)
	}
	if e.Args[0].Op != ExprNeq || !e.Args[0].IsLit || e.Args[0].Lit != "" {
		t.Fatalf(`!= "" not recognised: %s`, e)
	}
}

// Buildroot uses backslash continuations across up to three lines.
func TestContinuations(t *testing.T) {
	out := joinContinuations([]string{
		`	depends on !A && \`, `		!B && \`, `		!C`, `	help`,
	})
	if got := out[0]; got != "	depends on !A &&  !B &&  !C" {
		t.Fatalf("joined wrong: %q", got)
	}
	if out[3] != "	help" {
		t.Fatalf("following line disturbed: %q", out[3])
	}
}

// package/linux-tools/Config.in contains `bool"selftests"` with no space.
func TestKeywordAdjacentQuote(t *testing.T) {
	kw, rest := cut(`bool"selftests"`)
	if kw != "bool" || rest != `"selftests"` {
		t.Fatalf("got %q / %q", kw, rest)
	}
}

func TestSplitIf(t *testing.T) {
	v, c := splitIf(`BR2_X if BR2_Y && BR2_Z`)
	if v != "BR2_X" || c != "BR2_Y && BR2_Z" {
		t.Fatalf("got %q / %q", v, c)
	}
	if v, c := splitIf(`"a if b"`); v != `"a if b"` || c != "" {
		t.Fatalf("if inside a string must not split: %q / %q", v, c)
	}
}

// An unresolved variable in a source path is an error, not a silent skip: the
// shape of the tree would otherwise depend on invisible state.
func TestSourceNeedsEnv(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte("source \"$MISSING/x.in\"\n"), 0o644)
	if _, err := Load("Config.in", Options{Root: dir}); err == nil {
		t.Fatal("expected an error for an unresolved source path")
	}
}

// Kconfig scoping is lexical across files: `if C / source x / endif` puts
// everything in x inside C. Losing that guard makes choice at-least-one
// clauses fire unconditionally.
func TestGuardCrossesSource(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte("if BR2_GUARD\nsource \"sub.in\"\nendif\n"), 0o644)
	os.WriteFile(dir+"/sub.in", []byte("config BR2_INNER\n\tbool \"inner\"\n"), 0o644)
	tr, err := Load("Config.in", Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	s := tr.Symbols["BR2_INNER"]
	if s == nil || s.Depends == nil {
		t.Fatal("sourced symbol lost its enclosing guard")
	}
	if s.Depends.String() != "BR2_GUARD" {
		t.Fatalf("guard = %s", s.Depends)
	}
}

func TestSelectRecordedSeparately(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(
		"config BR2_A\n\tbool \"a\"\n\tdepends on BR2_D\n\tselect BR2_B\n\tselect BR2_C if BR2_E\n"), 0o644)
	tr, err := Load("Config.in", Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	s := tr.Symbols["BR2_A"]
	if len(s.Selects) != 2 {
		t.Fatalf("selects: %+v", s.Selects)
	}
	if s.Selects[1].Cond == nil || s.Selects[1].Cond.String() != "BR2_E" {
		t.Fatal("conditional select lost its condition")
	}
	// select must not be folded into depends: the two readings of §8.1 both
	// have to stay expressible over the same imported data.
	if s.Depends.String() != "BR2_D" {
		t.Fatalf("depends polluted by select: %s", s.Depends)
	}
}

func TestChoiceMembersAndDefaults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(
		"choice\n\tprompt \"pick\"\n\tdefault BR2_B\nconfig BR2_A\n\tbool \"a\"\nconfig BR2_B\n\tbool \"b\"\nendchoice\n"), 0o644)
	tr, err := Load("Config.in", Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Choices) != 1 || len(tr.Choices[0].Members) != 2 {
		t.Fatalf("choices: %+v", tr.Choices)
	}
	if len(tr.Choices[0].Defaults) != 1 || tr.Choices[0].Defaults[0].Value != "BR2_B" {
		t.Fatalf("choice default dropped: %+v", tr.Choices[0].Defaults)
	}
	if tr.Symbols["BR2_A"].Choice == "" {
		t.Fatal("member not linked to its choice")
	}
}

// The whole Buildroot tree, when one is available. This is the only test that
// exercises the real corpus, and it is where every parser bug so far was found.
//
// It asserts structural properties, never a symbol count. Buildroot gains and
// loses hundreds of symbols between releases, so a hardcoded total pins the
// test to one checkout and fails for the wrong reason on every other — which
// teaches people to ignore it.
func TestRealTree(t *testing.T) {
	root := os.Getenv("SILT_BUILDROOT")
	if root == "" {
		t.Skip("set SILT_BUILDROOT to a Buildroot checkout to run")
	}
	tr, err := Load("Config.in", Options{Root: root, Env: map[string]string{
		"BR2_BASE_DIR": "/nonexistent", "HOSTARCH": "x86_64",
		"HOST_GCC_VERSION": "13 2", "BR2_VERSION_FULL": "0",
	}})
	if err != nil {
		t.Fatal(err)
	}

	// A floor, not a target: anything this low means the parse died early
	// rather than that the tree changed.
	if len(tr.Symbols) < 5000 {
		t.Fatalf("only %d symbols — the parse stopped early", len(tr.Symbols))
	}
	t.Logf("%d symbols, %d choices", len(tr.Symbols), len(tr.Choices))

	// Every symbol must be typed. An untyped symbol means a declaration the
	// tokenizer failed to recognise, which is how bool"selftests" was found.
	untyped := 0
	for name, s := range tr.Symbols {
		if s.Type == Unknown {
			if untyped < 5 {
				t.Errorf("%s has no type (%s:%d)", name, s.File, s.Line)
			}
			untyped++
		}
	}
	if untyped > 5 {
		t.Errorf("... and %d more untyped symbols", untyped-5)
	}

	// Symbols that have been in Buildroot for years and anchor the arch and
	// toolchain trees. Their absence means whole files were skipped.
	for _, want := range []string{"BR2_aarch64", "BR2_USE_MMU", "BR2_STATIC_LIBS"} {
		if tr.Symbols[want] == nil {
			t.Errorf("%s missing — a source file was likely skipped", want)
		}
	}

	// Guards must cross file boundaries. Most symbols sit inside some `if` or
	// under a package menu, so a tree where almost nothing has a dependency
	// means the scope threading regressed.
	withDeps := 0
	for _, s := range tr.Symbols {
		if s.Depends != nil {
			withDeps++
		}
	}
	if ratio := float64(withDeps) / float64(len(tr.Symbols)); ratio < 0.5 {
		t.Errorf("only %.0f%% of symbols have dependencies; guards are being lost across source",
			ratio*100)
	}

	// select must stay out of depends, or both readings of §8.1 collapse.
	if s := tr.Symbols["BR2_PACKAGE_LIBCAMERA"]; s != nil {
		if len(s.Selects) == 0 {
			t.Error("libcamera has no selects recorded")
		}
		if s.Depends != nil && strings.Contains(s.Depends.String(), "BR2_PACKAGE_GNUTLS") {
			t.Error("a select leaked into depends")
		}
	}
}

// A comment block carries its own depends-on lines, which belong to the
// comment's visibility and to nothing else.
//
// Buildroot's Config.in has, right after BR2_STATIC_LIBS:
//
//	comment "static only needs a toolchain w/ uclibc or musl"
//		depends on BR2_TOOLCHAIN_USES_GLIBC
//
// Treating comment as contentless left cur pointing at BR2_STATIC_LIBS, so that
// line landed on it — giving the symbol !GLIBC && GLIBC. A contradiction, which
// made assuming BR2_STATIC_LIBS instantly unsatisfiable and, through it, every
// solver query that touched the libc choice.
func TestCommentDependsDoNotLeak(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(`
config BR2_A
	bool "a"
	depends on !BR2_G

comment "a needs something else"
	depends on BR2_G

config BR2_B
	bool "b"
`), 0o644)
	tr, err := Load("Config.in", Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.Symbols["BR2_A"].Depends.String(); got != "!BR2_G" {
		t.Fatalf("comment's depends leaked onto the symbol: %s", got)
	}
	if tr.Symbols["BR2_B"] == nil {
		t.Fatal("parsing did not continue past the comment")
	}
}

// Inside a choice, a comment's depends must not land on the choice either.
func TestCommentDependsDoNotLeakIntoChoice(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(`
choice
	prompt "pick"
config BR2_X
	bool "x"

comment "note"
	depends on BR2_G

config BR2_Y
	bool "y"
endchoice
`), 0o644)
	tr, err := Load("Config.in", Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if c := tr.Choices[0]; c.Depends != nil {
		t.Fatalf("comment's depends leaked onto the choice: %s", c.Depends)
	}
}
