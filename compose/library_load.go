package compose

import (
	"os"
	"path/filepath"

	"github.com/vinodhalaharvi/silt/lang"
)

// LibraryPaths returns every .sx file that makes up this repository's library,
// in the order silt's own loader reads them, given the path to the repository
// root.
//
// It exists because three tests had three hand-written sets of globs and no two
// agreed. Each disagreement surfaced as something that read like a source bug
// and was not: an image composing image:something failed with "no such image"
// because images were not loaded; a pack keeping its fragments directly under
// fragments/ rather than in features/ failed with "no such fragment"; and a
// capability declared in a pack's silt.sx failed with "provides capability
// no-console-login, which is not declared". Each was found separately, days
// apart, and fixed in one test while the others kept their own version of the
// mistake.
//
// So: one list, used by all of them. A pack or a layout this misses is missed
// everywhere at once, which is a bug that gets fixed rather than three bugs
// that look like three different problems.
func LibraryPaths(root string) []string {
	patterns := []string{
		// The library proper: capabilities and rules at the top, fragments
		// below.
		"fragments/*.sx",
		"fragments/*/*.sx",
		// Packs keep fragments at either depth, and their silt.sx declares
		// the capabilities those fragments provide.
		"packs/*/fragments/*.sx",
		"packs/*/fragments/*/*.sx",
		"packs/*/silt.sx",
		// Images, because an image composing image:something needs the file
		// that defines it.
		"images/*.sx",
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range patterns {
		m, _ := filepath.Glob(filepath.Join(root, p))
		for _, f := range m {
			if !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// LoadLibrary parses everything LibraryPaths finds and adds it to lib.
func LoadLibrary(lib *Library, root string) error {
	for _, p := range LibraryPaths(root) {
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := lang.ParseFile(string(data), p)
		if err != nil {
			return err
		}
		if err := lib.Add(f); err != nil {
			return err
		}
	}
	return nil
}

// Externals returns the br2-external directories of every pack, absolute, for
// passing to kbuild as BR2_EXTERNAL. Without them the symbols a pack defines do
// not exist, and every symbol an image states about one looks like a request
// kbuild dropped.
func Externals(root string) []string {
	m, _ := filepath.Glob(filepath.Join(root, "packs/*/br2-external"))
	out := make([]string, 0, len(m))
	for _, e := range m {
		if a, err := filepath.Abs(e); err == nil {
			out = append(out, a)
		}
	}
	return out
}
