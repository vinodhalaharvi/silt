package plugin

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/plan"
)

// A kind written against its own types, with no assertion anywhere in it -
// which is what the type parameters are for.
type fakeClaim struct{ Want string }

type fakeCfg struct {
	known map[string]string
	opens *int
}

type fakeDefs struct{ known map[string]string }

var fake = Interp[fakeClaim, fakeCfg, *fakeDefs]{
	Open: func(c fakeCfg) (*fakeDefs, error) {
		if c.opens != nil {
			*c.opens++
		}
		return &fakeDefs{known: c.known}, nil
	},
	Answer: func(d *fakeDefs, k plan.Ask[fakeClaim]) plan.Answer {
		v, ok := d.known[k.Name]
		switch k.Op {
		case plan.Exists:
			return plan.Yes(ok)
		case plan.Holds:
			if !ok {
				return plan.Fail(fmt.Errorf("%s is not a symbol", k.Name))
			}
			if k.Claim.Want != "" && k.Claim.Want != v {
				return plan.Fail(fmt.Errorf("%s is %q, not %q", k.Name, v, k.Claim.Want))
			}
			return plan.OK()
		case plan.Settled:
			if !ok {
				return plan.NotAnswerable()
			}
			return plan.Val(v)
		}
		return plan.NotAnswerable()
	},
	// Emit, Settle and Solve stay nil: this kind cannot do them, and that has
	// to stay visible.
}

func bound(known map[string]string, opens *int) *Bound[fakeClaim, fakeCfg, *fakeDefs] {
	return Bind(fake, fakeCfg{known: known, opens: opens})
}

func holds(tree, name, want string) plan.Ask[fakeClaim] {
	return plan.Ask[fakeClaim]{Op: plan.Holds, Tree: tree, Name: name, Claim: fakeClaim{Want: want}}
}

