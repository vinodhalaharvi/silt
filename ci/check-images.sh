#!/usr/bin/env bash
# Check every image against a Buildroot tree, one image at a time.
#
# `silt check --buildroot` over the whole library exits 1 forever, because
# edge-camera is a deliberately failing fixture. Keying CI on that exit code
# would mean CI is always red, which is the same as having no CI. So images are
# checked individually, and the fixtures are listed here: each must fail, and a
# fixture that starts passing fails the run too, since it has stopped testing
# what it was checked in to test.
#
# Usage: ci/check-images.sh BUILDROOT_DIR [SILT_BINARY] [LINUX_DIR]
set -euo pipefail

br=${1:?usage: ci/check-images.sh BUILDROOT_DIR [SILT_BINARY]}
silt=${2:-bin/silt}
linux=${3:-}
lxflag=()
[[ -n $linux ]] && lxflag=(--linux "$linux")

expect_fail=(edge-camera)

status=0
for img in images/*.sx; do
	name=$(basename "$img" .sx)
	want=pass
	for f in "${expect_fail[@]}"; do
		[[ $name == "$f" ]] && want=fail
	done

	set +e
	out=$("$silt" check --buildroot "$br" "${lxflag[@]}" fragments "$img" 2>&1)
	rc=$?
	set -e

	# Exit 1 also covers a parse or compose error, so a fixture must fail in
	# the verifier specifically. One with a typo would otherwise pass forever.
	if [[ $want == fail && $rc == 1 ]] && ! grep -q 'problem(s), buildroot' <<<"$out"; then
		rc=wrong
	fi

	case "$want:$rc" in
	pass:0) echo "ok    $name" ;;
	fail:1) echo "ok    $name (fails in the verifier, as a fixture should)" ;;
	pass:*)
		echo "FAIL  $name: expected to check clean, exit $rc"
		echo "$out" | sed 's/^/      /'
		status=1
		;;
	fail:0)
		echo "FAIL  $name: fixture now checks clean; it no longer tests anything"
		status=1
		;;
	fail:*)
		echo "FAIL  $name: fixture failed, but not in the verifier"
		echo "$out" | sed 's/^/      /'
		status=1
		;;
	esac
done
exit $status
