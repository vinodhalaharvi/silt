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
	"github.com/vinodhalaharvi/silt/fixpoint"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/pack"
	"github.com/vinodhalaharvi/silt/sexpr"
	"github.com/vinodhalaharvi/silt/solve"
	"github.com/vinodhalaharvi/silt/solver"
	"github.com/vinodhalaharvi/silt/verify"
)

const usage = `silt — composable S-expressions over Kconfig

  silt check [PATH...]              parse and validate; report problems
  silt check --buildroot DIR        also verify every claim against that tree
        [--external DIR]            with a br2-external tree (any command)
        [--pack DIR]                with a pack: fragments and its tree (any command)
        [--linux DIR]               and every CONFIG_* claim against a kernel tree
  silt emit IMAGE.sx [-o DIR]       compose and write defconfig + linux.config
        [--buildroot DIR]           let rules see select-implied symbols
  silt fmt [-w] [PATH...]           canonical form
  silt hash [PATH...]               content address of each file
  silt hash --solution IMAGE.sx --buildroot DIR [--linux DIR]
                                    content address of a composed image: its
                                    configuration, its trees and the host
  silt why SYMBOL IMAGE.sx          why a symbol has the value it has
  silt solve IMAGE.sx --buildroot DIR
                                    ask the Kconfig model whether it can exist
  silt complete IMAGE.sx --buildroot DIR
                                    predict the full .config kbuild will produce [-o FILE]
        [--linux DIR]               and the kernel's, from the base Buildroot picks
  silt fixpoint IMAGE.sx --buildroot DIR --config .config [--config TREE=PATH]
                                    diff kbuild's .config against the image (absent = n)
  silt import DEFCONFIG --buildroot DIR [--kbuild] [--name NAME] [-n] [-f]
                                    split a real defconfig into target, profile, image;
                                    --kbuild also proves it round-trips via savedefconfig

Fragments are loaded from ./fragments by default; override with -L DIR.
`

// externalTrees are the br2-external trees to import alongside Buildroot,
// from --external, which every command accepts. A global because it is a
// property of the tree being imported rather than of any one command: an
// image that states a symbol from an external tree needs it wherever the
// tree is loaded.
var externalTrees []string

