#!/usr/bin/env bash
#
# boot-diff.sh — what a card that boots has, that one that does not lacks.
#
# A Raspberry Pi's boot partition is the whole handoff from firmware to kernel:
# config.txt says what to load and how, cmdline.txt says what to tell it, the
# device trees say what the hardware is, and the overlays directory says what
# may be turned on. When a board boots one card and not another, the difference
# is often here rather than in either kernel - and it is invisible, because both
# partitions are inside image files.
#
#   ci/boot-diff.sh THEIRS OURS
#
# Each argument may be an image, an already-mounted boot partition, or a
# compressed image, and the two need not be the same kind:
#
#   ci/boot-diff.sh /Volumes/bootfs build/images/sdcard.img
#   ci/boot-diff.sh raspios.img sdcard.img.zst
#
# What this script contributes is the extraction: pulling a FAT partition out of
# an image at an MBR offset, mounting nothing and needing no root. The comparison
# is diff and git diff, because a unified diff is a format every reader already
# knows and a hand-rolled one is a format nobody does. An earlier version sorted
# config.txt and compared with comm, which threw the order away - and in
# config.txt order is information, because a line means what the [pi5] or [cm4]
# section above it says it means.
#
# It says nothing about either kernel, deliberately. A kernel image is a compiled
# binary carrying no configuration, so "is this kernel wrong" cannot be answered
# by reading a partition. If nothing here explains the failure, the difference is
# inside one of them and the next tool is a serial console.
set -euo pipefail

usage() {
	sed -n '3,31p' "$0" | sed 's/^# \?//'
	exit 2
}

[[ $# -eq 2 ]] || usage
command -v mcopy >/dev/null || {
	echo "boot-diff: mtools not found (apt install mtools, brew install mtools)" >&2
	exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Where the first FAT partition starts, in bytes. The MBR's four entries live at
# 0x1BE, sixteen bytes each; byte 4 is the type and bytes 8..11 the starting LBA,
# little-endian. FAT types: 0x01, 0x04, 0x06, 0x0b, 0x0c, 0x0e.
fat_offset() {
	python3 - "$1" <<'PY'
import sys, struct
with open(sys.argv[1], 'rb') as f:
    mbr = f.read(512)
for i in range(4):
    e = mbr[0x1BE + i*16 : 0x1BE + (i+1)*16]
    if e[4] in (0x01, 0x04, 0x06, 0x0b, 0x0c, 0x0e):
        print(struct.unpack('<I', e[8:12])[0] * 512)
        break
else:
    sys.exit(1)
PY
}

# Put a boot partition's files somewhere walkable, whatever the source was.
extract() {
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
		echo "boot-diff: no FAT partition in the MBR of $src" >&2
		exit 1
	fi

	# mcopy takes the offset in a drive definition rather than as a flag.
	export MTOOLS_SKIP_CHECK=1
	printf 'drive z: file="%s" offset=%s\n' "$img" "$off" > "$work/$name.mtoolsrc"
	MTOOLSRC="$work/$name.mtoolsrc" mcopy -s -n z:/* "$dst"/ 2>/dev/null || true
	echo "$dst"
}

theirs=$(extract "$1" theirs)
ours=$(extract "$2" ours)

echo "--- $1"
echo "+++ $2"

# git diff --no-index works outside a repository and brings colour and --stat.
# Both it and diff exit non-zero when things differ, which here is the expected
# case rather than a failure.
have_git=0
command -v git >/dev/null && have_git=1

# An overview from git diff --stat was here and came out worse than the sections
# below: with two temporary directories git renders every added or removed file
# as a {a => b} pair with the temp path spliced through the middle, and fighting
# that formatting to say what the file list already says is not worth it.

echo
echo "== config.txt"
diff -u "$theirs/config.txt" "$ours/config.txt" 2>/dev/null | tail -n +3 || true

echo
echo "== cmdline.txt"
# One long line, so a word diff reads where a line diff does not.
if [[ $have_git -eq 1 ]]; then
	git --no-pager diff --no-index --word-diff=plain --unified=0 \
		"$theirs/cmdline.txt" "$ours/cmdline.txt" 2>/dev/null | tail -n +5 || true
else
	echo "  theirs: $(tr -d '\n' < "$theirs/cmdline.txt" 2>/dev/null)"
	echo "  ours:   $(tr -d '\n' < "$ours/cmdline.txt" 2>/dev/null)"
fi

echo
echo "== every other file"
# -r walks both trees, reports what is present on one side only, and says
# "Files ... differ" for binaries rather than printing them. -q keeps it to one
# line each, which is what you want across a directory of device trees.
#
# A directory absent on one side is collapsed to one line with a count: an
# overlays directory has dozens of entries and listing each buries the rest.
diff -rq "$theirs" "$ours" 2>/dev/null |
	grep -vE "(^Only in .*: (config|cmdline)\.txt$|/(config|cmdline)\.txt and )" |
	while IFS= read -r line; do
		if [[ $line =~ ^Only\ in\ (.*):\ (.*)$ ]]; then
			d=${BASH_REMATCH[1]}; f=${BASH_REMATCH[2]}
			if [[ -d "$d/$f" ]]; then
				n=$(find "$d/$f" -type f | wc -l | tr -d ' ')
				echo "Only in $d: $f/ ($n entries)"
				continue
			fi
		fi
		echo "$line"
	done |
	sed -e "s|$theirs|theirs|g" -e "s|$ours|ours|g" || true

echo
echo "Configuration only: neither kernel is examined. A kernel image carries no"
echo "configuration, so if nothing above explains the failure, the difference is"
echo "inside one of them and the next tool is a serial console."
