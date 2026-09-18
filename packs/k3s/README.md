# k3s

Lightweight Kubernetes as a Silt feature: the package, the kernel options, and
what it requires of userspace.

`br2-external/package/k3s` installs the upstream release binary
(v1.31.14+k3s1) rather than building it. k3s is a Go program vendoring
containerd, runc, flannel and CoreDNS; building it needs a Go toolchain and
roughly an hour, and produces the binary upstream already publishes. The
hashes in `k3s.hash` are the ones in the release's own `sha256sum-<arch>.txt`.

The kernel list in `fragments/features/k3s.sx` is k3s's own
`contrib/util/check-config.sh` at 4c6e5fd, transcribed symbol for symbol —
the script k3s tells you to run on a machine whose pods will not start. Every
one of those symbols is checked to exist by `silt check --linux DIR`.

k3s is not stated to need systemd and glibc; it *requires* them as
capabilities, so composing it onto `profile:minimal` fails at the composition
with the reason, rather than at the build or at the first boot.

**Not built and not booted.** This sandbox has no hour to spare and no way to
run QEMU. What has been checked: the pack's interface, that every symbol it
states exists in Buildroot 2025.02.16 and in Linux 6.12, that the composition
solves, and that kbuild keeps every stated symbol. `tests/` holds what a boot
should assert and the extra QEMU memory it needs; nobody has run it.
