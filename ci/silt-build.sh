#!/bin/sh
#
# silt-build - build an image, reusing whatever has already been built.
#
#   ci/silt-build.sh IMAGE.sx [-o OUTDIR]
#   ci/silt-build.sh --list        what is in the store
#   ci/silt-build.sh --prune N     keep trees for the N most recent slots
#
# Three kinds of answer, in order of how little work they are:
#
#   hit      this exact image was built before: copy the artifact out.
#   prefix   something was built that this image only adds to: restore that
#            output tree and let Buildroot build the difference.
#   miss     build it from nothing.
#
# The prefix case is where a cache like this usually becomes subtly wrong, so
# it is worth being explicit about what makes one safe.
#
# WHAT MAKES A PREFIX SAFE
#
# Buildroot decides what to rebuild from stamp files, not from inputs. Drop a
# tree built with one configuration into a build with another and every
# package already stamped is skipped, whatever the new configuration says
# about it. So a restored tree is only safe if nothing already built in it
# would have been built differently.
#
# silt can answer that, which is the reason to use it here rather than
# guessing: silt complete predicts the .config an image will become, without
# running make. A stored tree is a safe prefix when every line of its
# predicted config also appears in the target's - same symbol, same value.
#
#   added symbols    fine, Buildroot builds the new packages
#   changed value    not fine, whatever depended on it is already stamped
#   removed symbol   not fine either, and this is the monotonicity rule made
#                    mechanical: you may add to a build, never take away
#
# The exception is a short list of symbols consumed after every package is
# built, by steps Buildroot re-runs on every make: the rootfs overlays, the
# post-build and post-image scripts, the filesystem size, the firmware config
# files. A changed overlay cannot have affected a compiled package, because
# nothing reads it until target-finalize. rootfs_only() below is that list.
# It is what lets a plain board image be a prefix of the appliance built on
# it, which is the common case here and would otherwise never match.
#
# WHY EVERY BUILD HAPPENS IN ONE DIRECTORY
#
# A Buildroot output tree is not relocatable: absolute paths are baked into
# .config, the generated Makefile, libtool archives and stamps. Restoring a
# tree built in ~/br-pi3 into ~/br-pi5 produces a build that fails in
# confusing ways. So every build happens in $SILT_CACHE/work, and the image
# is copied out afterwards. The -o directory receives the image and the
# defconfig, not a build tree.
#
# WHY THERE IS A DOWNLOAD DIRECTORY OUTSIDE ALL OF THIS
#
# Buildroot's default download directory lives inside the output tree, and
# this script wipes the work tree before every build that is not an exact
# hit. Left at the default, a miss would download the kernel tarball again
# every time. BR2_DL_DIR points at one shared directory that nothing here
# deletes, which is also what makes a cold build for a new board tolerable:
# most of what it fetches, some other board already fetched.
#
# Environment:
#   SILT_CACHE       the store (default ~/.cache/silt)
#   BR2_DL_DIR       shared downloads (default ~/.cache/buildroot-dl)
#   BUILDROOT        the Buildroot tree (default ~/buildroot)
#   SILT             the silt binary (default ./bin/silt)
#   JOBS             parallelism (default nproc)
#   SILT_KEEP_TREES  0 to store images only, no trees (default 1)

set -e

SILT=${SILT:-./bin/silt}
BUILDROOT=${BUILDROOT:-$HOME/buildroot}
SILT_CACHE=${SILT_CACHE:-$HOME/.cache/silt}
JOBS=${JOBS:-$(nproc 2>/dev/null || echo 4)}
SILT_KEEP_TREES=${SILT_KEEP_TREES:-1}

STORE=$SILT_CACHE/slots
WORK=$SILT_CACHE/work

BR2_DL_DIR=${BR2_DL_DIR:-$HOME/.cache/buildroot-dl}
export BR2_DL_DIR
mkdir -p "$BR2_DL_DIR"

