package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/emit"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
)

// Compose parses rendered fragment and image text and composes the image. It
// works on the text rather than on Imported directly, so what is checked is
// exactly what would be written to disk.
func Compose(tree *kconfig.Tree, image string, fragments ...string) (*compose.Result, error) {
	lib := compose.NewLibrary()
	lib.Tree = tree
	for i, src := range fragments {
		f, err := lang.ParseFile(src, fmt.Sprintf("fragment-%d.sx", i))
		if err != nil {
			return nil, err
		}
		if err := lib.Add(f); err != nil {
			return nil, err
		}
	}
	f, err := lang.ParseFile(image, "image.sx")
	if err != nil {
		return nil, err
	}
	if len(f.Images) != 1 {
		return nil, fmt.Errorf("expected one image, got %d", len(f.Images))
	}
	return lib.Compose(f.Images[0])
}

// Emit writes the composition's Buildroot defconfig into dir and returns its
// path. Linux constraints are not handled: imported fragments have none.
func Emit(res *compose.Result, dir string) (string, error) {
	path := filepath.Join(dir, "emitted_defconfig")
	def, err := emit.BuildrootDefconfig(res, nil)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, []byte(def), 0o644)
}

// DeriveProvides returns the declared capabilities whose symbol is on in a
// kbuild .config, sorted.
func DeriveProvides(cfg map[string]string, decls map[string]lang.CapabilityDecl) []string {
	var out []string
	for name, d := range decls {
		// kbuild's .config here is Buildroot's; a capability bound to
		// another tree cannot be read off it.
		if d.Symbol.Tree != string(lang.Buildroot) {
			continue
		}
		if v := cfg[d.Symbol.Name]; v == "y" || v == "m" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// UndecidableCapabilities lists the declared capabilities no defconfig can
// settle: those with no symbol to read a value from, minus the ones some
// profile already provides. dhcp-client is an outcome of userspace, so it is
// profile:minimal's to claim and never a board's.
func UndecidableCapabilities(decls map[string]lang.CapabilityDecl, byProfiles map[string]bool) []string {
	var out []string
	for name, d := range decls {
		if d.Symbol.IsZero() && !byProfiles[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ProfileCapabilities is what the library's profiles provide.
func ProfileCapabilities(frags map[string]*lang.Fragment) map[string]bool {
	out := map[string]bool{}
	for _, fr := range frags {
		if fr.ID.Kind != lang.Profile {
			continue
		}
		for _, c := range fr.Provides {
			out[c.Name] = true
		}
	}
	return out
}
