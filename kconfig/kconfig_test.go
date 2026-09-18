package kconfig

import (
	"os"
	"path/filepath"
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

// A symbol declared twice depends on either declaration, not both. Buildroot
// declares BR2_PACKAGE_BUSYBOX_SHOW_OTHERS once inside `if BR2_PACKAGE_BUSYBOX`
// and again inside `if !BR2_PACKAGE_BUSYBOX`; conjoining the two made it
// unsatisfiable, and systemd selects it.
func TestRepeatedDeclarationsDisjoin(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(`
config BR2_BB
	bool "busybox"

if BR2_BB
config BR2_SHOW
	bool "show others"
	depends on BR2_X
endif

if !BR2_BB
config BR2_SHOW
	default y
endif

config BR2_ONCE
	bool "declared once"
	depends on BR2_X
config BR2_UNGUARDED
	string
if BR2_BB
config BR2_UNGUARDED
	default "bb"
endif
`), 0o644)
	tr, err := Load("Config.in", Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.Symbols["BR2_SHOW"].Depends.String(); got != "((BR2_BB && BR2_X) || !BR2_BB)" {
		t.Fatalf("repeated declarations should disjoin: %s", got)
	}
	if got := tr.Symbols["BR2_ONCE"].Depends.String(); got != "BR2_X" {
		t.Fatalf("a single declaration is unchanged: %s", got)
	}
	// A declaration with no guard at all makes the symbol unconditional, as it
	// does for BR2_ARCH.
	if d := tr.Symbols["BR2_UNGUARDED"].Depends; d != nil {
		t.Fatalf("an unguarded declaration should absorb the rest: %s", d)
	}
}

// The kernel's Kconfig dialect. Buildroot's parser had to grow three things
// for it: $(VAR) expansion, relational comparisons, and macro calls, which
// kbuild evaluates by running the compiler and Silt cannot.
func TestKernelDialect(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/arch/arm64", 0o755)
	os.WriteFile(dir+"/Kconfig", []byte(`
source "arch/$(SRCARCH)/Kconfig"

config GCC_VERSION
	int
	default 130300

config NEEDS_NEW_GCC
	bool "needs a new compiler"
	depends on GCC_VERSION >= 110500

config STACKPROTECTOR
	bool "stack protector"
	depends on $(cc-option,-fstack-protector-strong)
`), 0o644)
	os.WriteFile(dir+"/arch/arm64/Kconfig", []byte("config ARM64\n\tdef_bool y\n"), 0o644)

	tr, err := Load("Kconfig", Options{Root: dir, Env: map[string]string{"SRCARCH": "arm64"}})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Symbols["ARM64"] == nil {
		t.Error("arch/$(SRCARCH)/Kconfig was not sourced")
	}
	if got := tr.Symbols["NEEDS_NEW_GCC"].Depends.String(); got != "GCC_VERSION >= 110500" {
		t.Errorf("relational comparison: %s", got)
	}
	// The macro call survives as one opaque name. It is an undefined symbol,
	// so nothing states it and no condition mentioning it is ever refuted —
	// which is the honest answer, since only a compiler knows.
	if got := tr.Symbols["STACKPROTECTOR"].Depends.String(); got != "$(cc-option,-fstack-protector-strong)" {
		t.Errorf("macro call: %s", got)
	}
}

// A br2-external tree's Config.in is sourced by absolute path, from a file
// Buildroot generates. Joining an absolute path to the tree root produced
// /home/user/buildroot/home/user/silt/br2-external, which does not exist, and
// the importer's tolerance for missing generated files turned that into
// silence: every external symbol vanished and silt check called them unknown.
func TestAbsoluteSourcePathsAreNotJoinedToTheRoot(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "Config.in"),
		[]byte("config BR2_FROM_ELSEWHERE\n\tbool \"elsewhere\"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "Config.in"),
		[]byte("source \""+filepath.Join(elsewhere, "Config.in")+"\"\n"), 0o644)
	tr, err := Load("Config.in", Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Symbols["BR2_FROM_ELSEWHERE"] == nil {
		t.Error("a symbol sourced by absolute path is missing")
	}

	// A source line that names nothing is a typo, and staying quiet about it
	// is how a whole tree can go missing. Only the generated br2-external
	// files, which Buildroot sources before anything creates them, are
	// allowed to be absent.
	os.WriteFile(filepath.Join(root, "Config.in"), []byte("source \"missing/Config.in\"\n"), 0o644)
	if _, err := Load("Config.in", Options{Root: root}); err == nil {
		t.Error("a missing source file should be an error")
	}
	os.WriteFile(filepath.Join(root, "Config.in"), []byte("source \"$BR2_BASE_DIR/.br2-external.in.paths\"\n"), 0o644)
	if _, err := Load("Config.in", Options{Root: root, Env: map[string]string{"BR2_BASE_DIR": root}}); err != nil {
		t.Errorf("an ungenerated br2-external file should be tolerated: %v", err)
	}
}

// The whole path, against a real Buildroot: run its own generator over Silt's
// br2-external tree and import the result.
func TestExternalTreeImports(t *testing.T) {
	root, ext := os.Getenv("SILT_BUILDROOT"), os.Getenv("SILT_EXTERNAL")
	if root == "" || ext == "" {
		t.Skip("set SILT_BUILDROOT and SILT_EXTERNAL")
	}
	base := t.TempDir()
	env, err := BuildrootEnv(root, base)
	if err != nil {
		t.Fatal(err)
	}
	add, err := PrepareExternal(root, base, []string{ext})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range add {
		env[k] = v
	}
	if env["BR2_EXTERNAL_SILT_PATH"] == "" {
		t.Fatal("BR2_EXTERNAL_SILT_PATH was not exported; the tree's Config.in cannot be found without it")
	}
	tr, err := Load("Config.in", Options{Root: root, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	s := tr.Symbols["BR2_PACKAGE_HELLO_SILT"]
	if s == nil || s.Type != Bool || s.Prompt == "" {
		t.Fatalf("the external package's symbol did not import: %+v", s)
	}
	t.Logf("%d symbols including %s from %s", len(tr.Symbols), s.Name, s.File)

	// Without the external tree the same symbol must not exist, or the test
	// above would pass on a tree that ignores externals entirely.
	plain, err := BuildrootEnv(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if add, err := PrepareExternal(root, plain["BASE_DIR"], nil); err == nil {
		for k, v := range add {
			plain[k] = v
		}
	}
	tr2, err := Load("Config.in", Options{Root: root, Env: plain})
	if err != nil {
		t.Fatal(err)
	}
	if tr2.Symbols["BR2_PACKAGE_HELLO_SILT"] != nil {
		t.Error("the symbol exists without the external tree: something else is defining it")
	}
}
