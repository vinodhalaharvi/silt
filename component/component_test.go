package component

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Every import kind, from a binary wasm-tools encoded. The nested component's
// import is satisfied inside the binary and must not be reported.
func TestImportsEveryKind(t *testing.T) {
	got, err := Imports(read(t, "kinds.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"silt:modbus/read@0.1.0", "log", "handle", "silt:plugin/driver",
		"blob", "wasi:clocks/monotonic-clock@0.3.0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestImportsRejectsCoreModule(t *testing.T) {
	if _, err := Imports(read(t, "module.wasm")); !errors.Is(err, ErrCoreModule) {
		t.Fatalf("got %v, want ErrCoreModule", err)
	}
}

func TestImportsRejectsGarbageAndTruncation(t *testing.T) {
	if _, err := Imports([]byte("not wasm at all")); err == nil {
		t.Fatal("accepted a non-wasm file")
	}
	data := read(t, "gateway.wasm")
	for n := 9; n < len(data); n += 7 {
		if _, err := Imports(data[:n]); err == nil {
			t.Fatalf("accepted a binary truncated to %d of %d bytes", n, len(data))
		}
	}
}

// The two name forms no current encoder emits, so no fixture can contain
// them: the legacy 0x01 discriminator and 0x02 with options. Both must yield
// the plain name, and 0x02's options must be consumed so the next import
// is read from the right place.
func TestImportsNameForms(t *testing.T) {
	str := func(s string) []byte { return append([]byte{byte(len(s))}, s...) }
	var sec []byte
	sec = append(sec, 3) // three imports
	sec = append(sec, 0x01)
	sec = append(sec, str("a:b/legacy")...)
	sec = append(sec, 0x05, 0x00) // instance 0
	sec = append(sec, 0x02)
	sec = append(sec, str("a:b/options")...)
	sec = append(sec, 2, 0x01)
	sec = append(sec, str("1.2.3")...)
	sec = append(sec, 0x02)
	sec = append(sec, str("id")...)
	sec = append(sec, 0x02, 0x01, 0x7f) // value of primitive type
	sec = append(sec, 0x00)
	sec = append(sec, str("a:b/after")...)
	sec = append(sec, 0x03, 0x00, 0x00) // type eq 0

	bin := []byte{0x00, 'a', 's', 'm', 0x0d, 0x00, 0x01, 0x00, sectionImport, byte(len(sec))}
	bin = append(bin, sec...)
	got, err := Imports(bin)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a:b/legacy", "a:b/options", "a:b/after"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSymbol(t *testing.T) {
	for in, want := range map[string]string{
		"silt:modbus/read@0.1.0":            "SILT__MODBUS__READ",
		"wasi:clocks/monotonic-clock@0.3.0": "WASI__CLOCKS__MONOTONIC_CLOCK",
		"wasi:http/incoming-handler":        "WASI__HTTP__INCOMING_HANDLER",
		"acme:plc-io/digital-out":           "ACME__PLC_IO__DIGITAL_OUT",
	} {
		got, err := Symbol(in)
		if err != nil || got != want {
			t.Errorf("Symbol(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"log", "a:b", "a/b", ":b/c", "a:/c", "a:b/", "a:b/c/d", "a:b:c/d", "a:b/c--d"} {
		if s, err := Symbol(bad); err == nil {
			t.Errorf("Symbol(%q) = %q, want an error", bad, s)
		}
	}
}

// The reason for the double underscore: with one, these two collide and a
// policy forbidding one silently forbids the other.
func TestSymbolIsInjective(t *testing.T) {
	a, _ := Symbol("a-b:c/d")
	b, _ := Symbol("a:b-c/d")
	if a == b {
		t.Fatalf("a-b:c/d and a:b-c/d both map to %s", a)
	}
}

func TestVocabulary(t *testing.T) {
	v, err := Vocabulary([]string{filepath.Join("testdata", "wit")})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"SILT__MODBUS__READ":            "silt:modbus/read",
		"SILT__MODBUS__WRITE":           "silt:modbus/write",
		"SILT__OPCUA__BROWSE":           "silt:opcua/browse",
		"WASI__HTTP__INCOMING_HANDLER":  "wasi:http/incoming-handler",
		"WASI__CLOCKS__MONOTONIC_CLOCK": "wasi:clocks/monotonic-clock",
	}
	if len(v) != len(want) {
		var got []string
		for s := range v {
			got = append(got, s)
		}
		t.Fatalf("got %d interfaces %v, want %d; the inline world interface must not count",
			len(v), got, len(want))
	}
	for sym, name := range want {
		if v[sym].Name != name {
			t.Errorf("%s: got %q, want %q", sym, v[sym].Name, name)
		}
	}
	if it := v["SILT__MODBUS__WRITE"]; !strings.HasSuffix(it.File, "silt.wit") || it.Line != 11 {
		t.Errorf("write is located at %s:%d, want silt.wit:11", it.File, it.Line)
	}
}

func TestVocabularyNeedsWIT(t *testing.T) {
	if _, err := Vocabulary([]string{t.TempDir()}); err == nil {
		t.Fatal("an empty directory gave a vocabulary")
	}
}

// wasm-tools is the reference: every fixture's top-level imports as this
// reader sees them must match what wasm-tools prints. Skipped where it is not
// installed, since it is a test oracle and not a dependency.
func TestAgainstWasmTools(t *testing.T) {
	if _, err := exec.LookPath("wasm-tools"); err != nil {
		t.Skip("wasm-tools not on PATH")
	}
	top := regexp.MustCompile(`(?m)^  \(import "([^"]+)"`)
	wats, _ := filepath.Glob(filepath.Join("testdata", "*.wat"))
	for _, wat := range wats {
		if strings.HasSuffix(wat, "module.wat") {
			continue
		}
		bin := filepath.Join(t.TempDir(), "x.wasm")
		if out, err := exec.Command("wasm-tools", "parse", wat, "-o", bin).CombinedOutput(); err != nil {
			t.Fatalf("%s: %s", wat, out)
		}
		printed, err := exec.Command("wasm-tools", "print", bin).Output()
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, m := range top.FindAllStringSubmatch(string(printed), -1) {
			want = append(want, m[1])
		}
		data, _ := os.ReadFile(bin)
		got, err := Imports(data)
		if err != nil {
			t.Fatalf("%s: %v", wat, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: reader %q, wasm-tools %q", wat, got, want)
		}
		// The committed binary must be what the committed text encodes, or
		// the other tests are about a file nobody can regenerate.
		committed := read(t, strings.TrimSuffix(filepath.Base(wat), ".wat")+".wasm")
		if !reflect.DeepEqual(committed, data) {
			t.Errorf("%s: committed .wasm differs from wasm-tools parse", wat)
		}
	}
}

// A WIT directory is one package, and only one of its files needs to say
// which. WASI's own cli package does exactly this: stdio.wit and
// environment.wit have no package line. The first scanner assumed every file
// declared its own and rejected the real WASI definitions.
func TestVocabularyDirectoryPackage(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Named so the headerless file sorts first: inheritance must not depend
	// on the declaring file having been read already.
	write("a-stdio.wit", "@since(version = 0.2.0)\ninterface stdout {\n  f: func();\n}\n")
	write("b-command.wit", "package wasi:cli@0.2.6;\n\ninterface exit {\n  g: func();\n}\n")
	v, err := Vocabulary([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if v["WASI__CLI__STDOUT"].Name != "wasi:cli/stdout" || v["WASI__CLI__EXIT"].Name != "wasi:cli/exit" {
		t.Fatalf("got %v", v)
	}

	// Two files claiming different packages for one directory is not a
	// package WIT would accept, and guessing which one wins would be wrong.
	write("c-other.wit", "package wasi:io@0.2.6;\n")
	if _, err := Vocabulary([]string{dir}); err == nil || !strings.Contains(err.Error(), "same directory") {
		t.Fatalf("got %v", err)
	}
}
