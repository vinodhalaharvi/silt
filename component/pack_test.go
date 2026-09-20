package component_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/component"
	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/emit"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/pack"
	"github.com/vinodhalaharvi/silt/verify"
)

// fixture is one pack: its tree declaration, its fragments, and which test
// binaries it ships as which component files.
type fixture struct {
	tree       string
	fragments  string
	components map[string]string // path under files/components → testdata binary
	noDeps     bool              // ship only silt.wit, not wit/deps
}

const gatewayTree = `(tree agent-gateway
  (kind wasm-component)
  (component "files/components/gateway.wasm")
  (wit "wit"))`

const carried = `(fragment feature:gateway
  (buildroot (path-append BR2_ROOTFS_OVERLAY "files")))`

const readOnly = `(fragment profile:read-only
  (scope agent-gateway (n SILT__MODBUS__WRITE)))`

func (f fixture) build(t *testing.T) *pack.Pack {
	t.Helper()
	dir := t.TempDir()
	ext := filepath.Join(dir, "br2-external")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "silt.sx"), `(pack demo (version "0.1.0") (external "br2-external")
  (provides (feature gateway) (profile read-only)))`+"\n"+f.tree)
	// Every image needs exactly one target; this one says nothing else.
	write(filepath.Join(dir, "fragments", "all.sx"), `(fragment target:board (doc "a board"))`+f.fragments)
	write(filepath.Join(ext, "external.desc"), "name: DEMO\n")

	witSrc, _ := filepath.Abs(filepath.Join("testdata", "wit"))
	copyFile(t, filepath.Join(witSrc, "silt.wit"), filepath.Join(ext, "wit", "silt.wit"))
	if !f.noDeps {
		copyFile(t, filepath.Join(witSrc, "deps", "opcua.wit"), filepath.Join(ext, "wit", "deps", "opcua.wit"))
	}
	comps := f.components
	if comps == nil {
		comps = map[string]string{"gateway.wasm": "gateway.wasm"}
	}
	for dst, src := range comps {
		copyFile(t, filepath.Join("testdata", src), filepath.Join(ext, "files", "components", dst))
	}
	p, err := pack.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func compose1(t *testing.T, p *pack.Pack, image string) *compose.Result {
	t.Helper()
	lib := compose.NewLibrary()
	for _, f := range p.Files {
		if err := lib.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	f, err := lang.ParseFile(image, "image.sx")
	if err != nil {
		t.Fatal(err)
	}
	res, err := lib.Compose(f.Images[0])
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// check runs both stages exactly as silt check does.
func check(t *testing.T, res *compose.Result) []verify.Finding {
	t.Helper()
	var out []verify.Finding
	for _, d := range component.Trees(res) {
		ct, err := component.Load(d)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, verify.Check(res, ct.Kconfig(), d).Findings...)
		out = append(out, component.Findings(res, ct)...)
	}
	return out
}

func expect(t *testing.T, got []verify.Finding, wants ...string) {
	t.Helper()
	if len(got) != len(wants) {
		t.Fatalf("got %d finding(s), want %d:\n%v", len(got), len(wants), got)
	}
	for i, w := range wants {
		if !strings.Contains(got[i].String(), w) {
			t.Errorf("finding %d is %q, want it to mention %q", i, got[i], w)
		}
	}
}

const image = `(image line3 (compose target:board feature:gateway profile:read-only))`

func TestReadOnlyGatewayPasses(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + readOnly}.build(t)
	expect(t, check(t, compose1(t, p, image)))
}

// The one the product exists for: a component that imports what the policy
// forbids.
func TestForbiddenImportIsAFinding(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + readOnly,
		components: map[string]string{"gateway.wasm": "writer.wasm"}}.build(t)
	expect(t, check(t, compose1(t, p, image)),
		"SILT__MODBUS__WRITE is forbidden, and files/components/gateway.wasm imports silt:modbus/write")
}

// Stage one. Without the vocabulary this would pass forever: the misspelled
// symbol is not imported by anything, so (n ...) of it is trivially true.
func TestMisspelledPolicyIsAFinding(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + `(fragment profile:read-only
  (scope agent-gateway (n SILT__MODBUS__WRTIE)))`}.build(t)
	expect(t, check(t, compose1(t, p, image)), "SILT__MODBUS__WRTIE does not exist")
}

func TestOnlyNIsAccepted(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + `(fragment profile:read-only
  (scope agent-gateway (y SILT__MODBUS__READ)))`}.build(t)
	expect(t, check(t, compose1(t, p, image)), "component trees take only (n ...)")
}

// Policy about a component the image does not carry checks a file the image
// does not have.
func TestUncarriedComponentIsAFinding(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: `(fragment feature:gateway (doc "carries nothing"))` +
		readOnly}.build(t)
	expect(t, check(t, compose1(t, p, image)), "is checked but not carried into the image")
}

