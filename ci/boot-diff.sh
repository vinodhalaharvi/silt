#!/usr/bin/env bash
#
# boot-diff.sh — what one card's boot partition has that another's does not.
#
# A Raspberry Pi's boot partition is the whole handoff from firmware to kernel:
# config.txt says what to load and how, cmdline.txt says what to tell it, the
# device trees say what the hardware is, and the overlays directory says what
# may be turned on. When a board boots one card and not another, the difference
# is often here rather than in either kernel - and it is a difference nobody can
# see, because the two partitions are inside two image files.
#
# This reads both and prints what differs. Nothing is mounted and nothing needs
# root: the boot partition is FAT, and mtools reads FAT from a file at an offset
# directly. Buildroot already builds boot partitions with mtools, so any machine
# that can build an image can run this.
#
#   ci/boot-diff.sh THEIRS OURS
#
# Each argument may be:
#
#   a directory     an already-mounted boot partition, /Volumes/bootfs or /mnt/boot
#   an .img file    the MBR is read and the first FAT partition used
#   an .img.zst     decompressed to a temporary file first
#
# The names are a convention rather than a requirement: "theirs" is whatever
# boots, "ours" is whatever does not, and the output is written from that
# direction - lines and files only in theirs are the candidates for what is
# missing.
#
# What it does not do: say anything about either kernel. A kernel image is a
# compiled binary with no configuration inside it, and the question "is this
# kernel wrong" cannot be answered by reading a partition. If this comes back
# with nothing interesting, the difference is in the kernel and the next tool is
# a serial console.
set -euo pipefail

usage() {
	sed -n '3,30p' "$0" | sed 's/^# \?//'
	exit 2
}

