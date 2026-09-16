// Command silt composes S-expression fragments into Buildroot and Linux
// configuration.
//
// Implemented: Rungs 1 and 2 of DESIGN.md §12 — parse, canonicalize, hash,
// compose, emit. Solving, explanation and repair are Rungs 5 through 8 and are
// not present; commands that would need them say so rather than guessing.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/emit"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/sexpr"
)

const usage = `silt — composable S-expressions over Kconfig

  silt check [PATH...]              parse and validate; report problems
  silt emit IMAGE.sx [-o DIR]       compose and write defconfig + linux.config
  silt fmt [-w] [PATH...]           canonical form
  silt hash [PATH...]               content address of each file
  silt why SYMBOL IMAGE.sx          why a symbol has the value it has

Fragments are loaded from ./fragments by default; override with -L DIR.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = cmdCheck(os.Args[2:])
	case "emit":
		err = cmdEmit(os.Args[2:])
	case "fmt":
		err = cmdFmt(os.Args[2:])
	case "hash":
		err = cmdHash(os.Args[2:])
	case "why":
		err = cmdWhy(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		os.Exit(1)
	}
}

func expand(paths []string) ([]string, error) {
	if len(paths) == 0 {
		paths = []string{"fragments", "images"}
	}
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".sx") {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func loadAll(paths []string) ([]*lang.File, error) {
	files, err := expand(paths)
	if err != nil {
		return nil, err
	}
	var out []*lang.File
	for _, p := range files {
		src, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		f, err := lang.ParseFile(string(src), p)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func cmdCheck(args []string) error {
	files, err := loadAll(args)
	if err != nil {
		return err
	}
	lib := compose.NewLibrary()
	var images []*lang.Image
	nf, nr := 0, 0
	for _, f := range files {
		if err := lib.Add(f); err != nil {
			return err
		}
		nf += len(f.Fragments)
		nr += len(f.Rules)
		images = append(images, f.Images...)
	}
	for _, im := range images {
		if _, err := lib.Compose(im); err != nil {
			return fmt.Errorf("image %s: %w", im.Name, err)
		}
	}
	fmt.Printf("ok  %d files, %d fragments, %d rule blocks, %d images\n",
		len(files), nf, nr, len(images))
	return nil
}

func cmdEmit(args []string) error {
	var target, outDir, libDir string
	outDir = "out"
	libDir = "fragments"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o":
			i++
			if i >= len(args) {
				return fmt.Errorf("-o needs a directory")
			}
			outDir = args[i]
		case "-L":
			i++
			if i >= len(args) {
				return fmt.Errorf("-L needs a directory")
			}
			libDir = args[i]
		default:
			target = args[i]
		}
	}
	if target == "" {
		return fmt.Errorf("emit needs an image file")
	}

	files, err := loadAll([]string{libDir})
	if err != nil {
		return err
	}
	lib := compose.NewLibrary()
	for _, f := range files {
		if err := lib.Add(f); err != nil {
			return err
		}
	}
	src, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	imf, err := lang.ParseFile(string(src), target)
	if err != nil {
		return err
	}
	if len(imf.Images) != 1 {
		return fmt.Errorf("%s: expected exactly one image, found %d", target, len(imf.Images))
	}
	im := imf.Images[0]
	r, err := lib.Compose(im)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	br := filepath.Join(outDir, "defconfig")
	lx := filepath.Join(outDir, "linux.config")
	if err := os.WriteFile(br, []byte(emit.Defconfig(r, lang.Buildroot)), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(lx, []byte(emit.Defconfig(r, lang.Linux)), 0o644); err != nil {
		return err
	}

	report(r, br, lx)
	return nil
}

// report prints the managed / opaque / unmanaged split. Invariant 9: a loss
// that does not appear in the output is a bug, whatever else is true about it.
func report(r *compose.Result, br, lx string) {
	fmt.Printf("composed %s\n", r.Image.Name)
	for _, f := range r.Fragments {
		fmt.Printf("  %-28s %s\n", f.ID, f.Pos.Short())
	}
	caps := make([]string, 0, len(r.Capabilities))
	for c := range r.Capabilities {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	fmt.Printf("\ncapabilities  %s\n", strings.Join(caps, " "))
	hardBR, softBR := split(r.Constraints[lang.Buildroot])
	hardLX, softLX := split(r.Constraints[lang.Linux])
	fmt.Printf("stated        %d buildroot, %d linux\n", hardBR, hardLX)
	if softBR+softLX > 0 {
		fmt.Printf("soft          %d  not emitted; needs MaxSAT (Rung 7)\n", softBR+softLX)
	}
	fmt.Printf("guards        %d\n", len(r.Guards))
	if n := len(r.Derived); n > 0 {
		fmt.Printf("derived       %d  by cross-tree rules:\n", n)
		for _, d := range r.Derived {
			fmt.Printf("                %s=%s  %s\n",
				d.Constraint.Symbol, d.Constraint.Want, d.Rule.Pos.Short())
		}
	}
	if n := len(r.Opaque); n > 0 {
		fmt.Printf("opaque        %d  carried verbatim, not solved\n", n)
	}
	if n := len(r.Environment); n > 0 {
		fmt.Printf("env-derived   %d  recorded in the solution hash\n", n)
	}
	if n := len(r.Unmanaged); n > 0 {
		fmt.Printf("unmanaged     %d prefixes\n", n)
	}
	if n := len(r.Overridden); n > 0 {
		fmt.Printf("overridden    %d  displaced by an explicit (override ...)\n", n)
	}
	fmt.Printf("\nwrote %s\n      %s\n", br, lx)
	fmt.Printf("\nnot solved: this is a partial config (Rung 2). Complete it with:\n")
	abs, err := filepath.Abs(br)
	if err != nil {
		abs = br
	}
	fmt.Printf("  make defconfig BR2_DEFCONFIG=%s && make olddefconfig\n", abs)
}

func split(cs []lang.Constraint) (hard, soft int) {
	for _, c := range cs {
		if c.Soft {
			soft++
		} else {
			hard++
		}
	}
	return
}

func cmdFmt(args []string) error {
	write := false
	var rest []string
	for _, a := range args {
		if a == "-w" {
			write = true
			continue
		}
		rest = append(rest, a)
	}
	paths, err := expand(rest)
	if err != nil {
		return err
	}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		nodes, err := sexpr.Parse(string(src), p)
		if err != nil {
			return err
		}
		var b strings.Builder
		for _, n := range nodes {
			b.WriteString(sexpr.Canonical(n))
			b.WriteByte('\n')
		}
		if write {
			if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
				return err
			}
		} else {
			fmt.Print(b.String())
		}
	}
	return nil
}

func cmdHash(args []string) error {
	paths, err := expand(args)
	if err != nil {
		return err
	}
	for _, p := range paths {
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		nodes, err := sexpr.Parse(string(src), p)
		if err != nil {
			return err
		}
		for _, n := range nodes {
			fmt.Printf("%s  %s\n", sexpr.Hash(n), p)
		}
	}
	return nil
}

// cmdWhy explains one symbol. At Rung 2 it can answer for stated and
// rule-derived symbols; symbols that only a solved model would settle are
// reported as such rather than guessed at.
func cmdWhy(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: silt why SYMBOL IMAGE.sx")
	}
	sym, target := args[0], args[1]

	files, err := loadAll([]string{"fragments"})
	if err != nil {
		return err
	}
	lib := compose.NewLibrary()
	for _, f := range files {
		if err := lib.Add(f); err != nil {
			return err
		}
	}
	src, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	imf, err := lang.ParseFile(string(src), target)
	if err != nil {
		return err
	}
	if len(imf.Images) != 1 {
		return fmt.Errorf("%s: expected exactly one image", target)
	}
	r, err := lib.Compose(imf.Images[0])
	if err != nil {
		return err
	}

	if s, ok := r.ExplainDerived(sym); ok {
		fmt.Print(s)
		return nil
	}
	for _, sc := range []lang.Scope{lang.Buildroot, lang.Linux} {
		for _, c := range r.Constraints[sc] {
			if c.Symbol != sym {
				continue
			}
			kind := "stated"
			if c.Soft {
				kind = "preferred (soft; not emitted before Rung 7)"
			}
			fmt.Printf("%s = %s\n  %s by %s at %s\n",
				sym, valueOf(c), kind, c.From, c.Pos.Short())
			return nil
		}
	}
	fmt.Printf("%s is not stated by any composed fragment and no rule derives it.\n", sym)
	fmt.Printf("Its value would be decided by Kconfig defaults, which needs the\n")
	fmt.Printf("importer and the solver (Rungs 4-7).\n")
	return nil
}

func valueOf(c lang.Constraint) string {
	if c.IsValue {
		return "\"" + c.Value + "\""
	}
	if c.AtLeast {
		return "at-least " + c.Want.String()
	}
	return c.Want.String()
}
