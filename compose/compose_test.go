package compose

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
)

func lib(t *testing.T, srcs ...string) *Library {
	t.Helper()
	l := NewLibrary()
	for i, s := range srcs {
		f, err := lang.ParseFile(s, "t"+string(rune('0'+i))+".sx")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := l.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

const target = `(fragment target:t
  (buildroot (y BR2_aarch64))
  (provides (capability mmu) (capability virtio)))`

const profile = `(fragment profile:p
  (buildroot (y BR2_INIT_BUSYBOX))
  (requires (capability mmu)))`

func composeSrc(t *testing.T, l *Library, src string) (*Result, error) {
	t.Helper()
	f, err := lang.ParseFile(src, "img.sx")
	if err != nil {
		t.Fatal(err)
	}
	return l.Compose(f.Images[0])
}

func TestComposeMerges(t *testing.T) {
	l := lib(t, target, profile)
	r, err := composeSrc(t, l, `(image i (compose target:t profile:p))`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Constraints[lang.Buildroot]) != 2 {
		t.Fatalf("want 2 merged constraints, got %v", r.Constraints[lang.Buildroot])
	}
}

// The central claim against merge_config.sh: a genuine conflict is an error
// naming both sources, not a silent last-wins.
func TestConflictIsAnErrorNamingBothSources(t *testing.T) {
	a := `(fragment profile:p (buildroot (y BR2_PACKAGE_SYSTEMD)) (requires (capability mmu)))`
	b := `(fragment feature:f (buildroot (n BR2_PACKAGE_SYSTEMD)))`
	l := lib(t, target, a, b)
	_, err := composeSrc(t, l, `(image i (compose target:t profile:p feature:f))`)
	if err == nil {
		t.Fatal("expected a conflict")
	}
	msg := err.Error()
	for _, want := range []string{"BR2_PACKAGE_SYSTEMD", "profile:p", "feature:f", "t1.sx:1", "t2.sx:1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("conflict message missing %q:\n%s", want, msg)
		}
	}
}

// (y X) satisfies (at-least m X). These must not be reported as conflicting.
func TestAtLeastIsNotAConflict(t *testing.T) {
	a := `(fragment profile:p (linux (at-least m CONFIG_MAC80211)) (requires (capability mmu)))`
	b := `(fragment feature:f (linux (y CONFIG_MAC80211)))`
	l := lib(t, target, a, b)
	r, err := composeSrc(t, l, `(image i (compose target:t profile:p feature:f))`)
	if err != nil {
		t.Fatalf("y should satisfy at-least m: %v", err)
	}
	c := r.Constraints[lang.Linux][0]
	if c.AtLeast || c.Want != lang.Y {
		t.Errorf("the stronger constraint should win, got %+v", c)
	}
}

func TestMissingCapabilityFailsFast(t *testing.T) {
	f := `(fragment feature:cam (requires (capability csi-camera)) (buildroot (y BR2_PACKAGE_LIBCAMERA)))`
	l := lib(t, target, profile, f)
	_, err := composeSrc(t, l, `(image i (compose target:t profile:p feature:cam))`)
	if err == nil {
		t.Fatal("expected a capability failure")
	}
	for _, want := range []string{"csi-camera", "feature:cam", "target:t", "mmu", "virtio"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q:\n%s", want, err)
		}
	}
}

func TestMissingFragment(t *testing.T) {
	l := lib(t, target, profile)
	_, err := composeSrc(t, l, `(image i (compose target:t profile:p feature:nope))`)
	if err == nil || !strings.Contains(err.Error(), "feature:nope") {
		t.Fatalf("got %v", err)
	}
}

// Overrides are the one permitted last-wins, and the displaced constraint is
// recorded so it can still be explained.
func TestOverrideRecordsWhatItDisplaced(t *testing.T) {
	p := `(fragment profile:p (buildroot (value BR2_TARGET_ROOTFS_EXT2_SIZE "120M")) (requires (capability mmu)))`
	l := lib(t, target, p)
	r, err := composeSrc(t, l, `(image i (compose target:t profile:p)
	  (override (value BR2_TARGET_ROOTFS_EXT2_SIZE "256M")))`)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Overridden) != 1 || r.Overridden[0].Value != "120M" {
		t.Fatalf("displaced constraint not recorded: %+v", r.Overridden)
	}
	for _, c := range r.Constraints[lang.Buildroot] {
		if c.Symbol == "BR2_TARGET_ROOTFS_EXT2_SIZE" && c.Value != "256M" {
			t.Errorf("override did not win: %+v", c)
		}
	}
}

func TestDuplicateFragmentRejected(t *testing.T) {
	l := NewLibrary()
	f, _ := lang.ParseFile(target, "a.sx")
	if err := l.Add(f); err != nil {
		t.Fatal(err)
	}
	g, _ := lang.ParseFile(target, "b.sx")
	if err := l.Add(g); err == nil {
		t.Fatal("expected a redefinition error")
	}
}

// Profiles provide capabilities too. An earlier rule allowed only targets, and
// two images disproved it: each profile supplies its own DHCP client, which is
// policy rather than hardware and is exactly what a feature needs.
func TestProfileCanProvide(t *testing.T) {
	l := lib(t,
		`(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`,
		`(fragment profile:p (requires (capability mmu)) (provides (capability dhcp-client)))`,
		`(fragment feature:net (requires (capability dhcp-client)))`)
	if _, err := composeSrc(t, l, `(image i (compose target:t profile:p feature:net))`); err != nil {
		t.Fatalf("a profile must be able to satisfy a feature's requirement: %v", err)
	}
}

// Without a profile supplying it, the feature fails at compose rather than at
// boot, which is where it used to fail.
func TestMissingDhcpClientFailsEarly(t *testing.T) {
	l := lib(t,
		`(fragment target:t (buildroot (y BR2_aarch64)) (provides (capability mmu)))`,
		`(fragment profile:bare (requires (capability mmu)))`,
		`(fragment feature:net (requires (capability dhcp-client)))`)
	_, err := composeSrc(t, l, `(image i (compose target:t profile:bare feature:net))`)
	if err == nil || !strings.Contains(err.Error(), "dhcp-client") {
		t.Fatalf("got %v", err)
	}
}
