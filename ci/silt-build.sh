#!/bin/sh
#
# silt-build - build an image, or fetch it from the store if it is already
# built.
#
#   ci/silt-build.sh IMAGE.sx [-o OUTDIR]
#
# The key is the solution hash - the composed configuration, the release of
# every tree it was checked against, and the host values those trees depend
# on - plus the contents of every br2-external tree the image uses. Same key
# means the same image, so the second build of anything is a copy rather
# than forty minutes.
#
# The second half of that key is not decoration. silt's solution hash covers
# the composition, which is a claim about configuration, and a pack's C
# source is not configuration: editing silt-modbusd.c leaves the solution
# hash identical. Right for what that hash is for, wrong for a cache, which
# would hand back an image built from the old source. So the key here is
# both: what was composed, and what it was composed from.
#
# Layout of the store, one directory per solution:
#
#   $SILT_CACHE/<hash>/sdcard.img      the artifact
#   $SILT_CACHE/<hash>/defconfig       what built it
#   $SILT_CACHE/<hash>/manifest        image name, date, buildroot, sizes
#
# Only the images are kept, not the output trees. Output trees are gigabytes
# and their value is in making the *next* build faster, which is the prefix
# reuse this does not do yet; artifacts are megabytes and answer the common
# case, which is building the same thing again.
#
# Environment:
#   SILT_CACHE    where the store lives (default ~/.cache/silt/images)
#   BUILDROOT     the Buildroot tree (default ~/buildroot)
#   SILT          the silt binary (default ./bin/silt)
#   JOBS          parallelism (default nproc)

set -e

IMAGE=$1
[ -n "$IMAGE" ] || { echo "usage: $0 IMAGE.sx [-o OUTDIR]" >&2; exit 1; }
shift

OUT=
while [ $# -gt 0 ]; do
	case "$1" in
	-o) OUT=$2; shift 2 ;;
	*)  echo "$0: unknown argument $1" >&2; exit 1 ;;
	esac
done

SILT=${SILT:-./bin/silt}
BUILDROOT=${BUILDROOT:-$HOME/buildroot}
SILT_CACHE=${SILT_CACHE:-$HOME/.cache/silt/images}
JOBS=${JOBS:-$(nproc 2>/dev/null || echo 4)}

name=$(basename "$IMAGE" .sx)
OUT=${OUT:-$HOME/br-$name}

[ -x "$SILT" ] || { echo "$0: no silt at $SILT (run make)" >&2; exit 1; }
[ -d "$BUILDROOT" ] || { echo "$0: no Buildroot at $BUILDROOT" >&2; exit 1; }

# The solution hash first: this is a composition, so it fails here on
# anything silt check would have caught, before any directory is touched.
solution=$("$SILT" hash --solution "$IMAGE" --buildroot "$BUILDROOT" | head -1 | awk '{print $1}')
[ -n "$solution" ] || { echo "$0: could not compute a solution hash" >&2; exit 1; }

# BR2_EXTERNAL comes from the composition rather than from memory: the packs
# this image actually composes a fragment from.
externals=$("$SILT" externals "$IMAGE" --buildroot "$BUILDROOT")

# Every file in those trees, by path and by content. sha256sum prints the
# name beside the hash, so a renamed file changes the result too.
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

slot=$SILT_CACHE/$hash

if [ -f "$slot/sdcard.img" ]; then
	echo "hit   $name  $hash"
	sed 's/^/      /' "$slot/manifest" 2>/dev/null || true
	if [ -n "$OUT" ]; then
		mkdir -p "$OUT/build/images"
		cp "$slot/sdcard.img" "$OUT/build/images/sdcard.img"
		cp "$slot/defconfig" "$OUT/defconfig" 2>/dev/null || true
		echo "      copied to $OUT/build/images/sdcard.img"
	fi
	exit 0
fi

echo "miss  $name  $hash"

"$SILT" emit "$IMAGE" -o "$OUT" --buildroot "$BUILDROOT" >/dev/null

# defconfig, every time. Buildroot builds from output/.config, which a plain
# make leaves alone, so a changed defconfig is silently ignored - the failure
# that costs an evening and looks like anything but a config problem.
make -C "$BUILDROOT" O="$OUT/build" \
	${externals:+BR2_EXTERNAL="$externals"} \
	defconfig BR2_DEFCONFIG="$OUT/defconfig"

start=$(date +%s)
make -C "$BUILDROOT" O="$OUT/build" -j"$JOBS"
took=$(( $(date +%s) - start ))

img=$OUT/build/images/sdcard.img
[ -f "$img" ] || { echo "$0: built, but no $img to store" >&2; exit 1; }

mkdir -p "$slot"
cp "$img" "$slot/sdcard.img"
cp "$OUT/defconfig" "$slot/defconfig"
{
	echo "image      $name"
	echo "hash       $hash"
	echo "solution   $solution"
	echo "built      $(date -u +%Y-%m-%dT%H:%M:%SZ) in ${took}s"
	echo "buildroot  $BUILDROOT"
	echo "image size $(du -h "$img" | cut -f1)"
} > "$slot/manifest"

echo "store $name  $hash  (${took}s)"
