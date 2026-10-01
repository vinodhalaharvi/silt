#!/usr/bin/env bash
#
# silt-build - build an image, reusing whatever has already been built.
#
#   ci/silt-build.sh IMAGE.sx [-o OUTDIR]
#   ci/silt-build.sh --list        what is in the store
#   ci/silt-build.sh --prune N     keep trees for the N most recent slots
#
# Two checks bracket the build, and they ask different questions.
#
# silt solve, first: can this image exist at all? It composes, so a conflict
# between two fragments fails here, and it asks the Kconfig model whether the
# result is satisfiable. 400ms. (silt check over the whole tree would be the
# thorough answer and takes seventeen seconds, most of it spent on forty
# images this build does not involve; and naming one image file does not work,
# because an image composing image:something needs the file that defines it.)
#
# silt fixpoint, after make defconfig and before the long make: did kbuild
# honour what the image stated? It trusts nothing and reads the .config
# kbuild produced. That is how "asked for y, kbuild would write absent" gets
# caught on a board nobody has tried, for a second instead of forty minutes.
#
# And then ci/config-agrees.awk, which is the same idea taken to its
# conclusion. fixpoint checks the symbols the image states - 43 of them on
# the Pi 3 appliance. The prediction covers 460, and the cache's prefix test
# compares all of them, including the four hundred nobody stated. If the
# model were wrong about one of those, the cache could accept a prefix it
# should not, and fixpoint would never notice. So every symbol kbuild knew
# about is compared, set and unset alike: that is the foundation the store
# rests on, verified on every build rather than assumed.
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
#   SILT_WORK        this build's scratch tree (default $SILT_CACHE/work)
#   SILT_LOCK        the lock guarding SILT_WORK (default $SILT_CACHE/lock)
#   BR2_DL_DIR       shared downloads (default ~/.cache/buildroot-dl)
#   BUILDROOT        the Buildroot tree (default ~/buildroot)
#   SILT             the silt binary (default ./bin/silt)
#   JOBS             parallelism (default nproc)
#   SILT_KEEP_TREES  0 to store images only, no trees (default 1)

set -e

# The store may be shared between accounts - a person at a terminal and a CI
# job that deliberately runs as somebody with nothing in their home directory.
# 002 makes what is written here group-writable, which with a setgid store
# directory lets either account prune or replace what the other wrote.
# Harmless when the store is private: the group is then the owner's own.
umask 002

SILT=${SILT:-./bin/silt}
BUILDROOT=${BUILDROOT:-$HOME/buildroot}
SILT_CACHE=${SILT_CACHE:-$HOME/.cache/silt}
JOBS=${JOBS:-$(nproc 2>/dev/null || echo 4)}
SILT_KEEP_TREES=${SILT_KEEP_TREES:-1}

STORE=$SILT_CACHE/slots

# The work directory and the lock are separable from the store, and CI wants
# them separate. The store is keyed by content - what was composed and what it
# was composed from - so two people building the same image produce the same
# key and can share every tree in it. The work directory cannot be shared: it
# is one build's scratch space, wiped at the start of every build that is not
# an exact hit.
#
# So a person at a terminal and a CI job on the same machine share the warm
# store, which is the expensive thing, and keep their own work directories and
# their own locks, which is the thing that would otherwise have one delete the
# other's tree mid-build.
#
# They do then compete for the machine's cores and disk, which is a slower
# build rather than a broken one.
WORK=${SILT_WORK:-$SILT_CACHE/work}
LOCK=${SILT_LOCK:-$SILT_CACHE/lock}

BR2_DL_DIR=${BR2_DL_DIR:-$HOME/.cache/buildroot-dl}
export BR2_DL_DIR
mkdir -p "$BR2_DL_DIR"

# Symbols that cannot make a stored tree unsafe, and so are ignored when
# deciding whether it is a prefix. Two kinds.
#
# Consumed after every package is built, by steps Buildroot re-runs on every
# make: the overlays, the post-build and post-image scripts, the filesystem
# size, the firmware config files. A changed overlay cannot have affected a
# compiled package, because nothing reads it until target-finalize.
#
# And filled in from the invocation rather than the configuration:
# BR2_DEFCONFIG, the kernel fragment path, BR2_DL_DIR, BR2_JLEVEL, and every
# BR2_EXTERNAL_*. That last one matters more than it looks.
# BR2_EXTERNAL_<NAME>_VERSION is git describe of the external tree, so it
# changes on every commit to this repository - which disqualified every
# stored tree the moment anything was committed, and turned what should have
# been a one-package delta into a twenty-two minute rebuild. It names a
# string nothing is compiled against.
#
# The list itself is `silt affects`, not a regular expression here. Which
# settings change what a build produces is a judgement about a kind of tree,
# and it lived in two places - a shell pattern and the tool - with no way to
# notice when they stopped agreeing. Each entry carries its reason where it
# is decided.
not_build_affecting() {
	"$SILT" affects
}

