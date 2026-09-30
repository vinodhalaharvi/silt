package esphome

import (
	"strings"
	"testing"
)

func doc(pairs ...string) *Doc {
	d := New()
	for i := 0; i+1 < len(pairs); i += 2 {
		d.Set(pairs[i], pairs[i+1])
	}
	return d
}

func TestNesting(t *testing.T) {
	out := doc(
		"esphome.name", "garage",
		"esp32.board", "esp32dev",
		"esp32.framework.type", "esp-idf",
		"wifi.ssid", "!secret wifi_ssid",
	).Render()

	for _, want := range []string{
		"esphome:\n  name: \"garage\"\n",
		"esp32:\n  board: \"esp32dev\"\n  framework:\n    type: \"esp-idf\"\n",
		"wifi:\n  ssid: \"!secret wifi_ssid\"\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing:\n%s\ngot:\n%s", want, out)
		}
	}
}

// A section that ESPHome writes as a list of entries, each with an id: a
// three-part path names the entry in the middle, so two fragments can each add
// a setting to the same entry.
func TestListSections(t *testing.T) {
	out := doc(
		"switch.toggle.platform", "gpio",
		"switch.toggle.pin", "26",
		"switch.toggle.on_turn_on.delay", "500ms",
		"switch.warn.platform", "template",
	).Render()

	want := `switch:
  - id: toggle
    on_turn_on:
      delay: "500ms"
    pin: 26
    platform: "gpio"
  - id: warn
    platform: "template"
`
	if !strings.Contains(out, want) {
		t.Errorf("got:\n%s\nwant to contain:\n%s", out, want)
	}
}

// Quoting is not cosmetic. An unquoted "on" is boolean true in YAML, a topic
// with a colon in it parses as a mapping, and a pin named D1 is a string that
// looks like nothing in particular. Numbers and booleans stay bare because
// ESPHome's schema expects them that way.
func TestScalarQuoting(t *testing.T) {
	cases := map[string]string{
		"26":                "26",
		"3.3":               "3.3",
		"true":              "true",
		"false":             "false",
		"on":                `"on"`,
		"garage/door/state": `"garage/door/state"`,
		"192.168.68.50":     `"192.168.68.50"`,
		"D1":                `"D1"`,
		"":                  `""`,
		`say "hi"`:          `"say \"hi\""`,
	}
	for in, want := range cases {
		if got := scalar(in); got != want {
			t.Errorf("scalar(%q) = %s, want %s", in, got, want)
		}
	}
}

// An unknown top-level section is written as a mapping rather than a list,
// which is the safer guess: esphome rejects a mapping where it wants a list,
// loudly, and silently drops the keys of a list written where it wants a
// mapping.
func TestUnknownSectionIsAMapping(t *testing.T) {
	out := doc("mqtt.broker", "10.0.0.1", "mqtt.topic_prefix", "garage").Render()
	if !strings.Contains(out, "mqtt:\n  broker: \"10.0.0.1\"\n  topic_prefix: \"garage\"\n") {
		t.Errorf("got:\n%s", out)
	}
	if strings.Contains(out, "- id:") {
		t.Error("wrote mqtt as a list")
	}
}

// Deterministic: the same paths in any order produce the same document, which
// is what makes an emitted file diffable and a build reproducible.
func TestDeterministic(t *testing.T) {
	a := doc("b.x", "1", "a.y", "2", "a.x", "3").Render()
	b := doc("a.x", "3", "b.x", "1", "a.y", "2").Render()
	if a != b {
		t.Errorf("order changed the output:\n%s\n---\n%s", a, b)
	}
}
