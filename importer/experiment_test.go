package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vinodhalaharvi/silt/cnf"
	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/kconfig"
	"github.com/vinodhalaharvi/silt/lang"
	"github.com/vinodhalaharvi/silt/solve"
	"github.com/vinodhalaharvi/silt/solver"
)

// These tests run Buildroot's own kconfig, so they need a checkout, a host C
// compiler and make. They are the evidence for the claims import makes; unit
// tests cannot be, since the thing being matched is kbuild's behaviour.
func kbuildEnv(t *testing.T) (string, *kconfig.Tree, string) {
	t.Helper()
	root := os.Getenv("SILT_BUILDROOT")
	if root == "" || os.Getenv("SILT_KBUILD") == "" {
		t.Skip("set SILT_BUILDROOT and SILT_KBUILD=1 to run kbuild experiments")
	}
	ver, err := kconfig.TreeVersion(root)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := kconfig.Load("Config.in", kconfig.Options{Root: root, Env: map[string]string{
		"BR2_BASE_DIR": filepath.Join(root, "output"), "HOSTARCH": "x86_64",
		"HOST_GCC_VERSION": "13 2", "BR2_VERSION_FULL": ver,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return root, tree, ver
}

func readDefconfig(t *testing.T, path string) []Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	es, err := ParseDefconfig(f, path)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// importText imports a defconfig and returns the rendered target, profile and
// image, so the tests exercise exactly what silt import writes.
func importText(tree *kconfig.Tree, ver, path string, es []Entry) (*Imported, [3]string, error) {
	im, err := Import(NameFor(path), path, ver, es, tree)
	if err != nil {
		return nil, [3]string{}, err
	}
	return im, [3]string{im.Fragment(Target), im.Fragment(Profile), im.Image()}, nil
}

// TestEveryDefconfigRoundTrips imports every defconfig Buildroot ships, emits
// it back, and requires the .config kbuild makes of the result to be identical
// to the .config it makes of the source. It also requires the model to accept each
// one, since kbuild does.
func TestEveryDefconfigRoundTrips(t *testing.T) {
	root, tree, ver := kbuildEnv(t)
	paths, _ := filepath.Glob(filepath.Join(root, "configs", "*_defconfig"))
	if only := os.Getenv("SILT_DEFCONFIGS"); only != "" {
		paths = nil
		for _, n := range strings.Fields(only) {
			paths = append(paths, filepath.Join(root, "configs", n+"_defconfig"))
		}
	}

	type outcome struct {
		name, problem string
	}
	jobs := make(chan string)
	results := make(chan outcome)
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers > 4 {
		workers = 4
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := t.TempDir()
			if dir := os.Getenv("SILT_KBUILD_OUT"); dir != "" && workers == 1 {
				// A reused O= directory keeps kconfig's conf built between
				// runs, which is most of the cost of a short run.
				out = dir
			}
			kb := &Kbuild{Root: root, Out: out}
			for path := range jobs {
				name := filepath.Base(path)
				problem := func() string {
					// Full .config equality, rather than savedefconfig of
					// each side: two make runs instead of four, and strictly
					// stronger, since equal .configs have equal
					// savedefconfigs but not the other way round.
					if err := kb.Defconfig(path); err != nil {
						return "kbuild on source: " + err.Error()
					}
					want, err := os.ReadFile(filepath.Join(kb.Out, ".config"))
					if err != nil {
						return err.Error()
					}
					es, err := ParseDefconfig(mustOpen(path), path)
					if err != nil {
						return err.Error()
					}
					_, txt, err := importText(tree, ver, path, es)
					if err != nil {
						return "import: " + err.Error()
					}
					res, err := Compose(tree, txt[2], txt[0], txt[1])
					if err != nil {
						return "compose: " + err.Error()
					}
					emitted, err := Emit(res, kb.Out)
					if err != nil {
						return err.Error()
					}
					if err := kb.Defconfig(emitted); err != nil {
						return "kbuild on emitted: " + err.Error()
					}
					got, err := os.ReadFile(filepath.Join(kb.Out, ".config"))
					if err != nil {
						return err.Error()
					}
					if configBody(got) != configBody(want) {
						return "lossy:\n" + diffLines(string(want), string(got))
					}
					return ""
				}()
				results <- outcome{name, problem}
			}
		}()
	}
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	var bad []outcome
	n := 0
	for r := range results {
		n++
		status := "ok"
		if r.problem != "" {
			bad = append(bad, r)
			status = "FAIL"
		}
		t.Logf("%3d/%d %-4s %s", n, len(paths), status, r.name)
	}
	sort.Slice(bad, func(i, j int) bool { return bad[i].name < bad[j].name })
	t.Logf("%d defconfigs, %d round-trip identically", n, n-len(bad))
	for _, b := range bad {
		t.Errorf("%s: %s", b.name, b.problem)
	}
}

// TestModelAcceptsEveryDefconfig asks the model about every shipped defconfig.
// kbuild accepts all of them, so an UNSAT is a Silt bug.
//
// It also measures the rule "omit a line if the model forces it given the
// others". The model cannot tell a select from a depends: in `A depends on B`,
// asserting A forces B, but kbuild will never turn B on for A. So every line
// the rule would drop is a line savedefconfig kept, and dropping it would
// lose it. The count is logged as the evidence for doing minimisation with
// kbuild rather than the model.
func TestModelAcceptsEveryDefconfig(t *testing.T) {
	root, tree, _ := kbuildEnv(t)
	paths, _ := filepath.Glob(filepath.Join(root, "configs", "*_defconfig"))
	m := cnf.Lower(tree)
	s := solver.New(m.F)

	forcedLines, lines, unsat := 0, 0, 0
	example := ""
	for _, path := range paths {
		es := readDefconfig(t, path)
		var as []cnf.Lit
		var syms []string
		for _, e := range es {
			sym := tree.Symbols[e.Symbol]
			if sym == nil || !sym.Type.Solvable() {
				continue
			}
			l := m.F.Var(e.Symbol)
			if e.Unset {
				l = l.Neg()
			}
			as = append(as, l)
			syms = append(syms, e.Symbol)
		}
		if r := s.Solve(as...); r.Status != solver.SAT {
			unsat++
			var core []string
			for _, c := range r.Core {
				core = append(core, m.F.Name(c.Var()))
			}
			t.Errorf("%s: model rejects a shipped defconfig; core %v", filepath.Base(path), core)
			continue
		}
		for i := range as {
			lines++
			trial := append(append([]cnf.Lit(nil), as[:i]...), as[i+1:]...)
			trial = append(trial, as[i].Neg())
			if s.Solve(trial...).Status == solver.UNSAT {
				forcedLines++
				if example == "" {
					example = fmt.Sprintf("%s in %s", syms[i], filepath.Base(path))
				}
			}
		}
	}
	t.Logf("%d defconfigs, %d rejected by the model", len(paths), unsat)
	t.Logf("%d of %d bool lines are forced by the others in the model, e.g. %s; "+
		"savedefconfig kept every one", forcedLines, lines, example)
}

// The five boards the factoring experiment uses: four aarch64 SBCs from three
// vendors and the QEMU virt machine.
var factorBoards = []string{"qemu_aarch64_virt", "odroidc2", "raspberrypi4_64", "orangepi_pc2", "pine64"}

// TestFactoring is the experiment from the README. Each board is imported and
// split into a target and a profile. Every target is then composed with every
// profile, 25 images, and each is run through kbuild.
//
// The diagonal must round-trip byte-identically after savedefconfig. Off the
// diagonal is the actual test of the combinatorics argument: a profile is only
// reusable if kbuild keeps every line of it on a target it was not written for.
// Each cell also records the model's verdict, so disagreements between Silt
// and kbuild are visible rather than averaged away.
func TestFactoring(t *testing.T) {
	root, tree, ver := kbuildEnv(t)
	kb := &Kbuild{Root: root, Out: t.TempDir()}

	type board struct {
		name    string
		source  string // savedefconfig of the original
		im      *Imported
		target  string
		profile string
	}
	var boards []board
	for _, n := range factorBoards {
		path := filepath.Join(root, "configs", n+"_defconfig")
		src, err := kb.Normalize(path)
		if err != nil {
			t.Fatal(err)
		}
		es, err := ParseDefconfig(strings.NewReader(src), path)
		if err != nil {
			t.Fatal(err)
		}
		im, txt, err := importText(tree, ver, path, es)
		if err != nil {
			t.Fatal(err)
		}
		boards = append(boards, board{n, src, im, txt[0], txt[1]})
	}

	var report strings.Builder
	fmt.Fprintf(&report, "\n%-20s", "target \\ profile")
	for _, b := range boards {
		fmt.Fprintf(&report, " %-18s", b.im.Name)
	}
	report.WriteString("\n")
	var details []string
	kept, cells := 0, 0

	for _, tg := range boards {
		fmt.Fprintf(&report, "%-20s", tg.im.Name)
		for _, pr := range boards {
			img := fmt.Sprintf("(image x (compose target:%s profile:%s))", tg.im.Name, pr.im.Name)
			res, err := Compose(tree, img, tg.target, pr.profile)
			if err != nil {
				t.Fatalf("%s + %s: %v", tg.name, pr.name, err)
			}
			verdict := "SAT"
			if solve.Solve(res, tree).Status != solver.SAT {
				verdict = "UNSAT"
			}
			emitted, err := Emit(res, kb.Out)
			if err != nil {
				t.Fatal(err)
			}
			if err := kb.Defconfig(emitted); err != nil {
				t.Fatal(err)
			}
			cfg, err := kb.Config()
			if err != nil {
				t.Fatal(err)
			}
			var lost []string
			// Only the half each board contributed: tg's target lines and
			// pr's profile lines. Checking every line of both boards reported
			// the other board's hardware as "lost", which it never stated.
			for _, e := range tg.im.Entries {
				if e.Class == Target && !Holds(e.Entry, cfg) {
					lost = append(lost, e.Symbol)
				}
			}
			for _, e := range pr.im.Entries {
				if e.Class == Profile && !Holds(e.Entry, cfg) {
					lost = append(lost, e.Symbol)
				}
			}
			cells++
			cell := fmt.Sprintf("ok/%s", verdict)
			if len(lost) > 0 {
				cell = fmt.Sprintf("%d lost/%s", len(lost), verdict)
				details = append(details, fmt.Sprintf("%s + %s (model %s): %s",
					tg.im.Name, pr.im.Name, verdict, strings.Join(lost, ", ")))
			} else {
				kept++
			}
			if (verdict == "UNSAT") != (len(lost) > 0) && verdict == "UNSAT" {
				t.Errorf("%s + %s: model says UNSAT but kbuild kept every line", tg.name, pr.name)
			}
			fmt.Fprintf(&report, " %-18s", cell)

			if tg.name == pr.name {
				saved, err := kb.Savedefconfig()
				if err != nil {
					t.Fatal(err)
				}
				if saved != tg.source {
					t.Errorf("%s does not round-trip:\n%s", tg.name, diffLines(tg.source, saved))
				}
				if len(lost) > 0 {
					t.Errorf("%s loses lines on its own profile: %v", tg.name, lost)
				}
			}
		}
		report.WriteString("\n")
	}
	t.Logf("%s\n%d of %d compositions keep every stated line", report.String(), kept, cells)
	for _, d := range details {
		t.Logf("  %s", d)
	}
}

func diffLines(want, got string) string {
	set := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			out[l] = true
		}
		return out
	}
	w, g := set(want), set(got)
	var b strings.Builder
	for _, l := range strings.Split(want, "\n") {
		if l != "" && !g[l] {
			b.WriteString("  - " + l + "\n")
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if l != "" && !w[l] {
			b.WriteString("  + " + l + "\n")
		}
	}
	if b.Len() == 0 {
		b.WriteString("  (same lines, different order)\n")
	}
	return b.String()
}

// TestImportedTargetsTakeLibraryProfiles composes each imported target with
// the profiles written by hand for the library. The imported profiles are
// tiny, because vendor defconfigs carry almost no userspace policy, so the
// 5x5 matrix above tests little. The library's profiles choose a libc, an
// init system and static linking, which is where a target written for one
// userspace can quietly refuse another.
func TestImportedTargetsTakeLibraryProfiles(t *testing.T) {
	root, tree, ver := kbuildEnv(t)
	kb := &Kbuild{Root: root, Out: t.TempDir()}

	libFiles, _ := filepath.Glob("../fragments/*/*.sx")
	caps, _ := filepath.Glob("../fragments/*.sx")
	libFiles = append(libFiles, caps...)
	var library []string
	profiles := map[string][]lang.Constraint{}
	for _, p := range libFiles {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		library = append(library, string(data))
		f, err := lang.ParseFile(string(data), p)
		if err != nil {
			t.Fatal(err)
		}
		for _, fr := range f.Fragments {
			if fr.ID.Kind == lang.Profile {
				profiles[fr.ID.Name] = fr.Constraints[lang.Buildroot]
			}
		}
	}
	// A control that must fail both ways: libcamera cannot coexist with
	// static libs. If this cell ever reads "ok", the lost-line check is
	// broken, and every "ok" beside it means nothing.
	const control = `(fragment profile:control-unsat (requires (capability mmu))
	  (buildroot (y BR2_STATIC_LIBS) (y BR2_PACKAGE_LIBCAMERA)))`
	library = append(library, control)
	profiles["control-unsat"] = nil

	decls := func() map[string]lang.CapabilityDecl {
		lib := compose.NewLibrary()
		for i, src := range library {
			f, _ := lang.ParseFile(src, fmt.Sprint(i))
			lib.Add(f)
		}
		return lib.Declared
	}()

	var names []string
	for n := range profiles {
		names = append(names, n)
	}
	sort.Strings(names)

	var report strings.Builder
	fmt.Fprintf(&report, "\n%-20s", "target \\ profile")
	for _, n := range names {
		fmt.Fprintf(&report, " %-34s", n)
	}
	report.WriteString("\n")
	var details []string
	checked := 0
	for _, board := range factorBoards {
		path := filepath.Join(root, "configs", board+"_defconfig")
		src, err := kb.Normalize(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := kb.Config()
		if err != nil {
			t.Fatal(err)
		}
		es, _ := ParseDefconfig(strings.NewReader(src), path)
		// Prefixed, because the library already has a hand-written
		// target:qemu-aarch64-virt and fragment names are global.
		im, err := Import("br-"+NameFor(path), path, ver, es, tree)
		if err != nil {
			t.Fatal(err)
		}
		im.Provides = DeriveProvides(cfg, decls)
		target := im.Fragment(Target)

		fmt.Fprintf(&report, "%-20s", im.Name)
		for _, pn := range names {
			img := fmt.Sprintf("(image x (compose target:%s profile:%s))", im.Name, pn)
			res, err := Compose(tree, img, append([]string{target}, library...)...)
			if err != nil {
				fmt.Fprintf(&report, " %-34s", "compose error")
				details = append(details, fmt.Sprintf("%s + %s: %v", im.Name, pn, err))
				continue
			}
			sv := solve.Solve(res, tree)
			verdict := sv.Status.String()
			emitted, err := Emit(res, kb.Out)
			if err != nil {
				t.Fatal(err)
			}
			if err := kb.Defconfig(emitted); err != nil {
				t.Fatal(err)
			}
			out, err := kb.Config()
			if err != nil {
				t.Fatal(err)
			}
			var lost []string
			for _, e := range im.Entries {
				if e.Class == Target && !Holds(e.Entry, out) {
					lost = append(lost, e.Symbol)
				}
			}
			for _, c := range res.Constraints[lang.Buildroot] {
				if c.Soft {
					continue
				}
				if !holdsConstraint(c, out) {
					lost = append(lost, c.Sym.String())
				}
			}
			lost = dedupe(lost)
			checked += len(res.Constraints[lang.Buildroot])
			cell := "ok, model " + verdict
			if len(lost) > 0 {
				cell = fmt.Sprintf("%d lost, model %s", len(lost), verdict)
				var core []string
				for _, a := range sv.Culprits {
					core = append(core, a.Symbol)
				}
				details = append(details, fmt.Sprintf("%s + %s: kbuild dropped %s; model %s %v",
					im.Name, pn, strings.Join(lost, " "), verdict, core))
			}
			if pn == "control-unsat" && (verdict != "UNSAT" || len(lost) == 0) {
				t.Errorf("%s + control: expected UNSAT and a dropped line, got %s and %d lost",
					im.Name, verdict, len(lost))
			}
			if verdict == "UNSAT" && len(lost) == 0 {
				t.Errorf("%s + %s: model says UNSAT but kbuild kept every line", im.Name, pn)
			}
			fmt.Fprintf(&report, " %-34s", cell)
		}
		report.WriteString("\n")
	}
	t.Logf("%s\n%d stated buildroot lines checked against kbuild's .config", report.String(), checked)
	for _, d := range details {
		t.Log("  " + d)
	}
}

func holdsConstraint(c lang.Constraint, cfg map[string]string) bool {
	if c.IsValue {
		got, ok := cfg[c.Sym.Name]
		return ok && strings.Trim(got, `"`) == c.Value
	}
	return Holds(Entry{Symbol: c.Sym.Name, Raw: c.Want.String(), Unset: c.Want == lang.N}, cfg)
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func mustOpen(path string) *os.File {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	return f
}

// configBody drops the lines of a .config that describe how it was made
// rather than what it configures: BR2_DEFCONFIG records the path it was
// loaded from, and the header carries a build timestamp.
func configBody(data []byte) string {
	var b strings.Builder
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "BR2_DEFCONFIG=") || strings.HasPrefix(l, "# Buildroot ") {
			continue
		}
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
