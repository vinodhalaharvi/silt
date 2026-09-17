package kconfig

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The values Buildroot's make passes to kconfig as environment variables.
// They are part of the model: BR2_HOST_GCC_AT_LEAST_* take their defaults
// from a string comparison against HOST_GCC_VERSION, so the same defconfig
// configures differently on a host with a different compiler (DESIGN.md
// §14.2). Each is derived exactly as Buildroot's Makefile derives it, and an
// earlier hard-coded "13 2" was wrong twice: the host was gcc 13.3, and for
// gcc 5 and later Buildroot passes only the major version.

var (
	gccVersionRe = regexp.MustCompile(`(?m)^.* ([0-9]*)\.([0-9]*)\.([0-9]*)[ ]*.*$`)
	gccTargetRe  = regexp.MustCompile(`(?m)^Target: ([^-]*)`)
	maxVersionRe = regexp.MustCompile(`(?m)^HOSTCC_MAX_VERSION := (\d+)`)
)

// HostGCCVersion computes HOSTCC_VERSION from `gcc --version` output, given
// the tree's HOSTCC_MAX_VERSION.
func HostGCCVersion(versionOutput string, max int) string {
	m := gccVersionRe.FindStringSubmatch(versionOutput)
	if m == nil {
		return ""
	}
	major, minor := m[1], m[2]
	if n, err := strconv.Atoi(major); err == nil && max > 0 && n > max {
		major, minor = strconv.Itoa(max), ""
	}
	if major == "4" {
		return major + " " + minor
	}
	return major
}

// HostArch computes HOSTARCH from `gcc -v` output.
func HostArch(verboseOutput string) string {
	m := gccTargetRe.FindStringSubmatch(verboseOutput)
	if m == nil {
		return ""
	}
	a := m[1]
	switch {
	case regexp.MustCompile(`^i.86$`).MatchString(a):
		return "x86"
	case a == "sun4u":
		return "sparc64"
	case strings.HasPrefix(a, "arm"):
		return "arm"
	}
	return a
}

// BuildrootEnv returns the kconfig environment for a Buildroot tree on this
// host, with baseDir as the output directory (make's O=).
func BuildrootEnv(root, baseDir string) (map[string]string, error) {
	ver, err := TreeVersion(root)
	if err != nil {
		return nil, err
	}
	max := 0
	if mk, err := readFile(filepath.Join(root, "Makefile")); err == nil {
		if m := maxVersionRe.FindStringSubmatch(mk); m != nil {
			max, _ = strconv.Atoi(m[1])
		}
	}
	cc := "gcc"
	vout, _ := exec.Command(cc, "--version").Output()
	vv, _ := exec.Command(cc, "-v").CombinedOutput()
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		abs = baseDir
	}
	return map[string]string{
		"BR2_BASE_DIR":     abs,
		"BASE_DIR":         abs,
		"HOSTARCH":         HostArch(string(vv)),
		"HOST_GCC_VERSION": HostGCCVersion(string(vout), max),
		"BR2_VERSION_FULL": ver,
		"SKIP_LEGACY":      "",
	}, nil
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
