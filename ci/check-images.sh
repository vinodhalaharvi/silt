#!/usr/bin/env bash
# Check every image against a Buildroot tree, and a Linux tree if given.
#
# This used to run silt check once per image, because a whole-tree check exited
# 1 forever: edge-camera is a deliberately failing fixture and there was no way
# to say so. (expect-problems "...") is that way, and a whole-tree check now
# exits 0 with the fixture reported as "2 expected problem(s)".
#
# Running it per image was not only slower, it was wrong. Naming one file loads
# only that file, so an image composing image:something failed with "no such
# image" - twenty of the fifty-five, every one of them fine when silt is asked
# the way a person asks it. That was the fourth copy of the same mistake in
# this repository: a test or script with its own idea of what the library is.
# There is now one, compose.LibraryPaths, and this script asks silt rather than
# assembling a library itself.
#
# What it still adds over a bare check: a fixture that starts passing is a
# failure. A fixture nobody notices has stopped testing what it was checked in
# to test.
#
# Usage: ci/check-images.sh BUILDROOT_DIR [SILT_BINARY] [LINUX_DIR]
set -euo pipefail

br=${1:?usage: ci/check-images.sh BUILDROOT_DIR [SILT_BINARY] [LINUX_DIR]}
silt=${2:-bin/silt}
linux=${3:-}
lxflag=()
[[ -n $linux ]] && lxflag=(--linux "$linux")

# Images that must report problems, and the reason each is here.
expect_fail=(edge-camera)

set +e
out=$("$silt" check --buildroot "$br" "${lxflag[@]}" 2>&1)
rc=$?
set -e

echo "$out"

status=0
if [[ $rc != 0 ]]; then
	echo
	echo "FAIL  silt check exited $rc"
	status=1
fi

for f in "${expect_fail[@]}"; do
	if ! grep -qE "^ok  +$f .*expected problem" <<<"$out"; then
		echo
		echo "FAIL  $f: expected to report problems and did not."
		echo "      It is checked in as a fixture - something that must keep"
		echo "      failing - and a fixture that passes has stopped testing"
		echo "      what it was written for. Either the conflict it captures"
		echo "      was fixed, in which case delete the fixture and say so,"
		echo "      or something stopped checking."
		status=1
	fi
done

exit $status
