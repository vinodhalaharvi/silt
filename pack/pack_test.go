package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
)

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func packDir(t *testing.T, decl, frag string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "silt.sx", decl)
	if frag != "" {
		write(t, dir, "fragments/features/f.sx", frag)
	}
	return dir
}

const goodDecl = `(pack demo (version "0.1.0")
  (requires (buildroot "2025.02.16"))
  (provides (feature widget)))`

func TestLoadAndCheck(t *testing.T) {
	dir := packDir(t, goodDecl, `(fragment feature:widget (buildroot (y BR2_PACKAGE_WIDGET)))`)
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Decl.Name != "demo" || p.Decl.Version != "0.1.0" {
		t.Fatalf("%+v", p.Decl)
	}
	if probs := p.Check(map[string]string{"buildroot": "2025.02.16"}); len(probs) != 0 {
		t.Fatalf("%v", probs)
	}
	// A tree this run is not checking against is not checked, rather than
	// assumed to match.
	if probs := p.Check(nil); len(probs) != 0 {
		t.Fatalf("%v", probs)
	}
}

// The declaration is an interface, so it is checked both ways.
func TestCheckBothDirections(t *testing.T) {
	missing := packDir(t, goodDecl, "")
	p, err := Load(missing)
	if err != nil {
		t.Fatal(err)
	}
	if probs := p.Check(nil); len(probs) != 1 || !strings.Contains(probs[0].Message, "none of its fragments define") {
		t.Fatalf("a promise with nothing behind it: %v", probs)
	}

	extra := packDir(t, goodDecl, `(fragment feature:widget (buildroot (y BR2_A)))
(fragment profile:secret (buildroot (y BR2_B)))`)
	p, err = Load(extra)
	if err != nil {
		t.Fatal(err)
	}
	probs := p.Check(nil)
	if len(probs) != 1 || !strings.Contains(probs[0].Message, "profile:secret") {
		t.Fatalf("an undeclared fragment is one the pack does not know it maintains: %v", probs)
	}
}

func TestCheckVersions(t *testing.T) {
	dir := packDir(t, goodDecl, `(fragment feature:widget (buildroot (y BR2_A)))`)
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	probs := p.Check(map[string]string{"buildroot": "2024.11"})
	if len(probs) != 1 || !strings.Contains(probs[0].Message, "needs buildroot 2025.02.16") {
		t.Fatalf("%v", probs)
	}
}

