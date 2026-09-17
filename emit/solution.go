package emit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

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
