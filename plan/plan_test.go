package plan

import (
	"errors"
	"strings"
	"testing"
)

// A claim type of the tests' own: the point of the parameter is that a plan
// built for one kind cannot be run against another's interpreter, and a test
// that used a bare string would not exercise that.
type testClaim struct{ Want string }

func ask(tree, name string) Ask[testClaim] {
	return Ask[testClaim]{Op: Holds, Tree: tree, Name: name}
}

// The property the whole design rests on: the questions are knowable before
// any of them is answered.
func TestAsksAreKnownBeforeRunning(t *testing.T) {
	p := Map2(
		Claim(ask("buildroot", "BR2_PACKAGE_MOSQUITTO")),
		Claim(ask("linux", "CONFIG_TUN")),
		func(a, b V[Unit]) V[Unit] { return Both(a, b, func(Unit, Unit) Unit { return Unit{} }) })

	if got := len(p.Asks()); got != 2 {
		t.Fatalf("%d asks, want 2", got)
	}
	if got := Trees(p); len(got) != 2 || got[0] != "buildroot" || got[1] != "linux" {
		t.Errorf("Trees = %v", got)
	}
	if got := Symbols(p, "linux"); len(got) != 1 || got[0] != "CONFIG_TUN" {
		t.Errorf("Symbols(linux) = %v", got)
	}
}

// Traverse over many images must ask about each tree once per claim but report
// each tree once - this is what lets a run open Buildroot a single time
// instead of once per command.
func TestTreesDeduplicates(t *testing.T) {
	claims := []string{"BR2_A", "BR2_B", "BR2_C"}
	p := Traverse(claims, func(n string) Plan[testClaim, V[Unit]] { return Claim(ask("buildroot", n)) })

	if got := len(p.Asks()); got != 3 {
		t.Errorf("%d asks, want 3", got)
	}
	if got := Trees(p); len(got) != 1 {
		t.Errorf("Trees = %v, want one tree", got)
	}
}

// Every failure, not the first. The reason this type exists rather than error.
func TestFailuresAccumulate(t *testing.T) {
	k1, k2, k3 := ask("buildroot", "A"), ask("buildroot", "B"), ask("linux", "C")
	p := Traverse([]Ask[testClaim]{k1, k2, k3}, Claim[testClaim])

	got := All(p.Fold([]Answer{
		Fail(errors.New("A is not a symbol")),
		OK(),
		Fail(errors.New("C needs MAC80211")),
	}))

	if len(got.Problems) != 2 {
		t.Fatalf("%d problems, want 2: %v", len(got.Problems), got.Err())
	}
	msg := got.Err().Error()
	for _, want := range []string{"A is not a symbol", "C needs MAC80211"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
}

// A left-hand failure must not hide the right-hand side's problems, which is
// exactly what a monadic bind would do.
func TestLeftFailureDoesNotHideRight(t *testing.T) {
	a := Bad[testClaim, Unit](ask("buildroot", "A"), errors.New("left"))
	b := Bad[testClaim, Unit](ask("linux", "B"), errors.New("right"))
	got := Both(a, b, func(Unit, Unit) Unit { return Unit{} })
	if len(got.Problems) != 2 {
		t.Errorf("%d problems, want both", len(got.Problems))
	}
}

// A tree that cannot answer is not a tree that says no. `silt check` without
// --linux reports CONFIG_ claims as unchecked, and that behaviour now lives in
// one fold rather than in each command.
func TestUnknownIsNotFailure(t *testing.T) {
	k := ask("linux", "CONFIG_TUN")
	v := Claim(k).Fold([]Answer{NotAnswerable()})
	if v.Failed() {
		t.Error("an unanswerable question was reported as a failure")
	}
	counts := Unchecked([]Ask[testClaim]{k}, []Answer{NotAnswerable()})
	if counts["linux"] != 1 {
		t.Errorf("Unchecked = %v", counts)
	}
}

// Both arms of a rule stay in the ask list even though one is folded: that is
// what lets a cache key cover a rule whose untaken branch changed, and `silt
// why` report a rule that was considered and did not fire.
func TestBranchKeepsBothArms(t *testing.T) {
	cond := Lift(Ask[testClaim]{Op: Exists, Tree: "buildroot", Name: "BR2_INIT_SYSTEMD"},
		func(a Answer) bool { return a.Bool })
	then := Claim(ask("linux", "CONFIG_CGROUPS"))
	els := Pure[testClaim](Good(Unit{}))

	p := Branch(cond, then, els)
	if got := len(p.Asks()); got != 2 {
		t.Fatalf("%d asks, want 2 (the condition and the consequent)", got)
	}
	if got := Symbols(p, "linux"); len(got) != 1 {
		t.Errorf("the consequent's symbol is invisible: %v", got)
	}

	// Condition false: the consequent is not folded, even though it was asked.
	v := p.Fold([]Answer{Yes(false), Fail(errors.New("would have failed"))})
	if v.Failed() {
		t.Error("folded the arm that was not taken")
	}
	// Condition true: it is.
	v = p.Fold([]Answer{Yes(true), Fail(errors.New("missing"))})
	if !v.Failed() {
		t.Error("did not fold the taken arm")
	}
}

// The cross-tree comparison, which is two kinds' answers meeting. It is not
// one plan on purpose: the two sides are questions of different kinds, so they
// have different types, and the place for kinds to meet is their results.
func TestCompare(t *testing.T) {
	const why = "both ends must agree where the ring is"

	if v := Compare(Val("0x88000000"), Val("0x88000000"), "shm base", why); v.Failed() {
		t.Error("equal values reported as a mismatch")
	}

	v := Compare(Val("0x88000000"), Val("0x88100000"), "shm base", why)
	if !v.Failed() {
		t.Fatal("mismatch not reported")
	}
	msg := v.Err().Error()
	for _, want := range []string{"0x88000000", "0x88100000", why} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}

	// One side unanswerable: not a mismatch. A tree that was not given cannot
	// contradict anything.
	if v := Compare(Val("0x88000000"), NotAnswerable(), "shm base", why); v.Failed() {
		t.Error("an unanswered side was treated as a mismatch")
	}
}

// Rewrite changes the questions without changing the plan's shape.
func TestRewrite(t *testing.T) {
	p := Claim(ask("buildroot", "BR2_X"))
	q := Rewrite(p, func(k Ask[testClaim]) Ask[testClaim] {
		k.Hint = "because the target says so"
		return k
	})
	if len(q.Asks()) != 1 || q.Asks()[0].Hint == "" {
		t.Error("hint not applied")
	}
	if p.Asks()[0].Hint != "" {
		t.Error("rewrote the original plan in place")
	}
}

// Describe is the dry run: what will be asked, without asking it.
func TestDescribe(t *testing.T) {
	p := Traverse([]Ask[testClaim]{ask("buildroot", "BR2_A"), ask("linux", "CONFIG_B")}, Claim[testClaim])
	out := Describe(p)
	if !strings.Contains(out, "buildroot:BR2_A") || !strings.Contains(out, "linux:CONFIG_B") {
		t.Errorf("Describe:\n%s", out)
	}
}
