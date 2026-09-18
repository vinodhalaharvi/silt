package compose

import (
	"os"
	"sort"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
)

// Composing two independent features gives exactly the union of composing each
// alone: no interaction term, no pairwise special case.
//
// This is the combinatorics argument, and it is the one claim the project had
// not tested where it should show. The factoring experiment on vendor
// defconfigs came back weak — boards share almost nothing — but boards are not
// where the saving lives. Protocol features are, and they compose disjointly.
//
// A failure here is more interesting than a pass: it would mean two features
// that look independent are not, and the fragment boundaries are wrong.
func TestFeaturesComposeDisjointly(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/Config.in", []byte(`
config BR2_ARCH
	bool "arch"
config BR2_PKG_A
	bool "a"
config BR2_PKG_B1
	bool "b1"
config BR2_PKG_B2
	bool "b2"
`), 0o644)

	frags := []string{
		`(fragment target:t (buildroot (y BR2_ARCH)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)))`,
		`(fragment feature:a (buildroot (y BR2_PKG_A)))`,
		`(fragment feature:b (buildroot (y BR2_PKG_B1) (y BR2_PKG_B2)))`,
	}
	symbols := func(image string) []string {
		l := lib(t, append(frags, image)...)
		var name string
		for n := range l.Images {
			name = n
		}
		r, err := l.Compose(l.Images[name])
		if err != nil {
			t.Fatalf("%s: %v", image, err)
		}
		var out []string
		for _, c := range r.Constraints[lang.Scope("buildroot")] {
			out = append(out, c.Sym.String())
		}
		sort.Strings(out)
		return out
	}

	onlyA := symbols(`(image x (compose target:t profile:p feature:a))`)
	onlyB := symbols(`(image x (compose target:t profile:p feature:b))`)
	both := symbols(`(image x (compose target:t profile:p feature:a feature:b))`)

	union := map[string]bool{}
	for _, s := range append(append([]string{}, onlyA...), onlyB...) {
		union[s] = true
	}
	if len(union) != len(both) {
		t.Fatalf("both = %v, union of each = %d symbols", both, len(union))
	}
	for _, s := range both {
		if !union[s] {
			t.Errorf("%s appears only when both features are composed; "+
				"the features are not independent", s)
		}
	}
}
