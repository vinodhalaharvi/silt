package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/importer"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/solve"
	"github.com/vinodhalaharvi/silt/solver"
	"github.com/vinodhalaharvi/silt/verify"
)

// cmdImport turns a Buildroot defconfig into a target fragment, a profile
// fragment and an image, and checks the result three ways before writing:
// every symbol against the tree, the composition against the model, and, with
// --kbuild, the emitted defconfig against kbuild's own savedefconfig of the
// source. Only the last one proves the import lost nothing.
func cmdImport(args []string) error {
	var src, brDir, root, name string
	root = "."
	force, useKbuild, dryRun := false, false, false
	for i := 0; i < len(args); i++ {
		next := func() (string, error) {
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s needs a value", args[i-1])
			}
			return args[i], nil
		}
		var err error
		switch args[i] {
		case "--buildroot":
			brDir, err = next()
		case "--root":
			root, err = next()
		case "--name":
			name, err = next()
		case "-f":
			force = true
		case "--kbuild":
			useKbuild = true
		case "-n", "--dry-run":
			dryRun = true
		default:
			if src != "" {
				return fmt.Errorf("import takes one defconfig, got %q and %q", src, args[i])
			}
			src = args[i]
		}
		if err != nil {
			return err
		}
	}
	if src == "" || brDir == "" {
		return fmt.Errorf("usage: silt import DEFCONFIG --buildroot DIR [--kbuild] [--name NAME] [--root DIR] [-f] [-n]")
	}
	if name == "" {
		name = importer.NameFor(src)
	}
	// Provenance should read the same on every machine: configs/x_defconfig,
	// not whatever path this checkout happens to live at.
	shown := src
	if a, err := filepath.Abs(src); err == nil {
		if b, err := filepath.Abs(brDir); err == nil {
			if rel, err := filepath.Rel(b, a); err == nil && !strings.HasPrefix(rel, "..") {
				shown = rel
			}
		}
	}

	ver, err := kconfig.TreeVersion(brDir)
	if err != nil {
		return fmt.Errorf("%s does not look like a Buildroot tree: %w", brDir, err)
	}
	tree, err := loadTree(brDir)
	if err != nil {
		return err
	}

	var kb *importer.Kbuild
	var provides, undecidable []string
	input := src
	if useKbuild {
		tmp, err := os.MkdirTemp("", "silt-import-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		kb = &importer.Kbuild{Root: brDir, Out: tmp}
		// Normalising first makes a full .config importable and strips lines
		// kbuild would derive anyway. That judgment is kbuild's to make.
		norm, err := kb.Normalize(src)
		if err != nil {
			return err
		}
		// Capabilities the library ties to a symbol are read off kbuild's
		// .config of the source, the only place they can be derived from.
		if cfg, err := kb.Config(); err == nil {
			decls, byProfiles, err := libraryCapabilities(filepath.Join(root, "fragments"))
			if err != nil {
				return err
			}
			provides = importer.DeriveProvides(cfg, decls)
			undecidable = importer.UndecidableCapabilities(decls, byProfiles)
		}
		input = filepath.Join(tmp, "source_defconfig")
		if err := os.WriteFile(input, []byte(norm), 0o644); err != nil {
			return err
		}
	}

	f, err := os.Open(input)
	if err != nil {
		return err
	}
	entries, err := importer.ParseDefconfig(f, shown)
	f.Close()
	if err != nil {
		return err
	}
	im, err := importer.Import(name, shown, ver, entries, tree)
	if err != nil {
		return err
	}

	im.Provides = provides
	im.Undecidable = undecidable
	targetSrc := im.Fragment(importer.Target)
	profileSrc := im.Fragment(importer.Profile)
	imageSrc := im.Image()

	fmt.Printf("imported %s  %d symbols from %s, buildroot %s\n\n", name, len(im.Entries), shown, ver)
	for _, e := range im.Entries {
		v := e.Raw
		if e.Unset {
			v = "n"
		}
		if len(v) > 40 {
			v = v[:37] + "..."
		}
		fmt.Printf("  %-8s %-48s %-40s %s\n", e.Class, e.Symbol, v, e.Reason)
	}
	fmt.Printf("\n  target %d, profile %d\n\n", im.Count(importer.Target), im.Count(importer.Profile))

	res, err := importer.Compose(tree, imageSrc, targetSrc, profileSrc)
	if err != nil {
		return fmt.Errorf("imported fragments do not compose: %w", err)
	}
	rep := verify.Check(res, tree, lang.BuiltinTrees()[string(lang.Buildroot)])
	if !rep.OK() {
		for _, fd := range rep.Findings {
			fmt.Printf("  %s\n", fd)
		}
		return fmt.Errorf("imported fragments do not verify against the tree")
	}
	fmt.Printf("check     ok, %d symbols verified\n", rep.Checked)
	sv := solve.Solve(res, tree)
	if sv.Status != solver.SAT {
		// kbuild accepted this defconfig, so an UNSAT here is a model bug,
		// not a bad import. That is exactly how 172 dead symbols were found.
		fmt.Print(sv.Explain(name))
		return fmt.Errorf("the model rejects a defconfig kbuild accepts: a Silt importer or lowering bug")
	}
	fmt.Printf("solve     SAT\n")

	if kb != nil {
		want, err := os.ReadFile(input)
		if err != nil {
			return err
		}
		emitted, err := importer.Emit(res, kb.Out)
		if err != nil {
			return err
		}
		got, err := kb.Normalize(emitted)
		if err != nil {
			return err
		}
		if got != string(want) {
			fmt.Printf("roundtrip DIFFERS from savedefconfig of the source:\n%s", lineDiff(string(want), got))
			return fmt.Errorf("import is lossy")
		}
		fmt.Printf("roundtrip identical: emit -> defconfig -> savedefconfig matches the source\n")
	}

	files := map[string]string{
		filepath.Join(root, "fragments", "targets", name+".sx"):  targetSrc,
		filepath.Join(root, "fragments", "profiles", name+".sx"): profileSrc,
		filepath.Join(root, "images", name+".sx"):                imageSrc,
	}
	if dryRun {
		fmt.Println()
		for _, p := range []string{"fragments/targets", "fragments/profiles", "images"} {
			path := filepath.Join(root, p, name+".sx")
			fmt.Printf(";; ---- %s\n%s\n", path, files[path])
		}
		return nil
	}
	for path := range files {
		if _, err := os.Stat(path); err == nil && !force {
			return fmt.Errorf("%s exists; pass -f to overwrite", path)
		}
	}
	fmt.Println()
	for _, p := range []string{"fragments/targets", "fragments/profiles", "images"} {
		path := filepath.Join(root, p, name+".sx")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(files[path]), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote     %s\n", path)
	}
	return nil
}

// lineDiff lists lines present in only one of two files.
func lineDiff(want, got string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			if l != "" {
				m[l] = true
			}
		}
		return m
	}
	w, g := in(want), in(got)
	var b strings.Builder
	for _, l := range strings.Split(want, "\n") {
		if l != "" && !g[l] {
			fmt.Fprintf(&b, "  - %s\n", l)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if l != "" && !w[l] {
			fmt.Fprintf(&b, "  + %s\n", l)
		}
	}
	if b.Len() == 0 {
		b.WriteString("  (same lines, different order)\n")
	}
	return b.String()
}

// libraryCapabilities reads the capability vocabulary from a fragment
// library, if there is one. A missing library is not an error: import works
// outside a Silt checkout, it just derives no capabilities.
func libraryCapabilities(dir string) (map[string]lang.CapabilityDecl, map[string]bool, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, nil, nil
	}
	files, err := loadAll([]string{dir})
	if err != nil {
		return nil, nil, err
	}
	lib := compose.NewLibrary()
	for _, f := range files {
		if err := lib.Add(f); err != nil {
			return nil, nil, err
		}
	}
	return lib.Declared, importer.ProfileCapabilities(lib.Fragments), nil
}
