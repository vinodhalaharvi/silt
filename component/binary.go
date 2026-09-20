// Package component reads what a WebAssembly component can reach, for the
// wasm-component tree kind.
//
// A component's imports are in its binary, and it cannot call what it did not
// import. That makes "what may this code do?" a question about the artifact
// rather than about a promise, and it is the only reason this package exists:
// Silt can check a component's imports against an image's policy before
// anything ships, the way it checks a fragment's symbols against Kconfig.
//
// Only the top-level import section is read. A component nests core modules
// and other components and wires them together internally; those imports are
// satisfied inside the binary and reach nothing on the host. What the host
// must provide, and therefore what the component can touch, is exactly its
// top-level imports.
package component

import (
	"bytes"
	"errors"
	"fmt"
)

var magic = []byte{0x00, 'a', 's', 'm'}

// The preamble's last two bytes are the layer: 0 for a core module, 1 for a
// component. The two before them are the version, which the component
// binary format has changed through its proposal phase (0x0d at the time of
// writing) and which this reader therefore does not pin.
const (
	layerModule    = 0
	layerComponent = 1
)

const sectionImport = 10

// ErrCoreModule is returned for a core module. A core module's imports are
// module.field pairs with no interface names, so there is nothing a policy
// written against WIT interfaces could say about it. WASI 0.3 exists only for
// components in any case.
var ErrCoreModule = errors.New("is a core WebAssembly module, not a component; " +
	"component trees check components (wasm-tools component new makes one)")

// Imports returns the names a component imports at its top level, in binary
// order.
func Imports(data []byte) ([]string, error) {
	if len(data) < 8 || !bytes.Equal(data[:4], magic) {
		return nil, errors.New("is not a WebAssembly binary")
	}
	layer := uint16(data[6]) | uint16(data[7])<<8
	switch layer {
	case layerModule:
		return nil, ErrCoreModule
	case layerComponent:
	default:
		return nil, fmt.Errorf("has unknown layer %d", layer)
	}

	r := &reader{b: data, pos: 8}
	var names []string
	for !r.done() {
		id, err := r.byte()
		if err != nil {
			return nil, err
		}
		size, err := r.u32()
		if err != nil {
			return nil, err
		}
		end := r.pos + int(size)
		if end > len(r.b) || end < r.pos {
			return nil, r.errf("section %d runs past the end of the binary", id)
		}
		if id == sectionImport {
			sec := &reader{b: r.b[:end], pos: r.pos}
			got, err := importSection(sec)
			if err != nil {
				return nil, err
			}
			if !sec.done() {
				return nil, sec.errf("import section has %d trailing bytes", end-sec.pos)
			}
			names = append(names, got...)
		}
		// Everything else, nested modules and components included, is
		// skipped by its declared size and never interpreted.
		r.pos = end
	}
	return names, nil
}

func importSection(r *reader) ([]string, error) {
	count, err := r.u32()
	if err != nil {
		return nil, err
	}
	var names []string
	for i := uint32(0); i < count; i++ {
		name, err := externName(r)
		if err != nil {
			return nil, err
		}
		if err := externDesc(r); err != nil {
			return nil, fmt.Errorf("import %q: %w", name, err)
		}
		names = append(names, name)
	}
	return names, nil
}

// externName reads an import's name.
//
// 0x00 is the form the spec requires. 0x01 is the older interface-name
// discriminator, which encoders emitted until 2024 and wasmparser still
// accepts as a synonym. 0x02 carries options after the name — implements,
// version suffix, external id — none of which change what is imported.
func externName(r *reader) (string, error) {
	form, err := r.byte()
	if err != nil {
		return "", err
	}
	switch form {
	case 0x00, 0x01:
		return r.str()
	case 0x02:
		name, err := r.str()
		if err != nil {
			return "", err
		}
		n, err := r.u32()
		if err != nil {
			return "", err
		}
		for j := uint32(0); j < n; j++ {
			opt, err := r.byte()
			if err != nil {
				return "", err
			}
			if opt > 0x02 {
				return "", r.errf("unknown name option 0x%02x on %q", opt, name)
			}
			if _, err := r.str(); err != nil {
				return "", err
			}
		}
		return name, nil
	}
	return "", r.errf("unknown import name form 0x%02x", form)
}

// externDesc skips an import's type descriptor. Its length depends on its
// kind, and the next import starts where it ends.
func externDesc(r *reader) error {
	kind, err := r.byte()
	if err != nil {
		return err
	}
	switch kind {
	case 0x00: // core module: 0x00 0x11 typeidx
		b, err := r.byte()
		if err != nil {
			return err
		}
		if b != 0x11 {
			return r.errf("core sort 0x%02x is not a module", b)
		}
		_, err = r.u32()
		return err
	case 0x01, 0x04, 0x05: // func, component, instance: typeidx
		_, err := r.u32()
		return err
	case 0x02: // value: 0x00 valueidx | 0x01 valtype
		b, err := r.byte()
		if err != nil {
			return err
		}
		if b > 0x01 {
			return r.errf("unknown value bound 0x%02x", b)
		}
		// A valtype is a signed LEB (primitives are negative, type indices
		// positive). Both are skipped the same way.
		return r.skipLEB()
	case 0x03: // type: 0x00 typeidx | 0x01 (sub resource)
		b, err := r.byte()
		if err != nil {
			return err
		}
		switch b {
		case 0x00:
			_, err = r.u32()
			return err
		case 0x01:
			return nil
		}
		return r.errf("unknown type bound 0x%02x", b)
	}
	return r.errf("unknown import kind 0x%02x", kind)
}

type reader struct {
	b   []byte
	pos int
}

func (r *reader) done() bool { return r.pos >= len(r.b) }

func (r *reader) errf(format string, args ...any) error {
	return fmt.Errorf("at byte %d: %s", r.pos, fmt.Sprintf(format, args...))
}

func (r *reader) byte() (byte, error) {
	if r.done() {
		return 0, r.errf("unexpected end of binary")
	}
	b := r.b[r.pos]
	r.pos++
	return b, nil
}

func (r *reader) u32() (uint32, error) {
	var v uint32
	for shift := 0; shift < 35; shift += 7 {
		b, err := r.byte()
		if err != nil {
			return 0, err
		}
		v |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, nil
		}
	}
	return 0, r.errf("LEB128 longer than five bytes")
}

func (r *reader) skipLEB() error {
	for i := 0; i < 10; i++ {
		b, err := r.byte()
		if err != nil {
			return err
		}
		if b&0x80 == 0 {
			return nil
		}
	}
	return r.errf("LEB128 longer than ten bytes")
}

func (r *reader) str() (string, error) {
	n, err := r.u32()
	if err != nil {
		return "", err
	}
	end := r.pos + int(n)
	if end > len(r.b) || end < r.pos {
		return "", r.errf("string runs past the end of the binary")
	}
	s := string(r.b[r.pos:end])
	r.pos = end
	return s, nil
}
