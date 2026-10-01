package plugin

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/plan"
)

// A kind written against its own types, with no assertions anywhere in it -
// which is what the type parameters are for.
type fakeCfg struct {
	known map[string]string
	opens *int
}

type fakeDefs struct {
	known map[string]string
}

var fake = Interp[fakeCfg, *fakeDefs]{
	Open: func(c fakeCfg) (*fakeDefs, error) {
		if c.opens != nil {
			*c.opens++
		}
		return &fakeDefs{known: c.known}, nil
	},
	Answer: func(d *fakeDefs, k plan.Ask) plan.Answer {
		v, ok := d.known[k.Name]
		switch k.Op {
		case plan.Exists:
			return plan.Yes(ok)
		case plan.Holds:
			if !ok {
				return plan.Fail(fmt.Errorf("%s is not a symbol", k.Name))
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
	// to survive erasure.
}

func reg(known map[string]string, opens *int) Registry {
	return Registry{"fake": Erase(fake, fakeCfg{known: known, opens: opens})}
}

func TestRunAnswersAndFolds(t *testing.T) {
	p := plan.Traverse(
		[]plan.Ask{
			{Op: plan.Holds, Tree: "fake", Name: "A"},
			{Op: plan.Holds, Tree: "fake", Name: "MISSING"},
		},
		plan.Claim)

	got, err := Run(p, reg(map[string]string{"A": "y"}, nil))
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

// The first thing the up-front ask list buys: one open for a whole run, not
// one per ask and not one per command.
func TestTreeOpenedOncePerRun(t *testing.T) {
	opens := 0
	names := []string{"A", "B", "C", "D", "E"}
	p := plan.Traverse(names, func(n string) plan.Plan[plan.V[plan.Unit]] {
		return plan.Claim(plan.Ask{Op: plan.Holds, Tree: "fake", Name: n})
	})

	if _, err := Run(p, reg(map[string]string{"A": "y", "B": "y", "C": "y", "D": "y", "E": "y"}, &opens)); err != nil {
		t.Fatal(err)
	}
	if opens != 1 {
		t.Errorf("opened %d times for 5 asks, want 1", opens)
	}
}

// A tree nobody asks about is never opened. An image with no CONFIG_ claim
// should not pay for reading a kernel.
func TestLazyDoesNotOpenUnaskedTrees(t *testing.T) {
	opens := 0
	r := Registry{
		"fake":   Erase(fake, fakeCfg{known: map[string]string{"A": "y"}}),
		"unused": Wrapped(Erase(fake, fakeCfg{known: nil, opens: &opens}), Lazy()),
	}
	p := plan.Claim(plan.Ask{Op: plan.Holds, Tree: "fake", Name: "A"})
	if _, err := Run(p, r); err != nil {
		t.Fatal(err)
	}
	if opens != 0 {
		t.Errorf("opened an unasked tree %d times", opens)
	}
}

// A tree with no interpreter answers Unknown rather than failing: that is what
// `silt check` without --linux means, and it now holds for every kind.
func TestMissingInterpreterIsUnknown(t *testing.T) {
	p := plan.Claim(plan.Ask{Op: plan.Holds, Tree: "nosuchtree", Name: "X"})
	got, err := Run(p, Registry{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed() {
		t.Error("a tree with no interpreter was reported as a failure")
	}
}

// Nil must survive erasure. A kind that cannot emit has to stay
// distinguishable from one that emits an empty file.
func TestNilCapabilitiesSurviveErasure(t *testing.T) {
	o := Erase(fake, fakeCfg{})
	if o.CanEmit() || o.CanSettle() || o.CanSolve() {
		t.Error("a kind with nil Emit/Settle/Solve claims it can")
	}

	with := Interp[fakeCfg, *fakeDefs]{
		Open:   fake.Open,
		Answer: fake.Answer,
		Emit:   func(*fakeDefs, any) (File, error) { return File{Name: "x"}, nil },
	}
	if o := Erase(with, fakeCfg{}); !o.CanEmit() || o.CanSettle() {
		t.Error("Emit lost or Settle invented")
	}
}

func TestMemoAsksOnce(t *testing.T) {
	calls := 0
	counted := func(o Opaque) Opaque {
		next := o.answer
		o.answer = func(d any, k plan.Ask) plan.Answer { calls++; return next(d, k) }
		return o
	}
	r := Registry{"fake": Wrapped(Erase(fake, fakeCfg{known: map[string]string{"A": "y"}}),
		Memo(), counted)}

	// The same question five times, from five different positions, which is
	// what fifty-six images asking about one symbol looks like.
	asks := make([]plan.Ask, 5)
	for i := range asks {
		asks[i] = plan.Ask{Op: plan.Exists, Tree: "fake", Name: "A", Pos: fmt.Sprintf("f%d.sx:1", i)}
	}
	if _, err := Run(plan.Traverse(asks, plan.Claim), r); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("asked %d times, want 1 (position must not split the cache)", calls)
	}
}

func TestTrace(t *testing.T) {
	var buf bytes.Buffer
	r := Registry{"fake": Wrapped(Erase(fake, fakeCfg{known: map[string]string{"A": "y"}}),
		Trace(&buf))}
	p := plan.Traverse([]plan.Ask{
		{Op: plan.Holds, Tree: "fake", Name: "A"},
		{Op: plan.Holds, Tree: "fake", Name: "B"},
	}, plan.Claim)
	if _, err := Run(p, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "A") || !strings.Contains(out, "ok") {
		t.Errorf("trace missing the answered question:\n%s", out)
	}
	if !strings.Contains(out, "not a symbol") {
		t.Errorf("trace missing the failure:\n%s", out)
	}
}

// Or: ask the first, fall back when it does not know. A board's overlays over
// an SoC's base tree.
func TestOrFallsBack(t *testing.T) {
	base := Erase(fake, fakeCfg{known: map[string]string{"SOC": "yes"}})
	over := Erase(fake, fakeCfg{known: map[string]string{"BOARD": "yes"}})
	r := Registry{"fake": Or(over, base)}

	p := plan.Traverse([]plan.Ask{
		{Op: plan.Settled, Tree: "fake", Name: "BOARD"},
		{Op: plan.Settled, Tree: "fake", Name: "SOC"},
	}, func(k plan.Ask) plan.Plan[string] {
		return plan.Lift(k, func(a plan.Answer) string { return a.Value })
	})

	got, err := Run(p, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "yes" || got[1] != "yes" {
		t.Errorf("got %v; the fallback did not answer", got)
	}
}

func TestPinnedRefusesAWrongVersion(t *testing.T) {
	o := Erase(fake, fakeCfg{known: map[string]string{"A": "y"}})
	o.Version = "2025.02.16"
	r := Registry{"fake": Wrapped(o, Pinned("2024.11.1"))}

	got, err := Run(plan.Claim(plan.Ask{Op: plan.Holds, Tree: "fake", Name: "A"}), r)
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
	if !Erase(fake, fakeCfg{}).Affects("ANYTHING") {
		t.Error("a kind with no Affects claimed a setting does not matter")
	}

	with := Interp[fakeCfg, *fakeDefs]{
		Open: fake.Open, Answer: fake.Answer,
		Affects: func(n string) bool { return n != "BR2_JLEVEL" },
	}
	o := Erase(with, fakeCfg{})
	if o.Affects("BR2_JLEVEL") {
		t.Error("Affects not consulted")
	}
	if !o.Affects("BR2_PACKAGE_X") {
		t.Error("Affects said a package does not matter")
	}
}

func TestOpenFailureNamesTheTree(t *testing.T) {
	broken := Interp[fakeCfg, *fakeDefs]{
		Open:   func(fakeCfg) (*fakeDefs, error) { return nil, errors.New("no such directory") },
		Answer: fake.Answer,
	}
	_, err := Run(plan.Claim(plan.Ask{Op: plan.Holds, Tree: "fake", Name: "A"}),
		Registry{"fake": Erase(broken, fakeCfg{})})
	if err == nil || !strings.Contains(err.Error(), "fake:") {
		t.Errorf("error does not name the tree: %v", err)
	}
}