// packs are the packs given with --pack: fragments plus the br2-external tree
// that implements them, maintained elsewhere.
var packs []*pack.Pack

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	args := os.Args[2:]
	for {
		rest, dir := takeFlag(args, "--external")
		if dir == "" {
			break
		}
		externalTrees = append(externalTrees, dir)
		args = rest
	}
	for {
		rest, dir := takeFlag(args, "--pack")
		if dir == "" {
			break
		}
		p, err := pack.Load(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		packs = append(packs, p)
		if p.External != "" {
			// A pack's fragments and its br2-external tree are two halves of
			// one thing: the fragment states the symbol, the tree is what
			// makes the symbol exist. Loading one without the other is the
			// mistake --pack exists to prevent.
			externalTrees = append(externalTrees, p.External)
		}
		args = rest
	}
	var err error
	switch os.Args[1] {
	case "check":
		err = cmdCheck(args)
	case "import":
		err = cmdImport(args)
	case "fixpoint":
		err = cmdFixpoint(args)
	case "emit":
		err = cmdEmit(args)
	case "fmt":
		err = cmdFmt(args)
	case "hash":
		err = cmdHash(args)
	case "why":
		err = cmdWhy(args)
	case "solve":
		err = cmdSolve(args)
	case "complete":
		err = cmdComplete(args)
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
	// A pack's fragments are part of every library load, so an image can
	// compose them without the pack being copied into fragments/.
	for _, p := range packs {
		out = append(out, p.Files...)
	}
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
	var brDir, lxDir string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--buildroot", "--linux":
			flag := args[i]
			i++
			if i >= len(args) {
				return fmt.Errorf("%s needs a directory", flag)
			}
			if flag == "--buildroot" {
				brDir = args[i]
			} else {
				lxDir = args[i]
			}
			continue
		}
		rest = append(rest, args[i])
	}
	args = rest

	var tree *kconfig.Tree
	var treeVer string
	if brDir != "" {
		var err error
		treeVer, err = kconfig.TreeVersion(brDir)
		if err != nil {
			return fmt.Errorf("%s does not look like a Buildroot tree: %w", brDir, err)
		}
		tree, err = loadTree(brDir)
		if err != nil {
			return err
		}
	}

	files, err := loadAll(args)
	if err != nil {
		return err
	}
	lib := compose.NewLibrary()
	lib.Tree = tree
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
	bad := 0

	// The packs' own declarations first: an image composing a pack that does
	// not offer what it says it does is a problem with the pack, and saying
	// so at the image is how a consumer ends up reading someone else's
	// fragments to find out what went wrong.
	versions := map[string]string{}
	if treeVer != "" {
		versions[string(lang.Buildroot)] = treeVer
	}
	for _, p := range packs {
		for _, prob := range p.Check(versions) {
			fmt.Printf("%v\n", prob)
			bad++
		}
		if len(p.Check(versions)) == 0 {
			fmt.Printf("ok  pack %-13s %s, %d fragment(s)%s\n", p.Decl.Name, p.Decl.Version,
				len(p.Decl.Provides), externalNote(p))
		}
	}

	for _, im := range images {
		res, err := lib.Compose(im)
		if err != nil {
			return fmt.Errorf("image %s: %w", im.Name, err)
		}
		if tree == nil {
			continue
		}
		// A pin that disagrees with the tree is reported before anything else:
		// every finding below is only meaningful for the release it was
		// checked against.
		if want, ok := im.VerifiedAgainst["buildroot"]; ok && want != treeVer {
			fmt.Printf("%s: pinned to buildroot %s, checking against %s\n",
				im.Name, want, treeVer)
			bad++
		}
		rep := verify.Check(res, tree, lib.Trees[string(lang.Buildroot)])
		verify.CheckCapabilities(lib.Declared, tree, rep)
		verify.CheckTrees(lib.Trees, tree, rep)
		verify.CheckRules(lib.Rules, tree, lib.Trees[string(lang.Buildroot)], rep)
		against := []string{fmt.Sprintf("%d buildroot", rep.Checked)}
		versions := []string{"buildroot " + treeVer}

		// Every other tree the library declares and the caller supplied. A
		// tree nobody loaded is not checked, and the count below says so.
		for _, sc := range otherScopes(res) {
			t2, ver, err := treeFor(sc, lib.Trees, lxDir)
			if err != nil {
				return err
			}
			if t2 == nil {
				fmt.Printf("    %-18s %d %s symbols not checked: no %s tree given\n",
					im.Name, countScope(res, sc), sc, sc)
				continue
			}
			if want, ok := im.VerifiedAgainst[string(sc)]; ok && want != ver {
				fmt.Printf("%s: pinned to %s %s, checking against %s\n", im.Name, sc, want, ver)
				bad++
			}
			r2 := verify.Check(res, t2, lib.Trees[string(sc)])
			verify.CheckRules(lib.Rules, t2, lib.Trees[string(sc)], r2)
			rep.Findings = append(rep.Findings, r2.Findings...)
			against = append(against, fmt.Sprintf("%d %s", r2.Checked, sc))
			versions = append(versions, string(sc)+" "+ver)
		}

		if rep.OK() {
			fmt.Printf("ok  %-18s %s symbols checked against %s\n",
				im.Name, strings.Join(against, ", "), strings.Join(versions, ", "))
			continue
		}
		bad++
		fmt.Printf("\n%s — %d problem(s), %s\n", im.Name, len(rep.Findings), strings.Join(versions, ", "))
		for _, f := range rep.Findings {
			fmt.Printf("  %s\n", f)
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d image(s) do not match the tree", bad)
	}
	fmt.Printf("ok  %d files, %d fragments, %d rule blocks, %d images\n",
		len(files), nf, nr, len(images))
	return nil
}

// otherScopes lists the trees an image configures beyond Buildroot, sorted.
func externalNote(p *pack.Pack) string {
	if p.External == "" {
		return ""
	}
	return ", br2-external"
}

