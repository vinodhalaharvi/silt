package kconfig

import (
	"os"
	"strings"
	"testing"
)

func evalOf(t *testing.T, src string, assign ...Assignment) *Evaluation {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/Config.in", []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMenu("Config.in", Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	return m.Evaluate(assign)
}

// The reason must mirror the order calc decides a value in. A reason listing
// them in any other order would be a plausible story rather than what happened.
func TestWhyFollowsCalcPrecedence(t *testing.T) {
	src := `
config BR2_PICKED
	bool "picked"
config BR2_SELECTOR
	bool "selector"
	select BR2_FORCED
config BR2_FORCED
	bool
config BR2_DEFAULTED
	bool
	default y
config BR2_QUIET
	bool
`
	ev := evalOf(t, src, Assignment{Name: "BR2_PICKED", Value: "y"},
		Assignment{Name: "BR2_SELECTOR", Value: "y"})

	for _, c := range []struct {
		sym  string
		want Because
	}{
		{"BR2_PICKED", BecauseUserValue},
		{"BR2_FORCED", BecauseSelected},
		{"BR2_DEFAULTED", BecauseDefault},
		{"BR2_QUIET", BecauseNoDefault},
	} {
		if got := ev.Why(c.sym).Because; got != c.want {
			t.Errorf("%s: got %v, want %v", c.sym, got, c.want)
		}
	}
}

// A symbol with no visible prompt cannot be set by hand however much a fragment
// asks — the most common surprise in a Kconfig tree, and worth saying plainly.
func TestWhyReportsInvisiblePrompt(t *testing.T) {
	ev := evalOf(t, `
config BR2_HIDDEN
	bool
	default y
`)
	r := ev.Why("BR2_HIDDEN")
	if r.Visible != "n" {
		t.Fatalf("visible = %q", r.Visible)
	}
	if !strings.Contains(r.Format(), "cannot be set by hand") {
		t.Errorf("format should say so:\n%s", r.Format())
	}
}

// A configured value the prompt's invisibility discarded is the silent drop
// seen from the other side, and it must not read as an ordinary user value.
func TestWhyDistinguishesDiscardedUserValue(t *testing.T) {
	ev := evalOf(t, `
config BR2_GUARD
	bool "guard"
config BR2_THING
	bool "thing"
	depends on BR2_GUARD
`, Assignment{Name: "BR2_THING", Value: "y"})

	if got := ev.Why("BR2_THING").Because; got != BecauseUserOverridden {
		t.Fatalf("got %v, want BecauseUserOverridden", got)
	}
}

// A symbol with several defaults and the wrong one firing is a question about
// the ones above it.
func TestWhyListsPassedOverDefaults(t *testing.T) {
	ev := evalOf(t, `
config BR2_COND
	bool "cond"
config BR2_MULTI
	bool
	default y if BR2_COND
	default y
`)
	r := ev.Why("BR2_MULTI")
	if r.Because != BecauseDefault {
		t.Fatalf("got %v", r.Because)
	}
	if len(r.Alternatives) != 1 || !strings.Contains(r.Alternatives[0], "BR2_COND") {
		t.Errorf("the skipped default should be named: %+v", r.Alternatives)
	}
}

func TestWhyUnknownSymbol(t *testing.T) {
	ev := evalOf(t, "config BR2_A\n\tbool \"a\"\n")
	if got := ev.Why("BR2_NOPE").Because; got != BecauseUnknown {
		t.Fatalf("got %v", got)
	}
}