# Symbols consumed after every package is built, by steps that run on every
# make. A tree differing only in these is still a safe prefix.
rootfs_only() {
	grep -vE '^(BR2_ROOTFS_OVERLAY|BR2_ROOTFS_POST_BUILD_SCRIPT|BR2_ROOTFS_POST_IMAGE_SCRIPT|BR2_ROOTFS_POST_SCRIPT_ARGS|BR2_TARGET_ROOTFS_EXT2_SIZE|BR2_TARGET_ROOTFS_TAR|BR2_PACKAGE_RPI_FIRMWARE_CONFIG_FILE|BR2_PACKAGE_RPI_FIRMWARE_CMDLINE_FILE)='
}

cmd_list() {
	[ -d "$STORE" ] || { echo "empty store at $STORE"; return 0; }
	for slot in "$STORE"/*/; do
		[ -f "$slot/manifest" ] || continue
		tree=""
		[ -f "$slot/tree.tar" ] && tree=" +tree"
		[ -f "$slot/tree.tar.zst" ] && tree=" +tree"
		printf "%s%s\n" "$(sed -n '1,3p' "$slot/manifest" | tr '\n' ' ')" "$tree"
	done
}

# Trees are gigabytes and images are megabytes, so pruning drops trees and
# keeps images: an exact rebuild is still a copy afterwards.
cmd_prune() {
	keep=${1:-5}
	[ -d "$STORE" ] || return 0
	ls -dt "$STORE"/*/ 2>/dev/null | tail -n +"$((keep + 1))" | while read -r slot; do
		if [ -f "$slot/tree.tar" ] || [ -f "$slot/tree.tar.zst" ]; then
			rm -f "$slot/tree.tar" "$slot/tree.tar.zst"
			echo "pruned tree $(basename "$slot")"
		fi
	done
}

case "$1" in
--list)  cmd_list; exit 0 ;;
--prune) shift; cmd_prune "$@"; exit 0 ;;
esac

IMAGE=$1
[ -n "$IMAGE" ] || { echo "usage: $0 IMAGE.sx [-o OUTDIR] | --list | --prune N" >&2; exit 1; }
shift

OUT=
while [ $# -gt 0 ]; do
	case "$1" in
	-o) OUT=$2; shift 2 ;;
	*)  echo "$0: unknown argument $1" >&2; exit 1 ;;
	esac
done

name=$(basename "$IMAGE" .sx)
OUT=${OUT:-$HOME/br-$name}

[ -x "$SILT" ] || { echo "$0: no silt at $SILT (run make)" >&2; exit 1; }
[ -d "$BUILDROOT" ] || { echo "$0: no Buildroot at $BUILDROOT" >&2; exit 1; }

mkdir -p "$STORE"

# One work directory means two builds at once would destroy each other's
# tree, and the second would look like a mysterious build failure rather
# than a mistake. A lock turns it into a sentence.
if command -v flock >/dev/null 2>&1; then
	exec 9>"$SILT_CACHE/lock"
	if ! flock -n 9; then
		echo "$0: another build is running (holding $SILT_CACHE/lock)" >&2
		exit 1
	fi
fi

# Composing first means this fails on anything silt check would have caught,
# before a directory is touched.
solution=$("$SILT" hash --solution "$IMAGE" --buildroot "$BUILDROOT" | head -1 | awk '{print $1}')
[ -n "$solution" ] || { echo "$0: could not compute a solution hash" >&2; exit 1; }

# BR2_EXTERNAL from the composition rather than from memory.
externals=$("$SILT" externals "$IMAGE" --buildroot "$BUILDROOT")

# The key: what was composed, and what it was composed from. silt's solution
# hash covers the configuration and each tree's release, not a pack's own C
# source - right for a hash recorded in a defconfig, wrong for a cache, which
# would then serve an image built from the old source.
tree_hash() {
	find "$1" -type f -print0 2>/dev/null | LC_ALL=C sort -z |
		xargs -0 sha256sum 2>/dev/null | sha256sum | cut -c1-24
}
key=$solution
IFS=:
for dir in $externals; do
	[ -d "$dir" ] || continue
	key="$key $(tree_hash "$dir")"
done
unset IFS
hash=$(printf '%s' "$key" | sha256sum | cut -c1-24)
slot=$STORE/$hash

