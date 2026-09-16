package kconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionRe = regexp.MustCompile(`(?m)^export BR2_VERSION\s*:?=\s*(.+)$`)

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
