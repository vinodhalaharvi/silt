package compose

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
)

const dtgt = `(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`
const dprof = `(fragment profile:p (requires (capability mmu)) (buildroot (y BR2_INIT_BUSYBOX)))`
const dfeat = `(fragment feature:a (buildroot (y BR2_PACKAGE_DHCPCD)))`
const dfeat2 = `(fragment feature:b (buildroot (y BR2_PACKAGE_DROPBEAR)))`

func derived(t *testing.T, imgs string, name string) (*Result, error) {
	t.Helper()
	l := lib(t, dtgt, dprof, dfeat, dfeat2, imgs)
	return l.Compose(l.Images[name])
}

// A family differing by a feature or two should not repeat the base.
func TestDerivedImageInheritsFragments(t *testing.T) {
	r, err := derived(t, `
(image base (compose target:t profile:p feature:a))
(image dev  (compose image:base feature:b))`, "dev")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range r.Fragments {
		got[f.ID.String()] = true
	}
	for _, want := range []string{"target:t", "profile:p", "feature:a", "feature:b"} {
		if !got[want] {
			t.Errorf("derived image missing %s: %v", want, got)
		}
	}
}

// Declarations describe the same build; dropping them would silently change it.
func TestDerivedImageInheritsDeclarations(t *testing.T) {
	r, err := derived(t, `
(image base (compose target:t profile:p)
  (opaque (value buildroot:BR2_GLOBAL_PATCH_DIR "board/x"))
  (unmanaged buildroot:BR2_TARGET_UBOOT_*))
(image dev (compose image:base feature:b))`, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Opaque) != 1 || len(r.Unmanaged) != 1 {
		t.Fatalf("opaque %d, unmanaged %d", len(r.Opaque), len(r.Unmanaged))
	}
}

// The two exactly-one slots come from the base. Naming either alongside is
// ambiguous rather than an override, and saying so beats picking one.
func TestDerivedImageMayNotReplaceTargetOrProfile(t *testing.T) {
	for _, bad := range []string{
		`(image dev (compose image:base target:t))`,
		`(image dev (compose image:base profile:p))`,
	} {
		_, err := lang.ParseFile(bad, "t.sx")
		if err == nil || !strings.Contains(err.Error(), "from the base") {
			t.Errorf("%s: got %v", bad, err)
		}
	}
}

func TestDerivationCycleIsReported(t *testing.T) {
	_, err := derived(t, `
(image a (compose image:b feature:a))
(image b (compose image:a feature:b))`, "a")
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("got %v", err)
	}
}

// A chain that never reaches a target is caught once flat, which is the only
// point at which the count is knowable.
func TestFlattenedShapeIsChecked(t *testing.T) {
	_, err := derived(t, `
(image base (compose target:t profile:p))
(image dev  (compose image:base feature:b))
(image odd  (compose image:missing feature:b))`, "odd")
	if err == nil || !strings.Contains(err.Error(), "no such image") {
		t.Fatalf("got %v", err)
	}
}

// The base's fragments were checked against one release and cannot be assumed
// correct for another.
func TestConflictingPinIsAnError(t *testing.T) {
	_, err := derived(t, `
(image base (compose target:t profile:p) (verified-against (buildroot "2025.02.16")))
(image dev  (compose image:base feature:b) (verified-against (buildroot "2024.11.1")))`, "dev")
	if err == nil || !strings.Contains(err.Error(), "pins") {
		t.Fatalf("got %v", err)
	}
}

func TestPinIsInheritedWhenUnstated(t *testing.T) {
	l := lib(t, dtgt, dprof, dfeat, dfeat2, `
(image base (compose target:t profile:p) (verified-against (buildroot "2025.02.16")))
(image dev  (compose image:base feature:b))`)
	flat, err := l.flatten(l.Images["dev"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if flat.VerifiedAgainst["buildroot"] != "2025.02.16" {
		t.Fatalf("pin not inherited: %v", flat.VerifiedAgainst)
	}
}
