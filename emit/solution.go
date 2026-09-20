package emit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/component"
	"github.com/vinodhalaharvi/silt/compose"
	"github.com/vinodhalaharvi/silt/lang"
)

// EnvInHash lists the environment values that change what a tree means, and
// therefore what the emitted configuration means. BR2_HOST_GCC_AT_LEAST_*
// take their values from a comparison against HOST_GCC_VERSION, and forty
// depends-on clauses compare against HOSTARCH, so the same fragments on two
// hosts are two different configurations (DESIGN.md §14.2).
var EnvInHash = []string{"HOST_GCC_VERSION", "HOSTARCH"}

// Solution renders everything the emitted configuration depends on: the
// configuration itself, the release of each tree it was checked against, and
// the host values that shape those trees.
//
// The point is to be able to answer "was this built from these fragments?"
// Two builds were wasted on a stale defconfig that looked current, and
// nothing in the output said which inputs it came from.
func Solution(r *compose.Result, versions, env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "image %s\n", r.Image.Name)
	for _, f := range r.Fragments {
		fmt.Fprintf(&b, "fragment %s\n", f.ID)
	}
	for _, name := range sortedKeys(versions) {
		fmt.Fprintf(&b, "tree %s %s\n", name, versions[name])
	}
	for _, k := range EnvInHash {
		if v, ok := env[k]; ok {
			fmt.Fprintf(&b, "env %s=%s\n", k, v)
		}
	}
	// Files the fragments carry are configuration too: an overlay that
	// changed produces a different image from the same symbols, and a
	// solution hash that ignored it would say the two builds were the same.
	for _, c := range pathConstraints(r) {
		// A list-valued symbol resolves to several directories; each is
		// hashed, so changing any one of them changes the solution.
		for _, path := range strings.Fields(c.Resolved) {
			fmt.Fprintf(&b, "file %s %s\n", c.Sym, hashPath(path))
		}
	}

	// Component trees: each binary by content, and each policy line.
	//
	// The security claim a component tree makes is "no import, no path",
	// and it holds only if the bytes check read are the bytes the image
	// runs. The overlay's hash above already covers a carried component,
	// but anonymously, as one file among many. Naming each binary against
	// its tree makes the claim itself part of the image's identity: swap a
	// component or widen a policy and the solution is a different one.
	for _, d := range component.Trees(r) {
		for i, abs := range d.ResolvedComponents {
			fmt.Fprintf(&b, "component %s %s %s\n", d.Name, d.Components[i], fileSHA256(abs))
		}
		var policy []string
		for _, c := range r.Constraints[lang.Scope(d.Name)] {
			policy = append(policy, fmt.Sprintf("%s %s %s", d.Name, c.Want, c.Sym.Name))
		}
		sort.Strings(policy)
		for _, line := range policy {
			fmt.Fprintf(&b, "policy %s\n", line)
		}
	}

	// The configuration itself, comments excluded: a header naming the
	// emitting tool is not part of what was configured, and including the
	// hash's own line would make it self-referential.
	trees := append([]string{string(lang.Buildroot)}, Trees(r)...)
	sort.Strings(trees)
	for _, t := range trees {
		for _, line := range strings.Split(Defconfig(r, lang.Scope(t)), "\n") {
			if line == "" || !configLine(line) {
				continue
			}
			fmt.Fprintf(&b, "%s %s\n", t, line)
		}
	}
	return b.String()
}

// SolutionHash is the content address of that.
func SolutionHash(r *compose.Result, versions, env map[string]string) string {
	sum := sha256.Sum256([]byte(Solution(r, versions, env)))
	return hex.EncodeToString(sum[:12])
}

// configLine tells a setting from a comment. "# X is not set" is a setting:
// it is how kbuild writes n, and leaving it out would hash an image with
// negative intent the same as one without it.
func configLine(line string) bool {
	return !strings.HasPrefix(line, "#") || strings.HasSuffix(line, " is not set")
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pathConstraints are the stated (path ...) values, sorted by symbol.
func pathConstraints(r *compose.Result) []lang.Constraint {
	var out []lang.Constraint
	for _, cs := range r.Constraints {
		for _, c := range cs {
			if c.IsPath && !c.Soft {
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sym.String() < out[j].Sym.String() })
	return out
}

// fileSHA256 is the full SHA-256 of a file's bytes and nothing else.
//
// Component lines use it rather than hashPath, which mixes in the path and
// mode. The component line is the one a reviewer compares with what a device
// actually runs, and sha256sum on the device must print the same thing; a
// hash that differed for reasons unrelated to the bytes would read as a
// swapped binary.
func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "missing"
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hashPath is the content address of a file, or of a directory tree taken as
// its sorted list of paths and contents. A missing file hashes as "missing"
// rather than being skipped: two runs where one has the overlay and one does
// not are not the same configuration.
func hashPath(path string) string {
	if path == "" {
		return "unresolved"
	}
	h := sha256.New()
	err := filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(path, p)
		fmt.Fprintf(h, "%s %v %o\n", filepath.ToSlash(rel), info.IsDir(), info.Mode().Perm())
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write(data)
		return nil
	})
	if err != nil {
		return "missing"
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}