# What counts as the built thing, which is not the same on every board.
#
# A Raspberry Pi, a Rock 5B and an i.MX EVK all produce sdcard.img, so the
# store assumed one. A QEMU target does not: it produces a kernel and a
# filesystem, to be passed to qemu-system-* separately, and the build that
# revealed this had succeeded - every package compiled, the rootfs assembled -
# and then failed at "built, but no sdcard.img to store".
#
# So: whatever the board makes. sdcard.img when there is one, otherwise the
# kernel and the filesystem, and the manifest records which.
artifacts_in() {
	local dir=$1 found=()
	local candidate
	for candidate in sdcard.img disk.img; do
		if [ -f "$dir/$candidate" ]; then
			printf '%s\n' "$candidate"
			return 0
		fi
	done
	# A QEMU image, or any board whose output is loose files. rootfs.ext4 is
	# a symlink to rootfs.ext2 in Buildroot, so both are listed and the copy
	# dereferences.
	for candidate in Image zImage bzImage rootfs.ext4 rootfs.squashfs rootfs.cpio.gz; do
		[ -f "$dir/$candidate" ] && found+=("$candidate")
	done
	[ ${#found[@]} -gt 0 ] || return 1
	printf '%s\n' "${found[@]}"
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
	mkdir -p "$(dirname "$LOCK")"
	exec 9>"$LOCK"
	if ! flock -n 9; then
		echo "$0: another build is running (holding $LOCK)" >&2
		exit 1
	fi
fi

# The gate. Everything below assumes this image can exist; without it an
# incoherent one surfaces as a Buildroot error deep in a build rather than as
# a sentence now.
if ! "$SILT" solve "$IMAGE" --buildroot "$BUILDROOT" >/dev/null 2>&1; then
	echo "$0: this image does not compose:" >&2
	"$SILT" solve "$IMAGE" --buildroot "$BUILDROOT" >&2 || true
	exit 1
fi

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

if [ -s "$slot/artifacts" ]; then
	touch "$slot"
	echo "hit    $name  $hash"
	mkdir -p "$OUT/build/images"
	while read -r a; do
		[ -n "$a" ] || continue
		cp "$slot/$a" "$OUT/build/images/$a"
		echo "       $OUT/build/images/$a"
	done < "$slot/artifacts"
	cp "$slot/defconfig" "$OUT/defconfig" 2>/dev/null || true
	exit 0
fi

# The predicted .config, which is what makes a prefix decidable.
"$SILT" complete "$IMAGE" --buildroot "$BUILDROOT" -o "$WORK.target.config" >/dev/null
grep -v '^#' "$WORK.target.config" | not_build_affecting | LC_ALL=C sort > "$WORK.target.sorted"

best=
best_lines=0
for cand in "$STORE"/*/; do
	[ -f "$cand/predicted.config" ] || continue
	if [ ! -f "$cand/tree.tar" ] && [ ! -f "$cand/tree.tar.zst" ]; then
		continue
	fi

	grep -v '^#' "$cand/predicted.config" | not_build_affecting | LC_ALL=C sort > "$WORK.cand.sorted"

	# Every line of the candidate must appear in the target, symbol and
	# value alike. One line that does not, and the tree is unsafe.
	if [ -n "$(comm -23 "$WORK.cand.sorted" "$WORK.target.sorted" | head -1)" ]; then
		continue
	fi
	lines=$(grep -c . "$WORK.cand.sorted" || true)
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

# The audit. silt check trusts silt's model of Kconfig; this trusts nothing
# and reads what kbuild wrote. A disagreement is a bug in silt, and it is
# worth finding here rather than in an image someone flashed.
if ! "$SILT" fixpoint "$IMAGE" --buildroot "$BUILDROOT" \
	--config "$WORK/build/.config" >/dev/null 2>&1; then
	echo "$0: kbuild did not write what silt predicted:" >&2
	"$SILT" fixpoint "$IMAGE" --buildroot "$BUILDROOT" \
		--config "$WORK/build/.config" >&2 || true
	echo "$0: refusing to build on a prediction that is wrong" >&2
	exit 1
fi

# Every symbol, not only the stated ones. Symbols kbuild fills in from the
# invocation are excluded, and symbols silt knows about because it loads
# every pack while make was given two are skipped: kbuild never saw them.
if ! awk -f "$(dirname "$0")/config-agrees.awk" \
	"$WORK/build/.config" "$WORK.target.config"; then
	echo "$0: silt's prediction does not match kbuild's .config" >&2
	echo "$0: the cache decides prefix reuse from that prediction, so this" >&2
	echo "$0: is not safe to build on" >&2
	exit 1
fi

start=$(date +%s)
make -C "$BUILDROOT" O="$WORK/build" -j"$JOBS"
took=$(( $(date +%s) - start ))

if ! mapfile -t built < <(artifacts_in "$WORK/build/images"); then
	echo "$0: the build finished and left nothing this script recognises in" >&2
	echo "  $WORK/build/images" >&2
	ls -la "$WORK/build/images" >&2
	echo "  add the file this board produces to artifacts_in()" >&2
	exit 1
fi

mkdir -p "$slot"
for a in "${built[@]}"; do
	cp -L "$WORK/build/images/$a" "$slot/$a"
done
printf '%s\n' "${built[@]}" > "$slot/artifacts"
cp "$WORK/defconfig" "$slot/defconfig"
cp "$WORK.target.config" "$slot/predicted.config"

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
	echo "artifacts ${built[*]}"
	echo "size   $(du -ch "${built[@]/#/$slot/}" | tail -1 | cut -f1)"
} > "$slot/manifest"

mkdir -p "$OUT/build/images"
for a in "${built[@]}"; do
	cp -L "$WORK/build/images/$a" "$OUT/build/images/$a"
done
cp "$WORK/defconfig" "$OUT/defconfig"

echo "store  $name  $hash  (${took}s)"
printf '       %s\n' "${built[@]/#/$OUT/build/images/}"