if [ -f "$slot/sdcard.img" ]; then
	touch "$slot"
	echo "hit    $name  $hash"
	mkdir -p "$OUT/build/images"
	cp "$slot/sdcard.img" "$OUT/build/images/sdcard.img"
	cp "$slot/defconfig" "$OUT/defconfig" 2>/dev/null || true
	echo "       $OUT/build/images/sdcard.img"
	exit 0
fi

# The predicted .config, which is what makes a prefix decidable.
"$SILT" complete "$IMAGE" --buildroot "$BUILDROOT" -o "$SILT_CACHE/target.config" >/dev/null
grep -v '^#' "$SILT_CACHE/target.config" | rootfs_only | LC_ALL=C sort > "$SILT_CACHE/target.sorted"

best=
best_lines=0
for cand in "$STORE"/*/; do
	[ -f "$cand/predicted.config" ] || continue
	if [ ! -f "$cand/tree.tar" ] && [ ! -f "$cand/tree.tar.zst" ]; then
		continue
	fi

	grep -v '^#' "$cand/predicted.config" | rootfs_only | LC_ALL=C sort > "$SILT_CACHE/cand.sorted"

	# Every line of the candidate must appear in the target, symbol and
	# value alike. One line that does not, and the tree is unsafe.
	if [ -n "$(comm -23 "$SILT_CACHE/cand.sorted" "$SILT_CACHE/target.sorted" | head -1)" ]; then
		continue
	fi
	lines=$(grep -c . "$SILT_CACHE/cand.sorted" || true)
	if [ "${lines:-0}" -gt "$best_lines" ]; then
		best=$cand
		best_lines=$lines
	fi
done

rm -rf "$WORK"
mkdir -p "$WORK"

if [ -n "$best" ]; then
	from=$(sed -n 's/^image  *//p' "$best/manifest" | head -1)
	echo "prefix $name  $hash  (from $from, $best_lines symbols shared)"
	if [ -f "$best/tree.tar.zst" ]; then
		zstd -dc "$best/tree.tar.zst" | tar -xf - -C "$WORK"
	else
		tar -xf "$best/tree.tar" -C "$WORK"
	fi
else
	echo "miss   $name  $hash"
fi

"$SILT" emit "$IMAGE" -o "$WORK" --buildroot "$BUILDROOT" >/dev/null

# defconfig every time: Buildroot builds from output/.config, which a plain
# make leaves alone, so a changed defconfig is otherwise ignored.
make -C "$BUILDROOT" O="$WORK/build" \
	${externals:+BR2_EXTERNAL="$externals"} \
	defconfig BR2_DEFCONFIG="$WORK/defconfig"

start=$(date +%s)
make -C "$BUILDROOT" O="$WORK/build" -j"$JOBS"
took=$(( $(date +%s) - start ))

img=$WORK/build/images/sdcard.img
[ -f "$img" ] || { echo "$0: built, but no $img to store" >&2; exit 1; }

mkdir -p "$slot"
cp "$img" "$slot/sdcard.img"
cp "$WORK/defconfig" "$slot/defconfig"
cp "$SILT_CACHE/target.config" "$slot/predicted.config"

if [ "$SILT_KEEP_TREES" = 1 ]; then
	printf "       storing tree: "
	if command -v zstd >/dev/null 2>&1; then
		tar -cf - -C "$WORK" . | zstd -q -3 -T0 -o "$slot/tree.tar.zst.new"
		mv "$slot/tree.tar.zst.new" "$slot/tree.tar.zst"
		du -h "$slot/tree.tar.zst" | cut -f1
	else
		tar -cf "$slot/tree.tar" -C "$WORK" .
		echo "$(du -h "$slot/tree.tar" | cut -f1), install zstd to shrink this"
	fi
fi

{
	echo "image  $name"
	echo "hash   $hash"
	echo "built  $(date -u +%Y-%m-%dT%H:%M:%SZ) in ${took}s"
	echo "solution $solution"
	echo "size   $(du -h "$img" | cut -f1)"
} > "$slot/manifest"

mkdir -p "$OUT/build/images"
cp "$img" "$OUT/build/images/sdcard.img"
cp "$WORK/defconfig" "$OUT/defconfig"

echo "store  $name  $hash  (${took}s)"
echo "       $OUT/build/images/sdcard.img"