func otherScopes(res *compose.Result) []lang.Scope {
	var out []lang.Scope
	for sc, cs := range res.Constraints {
		if sc != lang.Buildroot && len(cs) > 0 {
			out = append(out, sc)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func countScope(res *compose.Result, sc lang.Scope) int {
	n := 0
	for _, c := range res.Constraints[sc] {
		if !c.Soft {
			n++
		}
	}
	return n
}

// treeFor imports a declared tree from its checkout. The path comes from the
// command line if given, else from the registry's (source ...); the
// environment comes from the registry's (env ...), since the kernel cannot be
// read at all without ARCH.
func treeFor(sc lang.Scope, decls map[string]lang.TreeDecl, override string) (*kconfig.Tree, string, error) {
	if sc != lang.Linux {
		return nil, "", nil // only the kernel has an importer entry point so far
	}
	decl := decls[string(sc)]
	dir := override
	if dir == "" {
		dir = decl.Source
	}
	if dir == "" {
		return nil, "", nil
	}
	arch := "arm64"
	if a, ok := decl.Env["ARCH"]; ok {
		arch = a
	}
	env, err := kconfig.LinuxEnv(dir, arch)
	if err != nil {
		return nil, "", fmt.Errorf("%s does not look like a Linux tree: %w", dir, err)
	}
	for k, v := range decl.Env {
		env[k] = v
	}
	tree, err := kconfig.Load("Kconfig", kconfig.Options{Root: dir, Env: env})
	if err != nil {
		return nil, "", fmt.Errorf("importing %s: %w", dir, err)
	}
	return tree, env["KERNELVERSION"], nil
}

func cmdEmit(args []string) error {
	args, lxDir := takeFlag(args, "--linux")
	var target, outDir, libDir, brDir string
	outDir = "out"
	libDir = "fragments"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--buildroot":
			i++
			if i >= len(args) {
				return fmt.Errorf("--buildroot needs a directory")
			}
			brDir = args[i]
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
	if brDir != "" {
		tree, err := loadTree(brDir)
		if err != nil {
			return err
		}
		lib.Tree = tree
	}
	if brDir != "" {
		tree, err := loadTree(brDir)
		if err != nil {
			return err
		}
		lib.Tree = tree
	}
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
	// One file per tree the image configures, each wired into the defconfig
	// through its tree's consumed-by symbol.
	paths := map[string]string{}
	var written []string
	for _, tree := range emit.Trees(r) {
		path := filepath.Join(outDir, emit.FileName(tree))
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		if err := os.WriteFile(path, []byte(emit.Defconfig(r, lang.Scope(tree))), 0o644); err != nil {
			return err
		}
		paths[tree] = abs
		written = append(written, path)
	}
	br := filepath.Join(outDir, emit.FileName("buildroot"))
	versions, env, err := solutionInputs(brDir, lxDir, lib.Trees, r)
	if err != nil {
		return err
	}
	hash := emit.SolutionHash(r, versions, env)
	def, err := emit.BuildrootDefconfigWithSolution(r, paths, hash)
	if err != nil {
		return err
	}
	if err := os.WriteFile(br, []byte(def), 0o644); err != nil {
		return err
	}

	report(r, append([]string{br}, written...), hash)
	return nil
}

// report prints the managed / opaque / unmanaged split. Invariant 9: a loss
// that does not appear in the output is a bug, whatever else is true about it.
func report(r *compose.Result, files []string, hash string) {
	br := files[0]
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
	var scopes []string
	for sc := range r.Constraints {
		scopes = append(scopes, string(sc))
	}
	sort.Strings(scopes)
	var parts []string
	softs := 0
	for _, sc := range scopes {
		hard, soft := split(r.Constraints[lang.Scope(sc)])
		parts = append(parts, fmt.Sprintf("%d %s", hard, sc))
		softs += soft
	}
	fmt.Printf("stated        %s\n", strings.Join(parts, ", "))
	if softs > 0 {
		fmt.Printf("soft          %d  not emitted; needs MaxSAT (Rung 7)\n", softs)
	}
	fmt.Printf("guards        %d\n", len(r.Guards))
	if n := len(r.Derived); n > 0 {
		fmt.Printf("derived       %d  by cross-tree rules:\n", n)
		for _, d := range r.Derived {
			fmt.Printf("                %s=%s  %s\n",
				d.Constraint.Sym, d.Constraint.Want, d.Rule.Pos.Short())
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
	fmt.Printf("\nwrote %s\n", strings.Join(files, "\n      "))
	if hash != "" {
		fmt.Printf("solution %s  (recorded in the defconfig; silt hash --solution prints it)\n", hash)
	}
	fmt.Printf("\nThis defconfig states intent; kbuild completes it. To see the full .config\n")
	fmt.Printf("it will become, and any stated line kbuild would drop, without running make:\n")
	fmt.Printf("  silt complete %s --buildroot DIR -o predicted.config\n", r.Image.Pos.File)
	abs, err := filepath.Abs(br)
	if err != nil {
		abs = br
	}
	fmt.Printf("To build:\n  make defconfig BR2_DEFCONFIG=%s\n", abs)
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
	if len(args) > 0 && args[0] == "--solution" {
		return cmdSolutionHash(args[1:])
	}
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

// takeFlag removes a flag and its value from an argument list.
func takeFlag(args []string, flag string) ([]string, string) {
	var out []string
	value := ""
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			value = args[i+1]
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out, value
}

// cmdSolutionHash prints the content address of a composed image: the
// configuration, the release of every tree it was checked against, and the
// host values those trees depend on. An emitted defconfig carries the same
// hash in its header, so a build can be told apart from a stale one.
func cmdSolutionHash(args []string) error {
	args, lxDir := takeFlag(args, "--linux")
	res, _, name, err := composeWithTree(args)
	if err != nil {
		return err
	}
	brDir := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--buildroot" {
			brDir = args[i+1]
		}
	}
	versions, env, err := solutionInputs(brDir, lxDir, res.Trees, res)
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s\n", emit.SolutionHash(res, versions, env), name)
	for _, line := range strings.Split(strings.TrimSpace(emit.Solution(res, versions, env)), "\n") {
		if strings.HasPrefix(line, "tree ") || strings.HasPrefix(line, "env ") ||
			strings.HasPrefix(line, "fragment ") {
			fmt.Printf("  %s\n", line)
		}
	}
	return nil
}

// completeLinux predicts the kernel's .config and reports which stated
// CONFIG_ claims survive it.
func completeLinux(res *compose.Result, brCfg map[string]string, brDir, lxDir, base, name string) error {
	arch := "arm64"
	if d, ok := res.Trees[string(lang.Linux)]; ok {
		if a, ok := d.Env["ARCH"]; ok {
			arch = a
		}
	}
	env, err := kconfig.LinuxEnv(lxDir, arch)
	if err != nil {
		return fmt.Errorf("%s does not look like a Linux tree: %w", lxDir, err)
	}
	menu, err := kconfig.LoadMenu("Kconfig", kconfig.Options{
		Root: lxDir, Env: env, Prefix: "CONFIG_"})
	if err != nil {
		return err
	}
	basePath, why, err := kernelBase(brCfg, brDir, lxDir, arch, base)
	if err != nil {
		return err
	}
	var assign []kconfig.Assignment
	if basePath != "" {
		f, err := os.Open(basePath)
		if os.IsNotExist(err) {
			// Usually the right answer to this is that the image builds a
			// vendor kernel: BR2_LINUX_KERNEL_DEFCONFIG="bcm2711" exists in
			// the Raspberry Pi fork and nowhere in mainline.
			return fmt.Errorf("%s: the kernel base %s chosen by %s is not in %s.\n"+
				"  If this image builds a vendor kernel, point --linux at that tree, "+
				"or pass --linux-defconfig PATH", name, filepath.Base(basePath), why, lxDir)
		}
		if err != nil {
			return err
		}
		assign, err = kconfig.ReadAssignments(f)
		f.Close()
		if err != nil {
			return err
		}
	}
	// Silt's fragment is layered over it, exactly as Buildroot layers the
	// file it passes in BR2_LINUX_KERNEL_CONFIG_FRAGMENT_FILES.
	ours, err := kconfig.ReadAssignments(strings.NewReader(emit.Defconfig(res, lang.Linux)))
	if err != nil {
		return err
	}
	ev := menu.Evaluate(append(assign, ours...))
	cfg := ev.Config()
	on := 0
	for _, v := range cfg {
		if v == "y" || v == "m" {
			on++
		}
	}
	fmt.Printf("linux      %d symbols written, %d on, from %s (%s), linux %s\n",
		len(cfg), on, filepath.Base(basePath), why, env["KERNELVERSION"])

	tainted := map[string]bool{}
	for _, n := range ev.Tainted() {
		tainted[n] = true
	}
	ms := fixpoint.Check(res, lang.Linux, fixpoint.Config(cfg))
	stated := countScope(res, lang.Linux)
	if len(ms) == 0 {
		fmt.Printf("           all %d stated CONFIG_ symbols honoured\n", stated)
	} else {
		fmt.Printf("           %d of %d stated CONFIG_ symbols would be dropped:\n", len(ms), stated)
		for _, m := range ms {
			fmt.Printf("  %v\n", m)
		}
	}
	// Symbols whose own conditions are compiler probes are read as no here,
	// because only a compiler can answer them. Saying how many is the
	// difference between a prediction and a guess.
	fmt.Printf("           %d symbols decided by compiler probes ($(cc-option,...)), read as n\n",
		len(tainted))
	if len(ms) > 0 {
		return fmt.Errorf("%d stated kernel symbol(s) would not survive kbuild", len(ms))
	}
	return nil
}

// kernelBase is the configuration the kernel starts from, which Buildroot
// chooses: the architecture's default, a named defconfig, or a file.
func kernelBase(brCfg map[string]string, brDir, lxDir, arch, override string) (string, string, error) {
	if override != "" {
		return override, "--linux-defconfig", nil
	}
	unquote := func(s string) string { return strings.Trim(s, `"`) }
	switch {
	case brCfg["BR2_LINUX_KERNEL_USE_ARCH_DEFAULT_CONFIG"] == "y":
		return filepath.Join(lxDir, "arch", arch, "configs", "defconfig"),
			"BR2_LINUX_KERNEL_USE_ARCH_DEFAULT_CONFIG", nil
	case brCfg["BR2_LINUX_KERNEL_DEFCONFIG"] != "":
		name := unquote(brCfg["BR2_LINUX_KERNEL_DEFCONFIG"])
		return filepath.Join(lxDir, "arch", arch, "configs", name+"_defconfig"),
			"BR2_LINUX_KERNEL_DEFCONFIG=" + name, nil
	case brCfg["BR2_LINUX_KERNEL_CUSTOM_CONFIG_FILE"] != "":
		return filepath.Join(brDir, unquote(brCfg["BR2_LINUX_KERNEL_CUSTOM_CONFIG_FILE"])),
			"BR2_LINUX_KERNEL_CUSTOM_CONFIG_FILE", nil
	}
	return filepath.Join(lxDir, "arch", arch, "configs", "defconfig"),
		"no kernel base stated; the architecture default", nil
}

// solutionInputs collects the tree versions and host values a solution
// depends on. A tree the caller did not supply is recorded as unknown rather
// than left out: two runs against different kernels must not hash the same.
func solutionInputs(brDir, lxDir string, decls map[string]lang.TreeDecl, r *compose.Result) (map[string]string, map[string]string, error) {
	versions := map[string]string{}
	env := map[string]string{}
	if brDir != "" {
		v, err := kconfig.TreeVersion(brDir)
		if err != nil {
			return nil, nil, err
		}
		versions[string(lang.Buildroot)] = v
		if e, err := buildrootEnv(brDir); err == nil {
			env = e
		}
	}
	for _, sc := range otherScopes(r) {
		versions[string(sc)] = "unknown"
		dir := lxDir
		if d, ok := decls[string(sc)]; ok && d.Source != "" && dir == "" {
			dir = d.Source
		}
		if sc == lang.Linux && dir != "" {
			if v, err := kconfig.KernelVersion(dir); err == nil {
				versions[string(sc)] = v
			}
		}
	}
	return versions, env, nil
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

	id, err := resolveSymbol(r, sym)
	if err != nil {
		return err
	}
	if s, ok := r.ExplainDerived(id); ok {
		fmt.Print(s)
		return nil
	}
	{
		for _, c := range r.Constraints[lang.Scope(id.Tree)] {
			if c.Sym != id {
				continue
			}
			kind := "stated"
			if c.Soft {
				kind = "preferred (soft; not emitted before Rung 7)"
			}
			fmt.Printf("%s = %s\n  %s by %s at %s\n",
				id, valueOf(c), kind, c.From, c.Pos.Short())
			return nil
		}
	}
	fmt.Printf("%s is not stated by any composed fragment and no rule derives it.\n", sym)
	fmt.Printf("Its value would be decided by Kconfig defaults, which needs the\n")
	fmt.Printf("importer and the solver (Rungs 4-7).\n")
	return nil
}

// resolveSymbol turns a command-line symbol into an identity. tree:NAME is
// exact. A bare name is accepted when exactly one tree in the composition
// mentions it, and refused when several do: linux:CONFIG_NET and
// busybox:CONFIG_NET are different questions.
func resolveSymbol(r *compose.Result, sym string) (lang.SymbolID, error) {
	if i := strings.IndexByte(sym, ':'); i >= 0 {
		return lang.SymbolID{Tree: sym[:i], Name: sym[i+1:]}, nil
	}
	found := map[string]bool{}
	for sc, cs := range r.Constraints {
		for _, c := range cs {
			if c.Sym.Name == sym {
				found[string(sc)] = true
			}
		}
	}
	for _, d := range r.Derived {
		if d.Constraint.Sym.Name == sym {
			found[d.Constraint.Sym.Tree] = true
		}
	}
	var trees []string
	for t := range found {
		trees = append(trees, t)
	}
	sort.Strings(trees)
	switch len(trees) {
	case 0:
		return lang.SymbolID{Tree: string(lang.Buildroot), Name: sym}, nil
	case 1:
		return lang.SymbolID{Tree: trees[0], Name: sym}, nil
	}
	return lang.SymbolID{}, fmt.Errorf("%s is stated in several trees (%s); qualify it, e.g. %s:%s",
		sym, strings.Join(trees, ", "), trees[0], sym)
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

// loadTree imports a Buildroot tree, with the environment supplied explicitly.
// The shape of the Kconfig tree depends on it (DESIGN.md §14.2), so reading it
// implicitly would make the import depend on invisible state.
// buildrootEnv is the environment a Buildroot import needs on this host, with
// any br2-external trees generated into a scratch directory first. The
// generated files are what Buildroot's own Config.in sources, so this is the
// only way an external tree's symbols exist at all.
func buildrootEnv(dir string) (map[string]string, error) {
	env, err := kconfig.BuildrootEnv(dir, filepath.Join(dir, "output"))
	if err != nil {
		return nil, err
	}
	base, err := os.MkdirTemp("", "silt-external-")
	if err != nil {
		return nil, err
	}
	add, err := kconfig.PrepareExternal(dir, base, externalTrees)
	if err != nil {
		return nil, err
	}
	for k, v := range add {
		env[k] = v
	}
	return env, nil
}

func loadTree(dir string) (*kconfig.Tree, error) {
	ver, err := kconfig.TreeVersion(dir)
	if err != nil {
		return nil, fmt.Errorf("%s does not look like a Buildroot tree: %w", dir, err)
	}
	_ = ver
	env, err := buildrootEnv(dir)
	if err != nil {
		return nil, err
	}
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: dir, Env: env})
	if err != nil {
		return nil, fmt.Errorf("importing %s: %w", dir, err)
	}
	return tree, nil
}

// cmdComplete predicts the .config kbuild will produce for an image.
//
// It loads the emitted defconfig into Buildroot's menu model and evaluates it
// the way support/kconfig does, so the prediction is the whole configuration,
// not the stated part. Every stated constraint the prediction does not honour
// is reported: those are the lines make defconfig would silently drop.
//
//	silt complete IMAGE.sx --buildroot DIR [-o FILE] [-L DIR]
func cmdComplete(args []string) error {
	args, lxDir := takeFlag(args, "--linux")
	args, lxBase := takeFlag(args, "--linux-defconfig")
	var outFile string
	var rest []string
	brDir := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o":
			i++
			if i >= len(args) {
				return fmt.Errorf("-o needs a file")
			}
			outFile = args[i]
			continue
		case "--buildroot":
			if i+1 < len(args) {
				brDir = args[i+1]
			}
		}
		rest = append(rest, args[i])
	}
	res, _, name, err := composeWithTree(rest)
	if err != nil {
		return err
	}
	env, err := buildrootEnv(brDir)
	if err != nil {
		return err
	}
	menu, err := kconfig.LoadMenu("Config.in", kconfig.Options{Root: brDir, Env: env})
	if err != nil {
		return err
	}

	// Exactly what emit writes, so the prediction is of emit's output.
	paths := map[string]string{}
	for _, t := range emit.Trees(res) {
		paths[t] = emit.FileName(t)
	}
	def, err := emit.BuildrootDefconfig(res, paths)
	if err != nil {
		return err
	}
	assign, err := kconfig.ReadAssignments(strings.NewReader(def))
	if err != nil {
		return err
	}
	cfg := menu.Evaluate(assign).Config()

	on := 0
	for _, v := range cfg {
		if v == "y" {
			on++
		}
	}
	fmt.Printf("%s\n", name)
	fmt.Printf("completed  %d symbols written, %d on (host gcc %s, %s)\n",
		len(cfg), on, env["HOST_GCC_VERSION"], env["HOSTARCH"])

	ms := fixpoint.Check(res, lang.Buildroot, fixpoint.Config(cfg))
	stated := 0
	for _, c := range res.Constraints[lang.Buildroot] {
		if !c.Soft {
			stated++
		}
	}
	if len(ms) == 0 {
		fmt.Printf("stated     all %d honoured\n", stated)
	} else {
		fmt.Printf("stated     %d of %d would be dropped by kbuild:\n", len(ms), stated)
		for _, m := range ms {
			fmt.Printf("  %v\n", m)
		}
		fmt.Printf("  run silt solve for why\n")
	}

	// The kernel's half. Which base configuration it starts from is a
	// Buildroot decision — arch default, a named defconfig, or a file — so
	// the prediction above chooses it, which is the cross-tree link this
	// project exists for.
	if lxDir != "" {
		if err := completeLinux(res, cfg, brDir, lxDir, lxBase, name); err != nil {
			return err
		}
	} else if len(res.Constraints[lang.Linux]) > 0 {
		fmt.Printf("linux      %d stated symbols not predicted: pass --linux DIR\n",
			countScope(res, lang.Linux))
	}

	if outFile != "" {
		var b strings.Builder
		for _, k := range kconfig.Names(cfg) {
			if cfg[k] == "n" {
				fmt.Fprintf(&b, "# %s is not set\n", k)
			} else {
				fmt.Fprintf(&b, "%s=%s\n", k, cfg[k])
			}
		}
		if err := os.WriteFile(outFile, []byte(b.String()), 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote      %s\n", outFile)
	}
	if len(ms) > 0 {
		return fmt.Errorf("%d stated symbol(s) would not survive kbuild", len(ms))
	}
	return nil
}

// cmdSolve asks the real Kconfig model whether a composition can exist.
//
// verify answers a narrower question conservatively; this one sees the whole
// formula and catches compositions that are over-constrained in combination
// rather than in any single pair.
func cmdSolve(args []string) error {
	res, tree, name, err := composeWithTree(args)
	if err != nil {
		return err
	}
	out := solve.Solve(res, tree)
	fmt.Print(out.Explain(name))
	if out.Status == solver.UNSAT {
		fmt.Print(solve.ExplainRepairs(
			solve.Repairs(out, out.Formula(), out.Owners(), res.Image.Policy)))
	}
	if out.Status != solver.SAT {
		return fmt.Errorf("%s cannot be realized", name)
	}
	return nil
}

func composeWithTree(args []string) (*compose.Result, *kconfig.Tree, string, error) {
	var target, brDir, libDir string
	libDir = "fragments"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--buildroot":
			i++
			if i >= len(args) {
				return nil, nil, "", fmt.Errorf("--buildroot needs a directory")
			}
			brDir = args[i]
		case "-L":
			i++
			if i >= len(args) {
				return nil, nil, "", fmt.Errorf("-L needs a directory")
			}
			libDir = args[i]
		default:
			target = args[i]
		}
	}
	if target == "" || brDir == "" {
		return nil, nil, "", fmt.Errorf("usage: IMAGE.sx --buildroot DIR")
	}
	tree, err := loadTree(brDir)
	if err != nil {
		return nil, nil, "", err
	}
	files, err := loadAll([]string{libDir})
	if err != nil {
		return nil, nil, "", err
	}
	lib := compose.NewLibrary()
	lib.Tree = tree
	for _, f := range files {
		if err := lib.Add(f); err != nil {
			return nil, nil, "", err
		}
	}
	src, err := os.ReadFile(target)
	if err != nil {
		return nil, nil, "", err
	}
	imf, err := lang.ParseFile(string(src), target)
	if err != nil {
		return nil, nil, "", err
	}
	if len(imf.Images) != 1 {
		return nil, nil, "", fmt.Errorf("%s: expected exactly one image", target)
	}
	res, err := lib.Compose(imf.Images[0])
	if err != nil {
		return nil, nil, "", err
	}
	return res, tree, imf.Images[0].Name, nil
}