// Fail closed: an interface the pack's WIT cannot name is one no policy can
// forbid, so importing it is a finding rather than a pass.
func TestImportOutsideVocabularyIsAFinding(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + readOnly, noDeps: true}.build(t)
	expect(t, check(t, compose1(t, p, image)),
		"imports silt:opcua/browse@0.1.0, which the pack's WIT does not define",
		"imports wasi:http/incoming-handler@0.3.0, which the pack's WIT does not define")
}

// A carried component with no policy still gets the checks that need none.
func TestCarriedWithoutPolicyStillChecked(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + `(fragment profile:read-only (doc "no policy"))`,
		components: map[string]string{"gateway.wasm": "kinds.wasm"}}.build(t)
	got := check(t, compose1(t, p, image))
	expect(t, got,
		"imports silt:plugin/driver, which the pack's WIT does not define",
		`imports "log", which is not a WIT interface`,
		`imports "handle", which is not a WIT interface`,
		`imports "blob", which is not a WIT interface`)
}

// A tree the image neither states policy for nor carries is not its business.
func TestUnrelatedTreeDoesNotApply(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: `(fragment feature:gateway (doc "nothing"))` +
		`(fragment profile:read-only (doc "nothing"))`}.build(t)
	if trees := component.Trees(compose1(t, p, image)); len(trees) != 0 {
		t.Fatalf("applies to an image that does not touch it: %v", trees)
	}
}

// A component tree configures nothing: emit must neither write a file for it
// nor demand a consumed-by it cannot have.
func TestEmitSkipsComponentTrees(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + readOnly}.build(t)
	res := compose1(t, p, image)
	if trees := emit.Trees(res); len(trees) != 0 {
		t.Fatalf("emit.Trees = %v", trees)
	}
	if _, err := emit.BuildrootDefconfig(res, map[string]string{"agent-gateway": "/x"}); err != nil {
		t.Fatal(err)
	}
}

// The claim is "no import, no path", and it holds only for the bytes check
// read. Swapping the binary or widening the policy must change the image's
// identity.
func TestSolutionHashCoversComponentsAndPolicy(t *testing.T) {
	hash := func(f fixture) string {
		res := compose1(t, f.build(t), image)
		if s := emit.Solution(res, nil, nil); !strings.Contains(s, "component agent-gateway files/components/gateway.wasm ") ||
			(strings.Contains(f.fragments, "SILT__MODBUS__WRITE") && !strings.Contains(s, "policy agent-gateway n SILT__MODBUS__WRITE")) {
			t.Fatalf("solution does not name the component and its policy:\n%s", s)
		}
		return emit.SolutionHash(res, nil, nil)
	}
	base := hash(fixture{tree: gatewayTree, fragments: carried + readOnly})
	if base != hash(fixture{tree: gatewayTree, fragments: carried + readOnly}) {
		t.Fatal("the same inputs hashed differently")
	}
	if base == hash(fixture{tree: gatewayTree, fragments: carried + readOnly,
		components: map[string]string{"gateway.wasm": "writer.wasm"}}) {
		t.Error("swapping the component did not change the hash")
	}
	if base == hash(fixture{tree: gatewayTree, fragments: carried +
		`(fragment profile:read-only (doc "policy removed"))`}) {
		t.Error("removing the policy did not change the hash")
	}
}

func TestWhy(t *testing.T) {
	p := fixture{tree: gatewayTree, fragments: carried + readOnly}.build(t)
	res := compose1(t, p, image)
	ct, err := component.Load(res.Trees["agent-gateway"])
	if err != nil {
		t.Fatal(err)
	}
	if s := ct.Why("SILT__MODBUS__READ"); !strings.Contains(s, "imported by files/components/gateway.wasm") {
		t.Errorf("read: %s", s)
	}
	if s := ct.Why("SILT__MODBUS__WRITE"); !strings.Contains(s, "imported by nothing") {
		t.Errorf("write: %s", s)
	}
	if s := ct.Why("NOPE"); !strings.Contains(s, "not an interface the pack's WIT defines") {
		t.Errorf("unknown: %s", s)
	}
}

// Components resolve against the br2-external tree or not at all. A tree
// declared outside a pack has no base that agrees with what the image carries.
func TestComponentTreesNeedAPack(t *testing.T) {
	f, err := lang.ParseFile(gatewayTree, "lib.sx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := component.Load(*f.Trees[0]); err == nil ||
		!strings.Contains(err.Error(), "not declared by a pack") {
		t.Fatalf("got %v", err)
	}
}

// The component line is what a reviewer compares with sha256sum on the
// device. It must be the plain hash of the bytes and nothing else, or an
// honest binary would read as a swapped one.
func TestComponentLineMatchesSha256sum(t *testing.T) {
	res := compose1(t, fixture{tree: gatewayTree, fragments: carried + readOnly}.build(t), image)
	data, err := os.ReadFile(filepath.Join("testdata", "gateway.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := "component agent-gateway files/components/gateway.wasm " + hex.EncodeToString(sum[:])
	if s := emit.Solution(res, nil, nil); !strings.Contains(s, want+"\n") {
		t.Fatalf("solution lacks %q:\n%s", want, s)
	}
}
