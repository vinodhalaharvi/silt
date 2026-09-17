package kconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	versionRe = regexp.MustCompile(`(?m)^export BR2_VERSION\s*:?=\s*(.+)$`)
	kernelRe  = regexp.MustCompile(`(?m)^VERSION = (\d+)\nPATCHLEVEL = (\d+)\nSUBLEVEL = (\d+)`)
)

// TreeVersion reads a Buildroot tree's own version string from its Makefile.
//
// This is what makes pinning possible: a fragment's claims are true of a
// release, not of Buildroot in general, and every failure this check exists to
// catch was release-specific.
func TreeVersion(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return "", err
	}
	m := versionRe.FindSubmatch(data)
	if m == nil {
		return "", os.ErrNotExist
	}
	return strings.TrimSpace(string(m[1])), nil
}

// KernelVersion reads a Linux tree's version from its Makefile, as
// VERSION.PATCHLEVEL.SUBLEVEL. The same reason as TreeVersion: CONFIG_ symbols
// come and go between releases, so a claim is about one of them.
// CONFIG_EXT4_ENCRYPTION was real once and is not in 6.12.
func KernelVersion(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return "", err
	}
	m := kernelRe.FindSubmatch(data)
	if m == nil {
		return "", os.ErrNotExist
	}
	v := fmt.Sprintf("%s.%s.%s", m[1], m[2], m[3])
	return strings.TrimSuffix(v, ".0"), nil
}

// LinuxEnv is the environment the kernel's Kconfig needs: the architecture it
// is being configured for, which decides which arch/ tree is sourced at all.
func LinuxEnv(root, arch string) (map[string]string, error) {
	ver, err := KernelVersion(root)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"ARCH": arch, "SRCARCH": srcArch(arch), "KERNELVERSION": ver, "srctree": ".",
	}, nil
}

// srcArch maps ARCH to the directory under arch/, as the kernel's Makefile
// does for the few that differ.
func srcArch(arch string) string {
	switch arch {
	case "i386", "x86_64":
		return "x86"
	case "sparc32", "sparc64":
		return "sparc"
	case "parisc64":
		return "parisc"
	}
	return arch
}
