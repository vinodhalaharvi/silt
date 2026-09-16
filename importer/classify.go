package importer

import (
	"strings"

	"github.com/vinodhalaharvi/silt/kconfig"
)

// Class is where the first cut puts a symbol.
type Class uint8

const (
	Target Class = iota
	Profile
)

func (c Class) String() string {
	if c == Target {
		return "target"
	}
	return "profile"
}

// Classify assigns a symbol to the target or the profile and says why.
//
// The rules are deliberately few and legible. They encode one idea: a target
// is whatever would change if the same userspace were moved to other hardware.
// That covers the architecture, the kernel and its headers, the bootloaders
// and firmware, the console, board-specific scripts and overlays, and the host
// tools that build boot media. Everything else is taken to be policy.
//
// A rule may look at the symbol and where the tree defines it, never at the
// value. An earlier rule sent strings mentioning board/ to the target, which
// put BR2_ROOTFS_POST_SCRIPT_ARGS in orangepi-pc2's target and in qemu's
// profile; composing one with the other then stated the symbol twice with
// different values. A split that depends on the value is not a partition of
// anything, so two imported boards cannot be recombined.
//
// They are wrong in known places. A rootfs size is usually dictated by the
// board's genimage layout but is classified as policy here, because nothing in
// the symbol says so. That is what the reason string is for.
func Classify(name string, sym *kconfig.Symbol) (Class, string) {
	file := ""
	if sym != nil {
		file = sym.File
	}
	switch {
	case strings.HasPrefix(file, "arch/"):
		return Target, "defined under arch/"
	case strings.HasPrefix(file, "linux/"):
		return Target, "defined under linux/ (kernel)"
	case strings.HasPrefix(file, "boot/"):
		return Target, "defined under boot/ (bootloader or firmware)"
	case strings.HasPrefix(name, "BR2_PACKAGE_HOST_LINUX_HEADERS_"),
		strings.HasPrefix(name, "BR2_KERNEL_HEADERS_"),
		strings.HasPrefix(name, "BR2_TOOLCHAIN_HEADERS_"):
		return Target, "kernel headers follow the kernel"
	case name == "BR2_TARGET_GENERIC_GETTY_PORT",
		strings.HasPrefix(name, "BR2_TARGET_GENERIC_GETTY_BAUDRATE"):
		return Target, "serial console is hardware"
	case name == "BR2_TARGET_GENERIC_HOSTNAME", name == "BR2_TARGET_GENERIC_ISSUE":
		return Target, "names the board"
	case name == "BR2_ROOTFS_OVERLAY", name == "BR2_ROOTFS_POST_BUILD_SCRIPT",
		name == "BR2_ROOTFS_POST_FAKEROOT_SCRIPT", name == "BR2_ROOTFS_POST_IMAGE_SCRIPT",
		name == "BR2_ROOTFS_POST_SCRIPT_ARGS", name == "BR2_GLOBAL_PATCH_DIR":
		return Target, "board integration hook"
	case strings.HasPrefix(name, "BR2_PACKAGE_") && strings.Contains(name, "FIRMWARE"):
		return Target, "firmware package"
	case strings.HasPrefix(name, "BR2_PACKAGE_HOST_"):
		return Target, "host tool, typically for boot media"
	}
	return Profile, "not hardware by any rule above"
}