func TestLoadRejects(t *testing.T) {
	for name, files := range map[string][2]string{
		"fragments in the declaration file": {
			`(pack demo (version "1") (provides (feature f)))
(fragment feature:f (buildroot (y BR2_A)))`, ""},
		"no version": {`(pack demo (provides (feature f)))`, ""},
		"external that is not one": {
			`(pack demo (version "1") (external "nowhere"))`, ""},
	} {
		dir := packDir(t, files[0], files[1])
		if _, err := Load(dir); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Load(t.TempDir()); err == nil {
		t.Error("a directory with no silt.sx is not a pack")
	}
}

// Version comparison is deliberately small: exact or >=, and anything else is
// an error rather than a guess, because a constraint nobody enforces reads as
// a promise.
func TestSatisfies(t *testing.T) {
	for _, c := range []struct {
		have, want string
		ok         bool
	}{
		{"2025.02.16", "2025.02.16", true},
		{"2025.02.16", "2025.02.15", false},
		{"6.12", ">=6.1", true},
		{"6.12", ">=6.13", false},
		{"6.12.3", ">=6.12", true},
		{"6.1", ">=6.12", false},
		{"2025.02.16", ">=2024.11", true},
	} {
		got, err := lang.Satisfies(c.have, c.want)
		if err != nil {
			t.Errorf("%s %s: %v", c.have, c.want, err)
		}
		if got != c.ok {
			t.Errorf("%s satisfies %s: got %v, want %v", c.have, c.want, got, c.ok)
		}
	}
	if _, err := lang.Satisfies("1.0", "^1.0"); err == nil {
		t.Error("an unsupported constraint should be an error, not a guess")
	}
}

// The reference pack in this repository is the one a third party would copy,
// so it is checked like any other.
func TestHelloSiltPack(t *testing.T) {
	p, err := Load("../packs/hello-silt")
	if err != nil {
		t.Fatal(err)
	}
	if probs := p.Check(map[string]string{"buildroot": "2025.02.16"}); len(probs) != 0 {
		t.Fatalf("%v", probs)
	}
	if p.External == "" {
		t.Error("the pack should carry its br2-external tree")
	}
	if len(p.Decl.Provides) != 1 || p.Decl.Provides[0] != (lang.ID{Kind: lang.Feature, Name: "hello-silt"}) {
		t.Errorf("provides: %v", p.Decl.Provides)
	}
}

// A (path ...) names a file the pack carries. It is checked at load, which is
// before anything composes or builds, and rewritten into the form Buildroot
// expands so the emitted defconfig names no machine's directory layout.
func TestPathsResolveAgainstThePack(t *testing.T) {
	dir := packDir(t, `(pack demo (version "1") (external "br2-external")
	  (provides (feature widget)))`,
		`(fragment feature:widget (buildroot (path BR2_ROOTFS_OVERLAY "overlay")))`)
	write(t, dir, "br2-external/external.desc", "name: DEMO\ndesc: demo\n")
	write(t, dir, "overlay/etc/hello", "hi\n")

	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Files[1].Fragments[0].Constraints["buildroot"][0]
	if c.Value != "$(BR2_EXTERNAL_DEMO_PATH)/overlay" {
		t.Errorf("value: %q", c.Value)
	}
	if c.Resolved != filepath.Join(dir, "overlay") {
		t.Errorf("resolved: %q", c.Resolved)
	}

	// A path the pack does not contain is the whole point: the symbol is
	// real, the value looks fine, and every other check passes.
	missing := packDir(t, `(pack demo (version "1") (external "br2-external")
	  (provides (feature widget)))`,
		`(fragment feature:widget (buildroot (path BR2_ROOTFS_OVERLAY "overlay")))`)
	write(t, missing, "br2-external/external.desc", "name: DEMO\n")
	if _, err := Load(missing); err == nil || !strings.Contains(err.Error(), "does not contain") {
		t.Fatalf("a missing overlay should be an error: %v", err)
	}

	// Without an external tree there is nothing for Buildroot to resolve the
	// path against, so it is refused rather than emitted as someone's
	// absolute path.
	noext := packDir(t, `(pack demo (version "1") (provides (feature widget)))`,
		`(fragment feature:widget (buildroot (path BR2_ROOTFS_OVERLAY "overlay")))`)
	write(t, noext, "overlay/etc/hello", "hi\n")
	if _, err := Load(noext); err == nil || !strings.Contains(err.Error(), "external") {
		t.Fatalf("a path with no external tree: %v", err)
	}
}

// The board pack in this repository, checked like any other. It is the one
// written as though by a stranger: it carries its own board files rather than
// pointing into somebody's Buildroot checkout.
func TestNanoPiPack(t *testing.T) {
	p, err := Load("../packs/nanopi-r2s")
	if err != nil {
		t.Fatal(err)
	}
	if probs := p.Check(map[string]string{"buildroot": "2025.02.16"}); len(probs) != 0 {
		t.Fatalf("%v", probs)
	}
	var paths []lang.Constraint
	for _, f := range p.Files {
		for _, fr := range f.Fragments {
			for _, c := range fr.Constraints["buildroot"] {
				if c.IsPath {
					paths = append(paths, c)
				}
			}
		}
	}
	if len(paths) != 2 {
		t.Fatalf("expected the post-build script and the genimage config: %v", paths)
	}
	for _, c := range paths {
		if !strings.Contains(c.Value, "$(BR2_EXTERNAL_NANOPI_R2S_PATH)/board/") {
			t.Errorf("%s should be emitted through the external path: %q", c.Sym, c.Value)
		}
		if _, err := os.Stat(c.Resolved); err != nil {
			t.Errorf("%s: %v", c.Sym, err)
		}
	}
	// One of them sits inside a longer value, which is why (as ...) exists.
	for _, c := range paths {
		if c.Sym.Name == "BR2_ROOTFS_POST_SCRIPT_ARGS" && !strings.HasPrefix(c.Value, "-c ") {
			t.Errorf("the genimage config is passed as an argument: %q", c.Value)
		}
	}
}