[[ $# -eq 2 ]] || usage
command -v mdir >/dev/null || {
	echo "boot-diff: mtools not found (apt install mtools, brew install mtools)" >&2
	exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# --- reading a boot partition, whatever it arrived as ----------------------

# Where the first FAT partition starts, in bytes. The MBR's four entries live
# at 0x1BE, sixteen bytes each; byte 4 is the type and bytes 8..11 the starting
# LBA, little-endian. FAT types: 0x01, 0x04, 0x06, 0x0b, 0x0c, 0x0e.
fat_offset() {
	local img=$1
	python3 - "$img" <<'PY'
import sys, struct
with open(sys.argv[1], 'rb') as f:
    mbr = f.read(512)
for i in range(4):
    e = mbr[0x1BE + i*16 : 0x1BE + (i+1)*16]
    if e[4] in (0x01, 0x04, 0x06, 0x0b, 0x0c, 0x0e):
        print(struct.unpack('<I', e[8:12])[0] * 512)
        break
else:
    sys.exit("no FAT partition in the MBR")
PY
}

# Copy a boot partition's files into a directory we can walk, whatever the
# source was. Returns the directory.
extract() {
	# Separate lines: bash expands every argument to `local` before assigning
	# any of them, so "$work/$name" on the same line would use an unset name.
	local src=$1
	local name=$2
	local dst="$work/$name"
	mkdir -p "$dst"

	if [[ -d $src ]]; then
		cp -a "$src"/. "$dst"/ 2>/dev/null || true
		echo "$dst"
		return
	fi

	local img=$src
	if [[ $src == *.zst ]]; then
		command -v zstd >/dev/null || { echo "boot-diff: zstd not found" >&2; exit 1; }
		img="$work/$name.img"
		zstd -q -d "$src" -o "$img"
	fi
	[[ -f $img ]] || { echo "boot-diff: no such file: $src" >&2; exit 1; }

	local off
	if ! off=$(fat_offset "$img"); then
		echo "boot-diff: $src has no FAT partition in its MBR" >&2
		exit 1
	fi
	# mcopy needs the offset in the drive definition rather than as a flag.
	export MTOOLS_SKIP_CHECK=1
	printf 'drive z: file="%s" offset=%s\n' "$img" "$off" > "$work/$name.mtoolsrc"
	MTOOLSRC="$work/$name.mtoolsrc" mcopy -s -n z:/* "$dst"/ 2>/dev/null || true
	echo "$dst"
}

theirs=$(extract "$1" theirs)
ours=$(extract "$2" ours)

echo "theirs  $1"
echo "ours    $2"

# --- config.txt ------------------------------------------------------------

# Comments and blank lines are noise; a section header ([pi5], [cm4]) is not,
# because a line's meaning depends on the section it is in.
clean_config() {
	[[ -f $1 ]] || return 0
	grep -v '^[[:space:]]*#' "$1" | grep -v '^[[:space:]]*$' | sed 's/[[:space:]]*$//'
}

echo
echo "== config.txt"
clean_config "$theirs/config.txt" | sort > "$work/tc"
clean_config "$ours/config.txt" | sort > "$work/oc"
if only=$(comm -23 "$work/tc" "$work/oc") && [[ -n $only ]]; then
	echo "  only in theirs:"
	sed 's/^/    /' <<<"$only"
fi
if only=$(comm -13 "$work/tc" "$work/oc") && [[ -n $only ]]; then
	echo "  only in ours:"
	sed 's/^/    /' <<<"$only"
fi
[[ -s "$work/tc" || -s "$work/oc" ]] || echo "  (neither has one)"

# --- cmdline.txt -----------------------------------------------------------
#
# Shown whole and side by side rather than diffed: it is one line, the order of
# its arguments matters, and a word-level diff of a kernel command line is
# harder to read than the two lines themselves.

echo
echo "== cmdline.txt"
echo "  theirs: $(tr -d '\n' < "$theirs/cmdline.txt" 2>/dev/null || echo '(none)')"
echo "  ours:   $(tr -d '\n' < "$ours/cmdline.txt" 2>/dev/null || echo '(none)')"

# --- files -----------------------------------------------------------------

echo
echo "== files"
( cd "$theirs" && find . -type f | sed 's|^\./||' | sort ) > "$work/tf"
( cd "$ours" && find . -type f | sed 's|^\./||' | sort ) > "$work/of"

# An overlays directory has dozens of entries and listing every one buries the
# rest of the output. Collapse any directory that differs wholesale.
summarise() {
	awk -F/ '
		NF > 1 { d[$1]++; next }
		{ print }
		END { for (k in d) printf "%s/ (%d entries)\n", k, d[k] }
	' | sort
}

if only=$(comm -23 "$work/tf" "$work/of" | summarise) && [[ -n $only ]]; then
	echo "  only in theirs:"
	sed 's/^/    /' <<<"$only"
fi
if only=$(comm -13 "$work/tf" "$work/of" | summarise) && [[ -n $only ]]; then
	echo "  only in ours:"
	sed 's/^/    /' <<<"$only"
fi

echo
echo "  in both: $(comm -12 "$work/tf" "$work/of" | wc -l | tr -d ' ') file(s)"

# Same name, different contents - the case neither list above catches, and the
# one most likely to matter for a device tree.
echo
echo "== same name, different contents"
found=0
while read -r f; do
	if ! cmp -s "$theirs/$f" "$ours/$f"; then
		printf '    %-32s theirs %s, ours %s\n' "$f" \
			"$(stat -c %s "$theirs/$f" 2>/dev/null || stat -f %z "$theirs/$f")" \
			"$(stat -c %s "$ours/$f" 2>/dev/null || stat -f %z "$ours/$f")"
		found=1
	fi
done < <(comm -12 "$work/tf" "$work/of")
[[ $found -eq 1 ]] || echo "    (none)"

echo
echo "This compares configuration only. Neither kernel is examined: a kernel"
echo "image carries no configuration, and if nothing above explains the"
echo "failure, the difference is inside one of them and the next tool is a"
echo "serial console."
