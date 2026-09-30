#!/usr/bin/env bash
#
# ci-build.sh — what CI runs on the builder.
#
# This exists as a file rather than as a string inside build.yml because the
# string version cost four failed runs, each one a twenty-minute round trip to
# learn something a local shell would have said immediately: a tilde that
# expanded to the wrong home, a PATH that a non-interactive shell does not
# have, a Buildroot tree in a directory this account cannot read. Shell inside
# YAML inside an ssh command argument is three levels of quoting and no way to
# test any of it.
#
# So: the runner copies this file to the builder and runs it. It can be run by
# hand, it can be shellchecked, and its failures name what to do.
#
#   ci-build.sh COMMIT [IMAGE...]
#
# With no images, it builds everything the images themselves ask for - the ones
# carrying (ci build). See docs/CI.md.
#
# Everything it needs is in one of three places, none of them a person's home
# directory:
#
#   /srv/silt         the store, group siltbuild, shared with whoever builds
#   /srv/buildroot    the Buildroot tree, cloned here if absent
#   ~/ci              this account's clone and scratch space
#
set -euo pipefail

REPO=${REPO:-https://github.com/vinodhalaharvi/silt.git}
CI_ROOT=${CI_ROOT:-$HOME/ci}
SILT_STORE=${SILT_STORE:-/srv/silt}
BUILDROOT_DIR=${BUILDROOT_DIR:-/srv/buildroot}
BUILDROOT_REF=${BUILDROOT_REF:-2025.02.16}

# A non-interactive ssh reads no profile, so nothing a login shell puts on PATH
# is here. Go is installed system-wide; without this the failure is the terse
# and misleading "make: go: No such file or directory".
export PATH=/usr/local/go/bin:/usr/local/bin:$PATH

commit=${1:-}
if [ -z "$commit" ]; then
	echo "usage: $0 COMMIT [IMAGE...]" >&2
	exit 2
fi
shift
images=("$@")

say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

# --- the tools this needs, named before they are missing -------------------

for tool in git make go; do
	command -v "$tool" >/dev/null ||
		{ echo "$0: no $tool on PATH ($PATH)" >&2; exit 1; }
done

# --- buildroot -------------------------------------------------------------

# Cloned into a shared path rather than borrowed from somebody's home. An
# earlier version pointed at /home/<person>/buildroot and failed with "no
# Buildroot at ...", because a home directory is not readable by another
# account and should not have to be.
say "buildroot $BUILDROOT_REF"
if [ ! -d "$BUILDROOT_DIR/.git" ] && [ ! -f "$BUILDROOT_DIR/Makefile" ]; then
	parent=$(dirname "$BUILDROOT_DIR")
	if [ ! -w "$parent" ]; then
		echo "$0: cannot write $parent to clone Buildroot into $BUILDROOT_DIR" >&2
		echo "  sudo mkdir -p $BUILDROOT_DIR && sudo chown $(id -un):siltbuild $BUILDROOT_DIR" >&2
		exit 1
	fi
	# The GitHub mirror, which is what ci.yml already clones; buildroot.org's
	# own GitLab is the upstream but is slower and is not what the rest of
	# this repository points at.
	git clone --depth 1 --branch "$BUILDROOT_REF" \
		https://github.com/buildroot/buildroot.git "$BUILDROOT_DIR"
else
	echo "already at $BUILDROOT_DIR"
fi

# --- the clone -------------------------------------------------------------

say "silt at $commit"
mkdir -p "$CI_ROOT"
if [ ! -d "$CI_ROOT/silt/.git" ]; then
	git clone --quiet "$REPO" "$CI_ROOT/silt"
fi
cd "$CI_ROOT/silt"
git fetch --all --quiet
git checkout --quiet --detach "$commit"
git log -1 --format='%h %s'
make

# --- what to build ---------------------------------------------------------

export BUILDROOT="$BUILDROOT_DIR"
export SILT_CACHE="$SILT_STORE"
export SILT_WORK="$CI_ROOT/work"
export SILT_LOCK="$CI_ROOT/lock"

if [ ${#images[@]} -eq 0 ]; then
	# Both lists. An image marked (ci boot) is built and then booted; one
	# marked (ci build) is built. Building only the first list is what the
	# language said and not what the workflow did, so an image could carry
	# (ci boot) and never be built at all - a claim with nothing behind it,
	# which is worse than no claim.
	mapfile -t images < <(./bin/silt images --ci build; ./bin/silt images --ci boot)
fi
if [ ${#images[@]} -eq 0 ]; then
	echo "nothing is marked (ci build); nothing to do"
	exit 0
fi

say "building ${#images[@]} image(s)"
printf '  %s\n' "${images[@]}"

# The store is shared, so check it is usable before spending twenty minutes
# discovering it is not.
if [ ! -d "$SILT_CACHE" ]; then
	echo "$0: no store at $SILT_CACHE" >&2
	exit 1
fi
if [ ! -w "$SILT_CACHE" ]; then
	echo "$0: $SILT_CACHE is not writable by $(id -un)" >&2
	echo "  the store is meant to be group siltbuild and setgid; see docs/CI.md" >&2
	exit 1
fi

# Which of these are to be booted, so the loop below knows without asking again.
boots=" $(./bin/silt images --ci boot | tr '\n' ' ') "

for image in "${images[@]}"; do
	echo "::group::$image"
	./ci/silt-build.sh "$image"
	echo "::endgroup::"

	case "$boots" in
	*" $image "*)
		# (ci boot) meant nothing until now: the list was printed and
		# ignored. Everything else in CI checks configuration - that a symbol
		# exists, that kbuild keeps it, that two trees agree - and none of it
		# can catch a setting that is real, correctly spelled, passes every
		# check and does nothing. Nor an init script that blocks every service
		# behind it, which is what an S41 ordering mistake did to a board that
		# answered ping with every port closed.
		#
		# --no-build because silt-build.sh has just built it, into the
		# directory named after the image.
		name=$(basename "$image" .sx)
		echo "::group::boot $name"
		if ! ./ci/boot-test.sh "$image" --no-build \
			-o "$HOME/br-$name" --buildroot "$BUILDROOT"; then
			echo "$0: $name built and did not boot" >&2
			exit 1
		fi
		echo "::endgroup::"
		;;
	esac
done

say "the store"
./ci/silt-build.sh --list
