# nanopi-r2s

FriendlyARM NanoPi R2S, RK3328, from `configs/friendlyarm_nanopi_r2s_defconfig`
in Buildroot 2025.02.16, imported with `silt import` and then split by hand.

`board/` holds the four files the defconfig points at, copied from
`board/friendlyarm/nanopi-r2s/` in that release (GPL-2.0-or-later, as the rest
of Buildroot). They are copied rather than referenced because a pack that
pointed into somebody's Buildroot checkout would work only for people whose
checkout has them — that is, for one release.

`post-build.sh` finds its own directory with `dirname $0`, so it works
unchanged wherever the pack is checked out.

**Not built or booted.** The R2S has no QEMU machine; this pack is checked
against Buildroot's Kconfig and its emitted defconfig is accepted by kbuild,
and that is all anyone has verified. The claims about the hardware —
`sdcard`, `serial-console` — come from the board's documentation, not from
anything Silt can check.
