package lang

import "testing"

func TestComponentTreeDecl(t *testing.T) {
	f := parse(t, `(tree gw (kind wasm-component)
	  (component "files/a.wasm") (component "files/b.wasm") (wit "wit"))`)
	d := f.Trees[0]
	if d.Kind != KindWasmComponent || !d.CheckOnly() {
		t.Fatalf("%+v", d)
	}
	if len(d.Components) != 2 || d.Components[1] != "files/b.wasm" || d.Wit[0] != "wit" {
		t.Fatalf("components %v, wit %v", d.Components, d.Wit)
	}
}

// A clause that means nothing for the tree's kind is an error rather than
// ignored: a component tree with consumed-by reads as though its policy
// reached Buildroot, and a Kconfig tree with a component as though that
// binary were checked.
func TestTreeClausesMatchKind(t *testing.T) {
	rejects(t, `(tree gw (kind wasm-component) (wit "wit"))`, "names no (component")
	rejects(t, `(tree gw (kind wasm-component) (component "a.wasm"))`, "names no (wit")
	rejects(t, `(tree gw (kind wasm-component) (component "a.wasm") (wit "w")
	  (consumed-by BR2_X))`, "configures nothing")
	rejects(t, `(tree gw (kind wasm-component) (component "a.wasm") (wit "w")
	  (prefix "X_"))`, "not prefix, source or env")
	rejects(t, `(tree k (kind kconfig) (component "a.wasm"))`, "belong to wasm-component trees")
	rejects(t, `(tree k (kind devicetree))`, "not a constraint system")
	if parse(t, `(tree k (kind kconfig) (prefix "K_"))`).Trees[0].CheckOnly() {
		t.Fatal("a kconfig tree reports check-only")
	}
}
