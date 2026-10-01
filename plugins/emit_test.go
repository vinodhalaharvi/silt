package plugins_test

import (
	"strings"
	"testing"

	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/plugins"
)

// A kind that writes nothing is not a kind that writes an empty file, and a
// kind Buildroot does not consume is not an error. Both were special cases in
// emit before the kinds were separated - the second one demanded a
// (consumed-by ...) symbol that could not exist for a tree built on another
// processor.
func TestEmitPerKind(t *testing.T) {
	root := repoRoot(t)
	lib := loadLibrary(t, root)

	var garage *lang.Image
	for _, im := range allImages(t, root) {
		if im.Name == "raspberrypi3-64-garage" {
			garage = im
		}
	}
	if garage == nil {
		t.Skip("the garage image is not in this library")
	}

	res, err := lib.Compose(garage)
	if err != nil {
		t.Fatal(err)
	}
	out, err := plugins.EmitAll(res, lib.Trees)
	if err != nil {
		t.Fatal(err)
	}

	byTree := map[string]plugins.Emitted{}
	for _, e := range out {
		byTree[e.Tree] = e
	}

	// Three kinds, three answers: a defconfig, a kernel fragment wired in
	// through a symbol, and a YAML file wired in through nothing.
	if _, ok := byTree["buildroot"]; !ok {
		t.Error("no defconfig")
	}
	if e, ok := byTree["linux"]; !ok {
		t.Error("no linux fragment")
	} else if e.ConsumedBy == "" {
		t.Error("the linux fragment is not handed to Buildroot")
	}
	e, ok := byTree["esphome"]
	if !ok {
		t.Fatal("no esphome document")
	}
	if e.ConsumedBy != "" {
		t.Errorf("esphome claims to be consumed by %s; esphome builds it", e.ConsumedBy)
	}
	if !strings.Contains(string(e.Body), "mqtt:") {
		t.Errorf("esphome document looks wrong:\n%s", e.Body)
	}
}