func TestRunAnswersAndFolds(t *testing.T) {
	p := plan.Traverse(
		[]plan.Ask[fakeClaim]{holds("fake", "A", ""), holds("fake", "MISSING", "")},
		plan.Claim[fakeClaim])

	got, err := Run(p, bound(map[string]string{"A": "y"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	v := plan.All(got)
	if len(v.Problems) != 1 {
		t.Fatalf("%d problems, want 1: %v", len(v.Problems), v.Err())
	}
	if !strings.Contains(v.Err().Error(), "MISSING is not a symbol") {
		t.Errorf("message: %v", v.Err())
	}
}

// The claim travels typed: a plugin reads k.Claim.Want with no assertion, and
// a plan carrying another kind's claim would not compile against this one.
func TestClaimIsTyped(t *testing.T) {
	p := plan.Claim(holds("fake", "A", "n"))
	got, err := Run(p, bound(map[string]string{"A": "y"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Failed() || !strings.Contains(got.Err().Error(), `is "y", not "n"`) {
		t.Errorf("the claim did not reach the interpreter: %v", got.Err())
	}
}

// The first thing the up-front ask list buys: one open for a whole run.
func TestTreeOpenedOncePerRun(t *testing.T) {
	opens := 0
	names := []string{"A", "B", "C", "D", "E"}
	p := plan.Traverse(names, func(n string) plan.Plan[fakeClaim, plan.V[plan.Unit]] {
		return plan.Claim(holds("fake", n, ""))
	})
	known := map[string]string{"A": "y", "B": "y", "C": "y", "D": "y", "E": "y"}
	if _, err := Run(p, bound(known, &opens)); err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Errorf("opened %d times for 5 asks, want 1", opens)
	}
}

// No interpreter for a kind: every question is unanswerable rather than
// failed, which is what a missing --linux has always meant.
func TestNilBoundIsUnanswerable(t *testing.T) {
	got, err := Run(plan.Claim(holds("fake", "A", "")), (*Bound[fakeClaim, fakeCfg, *fakeDefs])(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed() {
		t.Error("a kind with no interpreter was reported as a failure")
	}
}

// A kind that cannot emit must stay distinguishable from one that emits an
// empty file.
func TestNilCapabilitiesAreVisible(t *testing.T) {
	b := bound(nil, nil)
	if b.CanEmit() || b.CanSettle() || b.CanSolve() || b.CanRead() {
		t.Error("a kind with nil Emit/Settle/Solve/Read claims it can")
	}

	with := fake
	with.Emit = func(*fakeDefs, []fakeClaim) (File, error) { return File{Name: "x"}, nil }
	if b := Bind(with, fakeCfg{}); !b.CanEmit() || b.CanSettle() {
		t.Error("Emit lost or Settle invented")
	}
}

func TestLazyOpensOnlyWhenAsked(t *testing.T) {
	opens := 0
	i := Wrapped(fake, Lazy[fakeClaim, fakeCfg, *fakeDefs]())
	b := Bind(i, fakeCfg{known: map[string]string{"A": "y"}, opens: &opens})

	// A plan with no asks never opens.
	if _, err := Run(plan.Pure[fakeClaim](plan.Good(plan.Unit{})), b); err != nil {
		t.Fatal(err)
	}
	if opens != 0 {
		t.Errorf("opened %d times for an empty plan", opens)
	}

	// Two runs share one open.
	for n := 0; n < 2; n++ {
		if _, err := Run(plan.Claim(holds("fake", "A", "")), b); err != nil {
			t.Fatal(err)
		}
	}
	if opens != 1 {
		t.Errorf("opened %d times across two runs, want 1", opens)
	}
}

func TestMemoAsksOnce(t *testing.T) {
	calls := 0
	counting := func(i Interp[fakeClaim, fakeCfg, *fakeDefs]) Interp[fakeClaim, fakeCfg, *fakeDefs] {
		next := i.Answer
		i.Answer = func(d *fakeDefs, k plan.Ask[fakeClaim]) plan.Answer {
			calls++
			return next(d, k)
		}
		return i
	}
	b := Bind(Wrapped(fake, Memo[fakeClaim, fakeCfg, *fakeDefs](), counting),
		fakeCfg{known: map[string]string{"A": "y"}})

	// The same question five times from five positions, which is what
	// fifty-six images asking about one symbol looks like.
	asks := make([]plan.Ask[fakeClaim], 5)
	for i := range asks {
		asks[i] = plan.Ask[fakeClaim]{Op: plan.Exists, Tree: "fake", Name: "A",
			Pos: fmt.Sprintf("f%d.sx:1", i)}
	}
	if _, err := Run(plan.Traverse(asks, plan.Claim[fakeClaim]), b); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("asked %d times, want 1 (position must not split the cache)", calls)
	}
}

// A Holds carries a claim, and two claims about one symbol can differ - so
// memoising it would answer the second from the first.
func TestMemoDoesNotCacheClaims(t *testing.T) {
	calls := 0
	counting := func(i Interp[fakeClaim, fakeCfg, *fakeDefs]) Interp[fakeClaim, fakeCfg, *fakeDefs] {
		next := i.Answer
		i.Answer = func(d *fakeDefs, k plan.Ask[fakeClaim]) plan.Answer {
			calls++
			return next(d, k)
		}
		return i
	}
	b := Bind(Wrapped(fake, Memo[fakeClaim, fakeCfg, *fakeDefs](), counting),
		fakeCfg{known: map[string]string{"A": "y"}})

	p := plan.Traverse([]plan.Ask[fakeClaim]{holds("fake", "A", "y"), holds("fake", "A", "n")},
		plan.Claim[fakeClaim])
	got, err := Run(p, b)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("%d calls; a cached Holds would answer the second from the first", calls)
	}
	if !plan.All(got).Failed() {
		t.Error("the second claim should have failed")
	}
}

func TestTrace(t *testing.T) {
	var buf bytes.Buffer
	b := Bind(Wrapped(fake, Trace[fakeClaim, fakeCfg, *fakeDefs](&buf)),
		fakeCfg{known: map[string]string{"A": "y"}})
	p := plan.Traverse([]plan.Ask[fakeClaim]{holds("fake", "A", ""), holds("fake", "B", "")},
		plan.Claim[fakeClaim])
	if _, err := Run(p, b); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "ok") || !strings.Contains(out, "not a symbol") {
		t.Errorf("trace:\n%s", out)
	}
}

func TestPinnedRefusesAWrongVersion(t *testing.T) {
	b := Bind(Wrapped(fake, Pinned[fakeClaim, fakeCfg, *fakeDefs]("2024.11.1", "2025.02.16")),
		fakeCfg{known: map[string]string{"A": "y"}})
	got, err := Run(plan.Claim(holds("fake", "A", "")), b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Failed() {
		t.Fatal("answered from a tree the image did not declare")
	}
	if !strings.Contains(got.Err().Error(), "2025.02.16") {
		t.Errorf("message does not name the version that answered: %v", got.Err())
	}
}

// A kind that does not say which settings affect a build is assumed to have
// all of them affect it: wrongly reusing a cached tree is worse than wrongly
// rebuilding one.
func TestAffectsDefaultsToEverything(t *testing.T) {
	if !bound(nil, nil).Affects("ANYTHING") {
		t.Error("a kind with no Affects claimed a setting does not matter")
	}
	with := fake
	with.Affects = func(n string) bool { return n != "BR2_JLEVEL" }
	b := Bind(with, fakeCfg{})
	if b.Affects("BR2_JLEVEL") || !b.Affects("BR2_PACKAGE_X") {
		t.Error("Affects not consulted")
	}
}

func TestOpenFailurePropagates(t *testing.T) {
	broken := fake
	broken.Open = func(fakeCfg) (*fakeDefs, error) { return nil, errors.New("no such directory") }
	_, err := Run(plan.Claim(holds("fake", "A", "")), Bind(broken, fakeCfg{}))
	if err == nil || !strings.Contains(err.Error(), "no such directory") {
		t.Errorf("open failure swallowed: %v", err)
	}
}
