#!/usr/bin/env bash
# Build a Silt image with Buildroot and boot it under QEMU.
#
# Everything else in CI checks configuration: that a symbol exists, that kbuild
# keeps it, that the two halves agree. None of it can catch a setting that is
# real, correctly spelled, passes every check and does nothing —
# BR2_TARGET_UBOOT_BOARDNAME under the Kconfig build system was exactly that.
# Only a boot can. So this exists, and it is deliberately small: two images, run
# on demand, not a gate on every push.
#
# Usage: ci/boot-test.sh IMAGE [-o DIR] [--buildroot DIR] [--pack DIR]
#                              [--timeout SECONDS] [--no-build] [--dry-run]
#                              [--no-login] [--scan ADDRESS]
#
# --no-login  the image has no getty; do not wait for a login prompt
# --scan      run nmap against the guest from here and keep the output, which
#             is the evidence a reader of the release actually wants
#
# Every pack under packs/ is loaded by default; --pack adds others.
#
# The QEMU command is not invented here: it is taken from the readme.txt of the
# board Buildroot itself ships, so it stays matched to the release in use.
set -euo pipefail

image=""
out="$PWD/out-boot"
br="${BUILDROOT:-$HOME/buildroot}"
packs=()
for d in packs/*/; do [[ -f $d/silt.sx ]] && packs+=(--pack "${d%/}"); done
timeout_s=600
build=1
dry=0
silt="${SILT:-./bin/silt}"

while [[ $# -gt 0 ]]; do
	case "$1" in
	-o) out=$2; shift 2 ;;
	--buildroot) br=$2; shift 2 ;;
	--pack) packs+=(--pack "$2"); shift 2 ;;
	--scan) scan_host=$2; shift 2 ;;
	--no-login) no_login=1; shift ;;
	--timeout) timeout_s=$2; shift 2 ;;
	--no-build) build=0; shift ;;
	--dry-run) dry=1; shift ;;
	-*) echo "unknown option $1" >&2; exit 2 ;;
	*) image=$1; shift ;;
	esac
done
[[ -n $image ]] || { echo "usage: ci/boot-test.sh IMAGE [options]" >&2; exit 2; }

name=$(basename "$image" .sx)
# Assertions live with whatever they are about: an image's own in ci/boot, a
# pack's with the pack, because a pack that ships a feature should ship the
# evidence that the feature works.
expect="ci/boot/$name.expect"
if [[ ! -f $expect ]]; then
	expect=$(ls packs/*/tests/"$name".expect 2>/dev/null | head -1 || true)
fi
[[ -f $expect ]] || { echo "no assertions for $name: looked in ci/boot and packs/*/tests" >&2; exit 2; }
mkdir -p "$out"

run() {
	echo "+ $*"
	[[ $dry -eq 1 ]] || "$@"
}

# 1. Compose and emit. The solution hash goes into the defconfig, so what is
#    booted can be traced back to the fragments it came from.
run "$silt" emit "$image" -o "$out" --buildroot "$br" "${packs[@]}"
hash=$(grep -m1 '^# silt-solution:' "$out/defconfig" 2>/dev/null | awk '{print $3}' || true)
echo "solution ${hash:-unknown}"

# 2. Build. O= keeps the Buildroot checkout clean, and BR2_EXTERNAL supplies
#    Silt's own packages without touching it either.
if [[ $build -eq 1 ]]; then
	# One BR2_EXTERNAL for every pack, space-separated, as Buildroot takes it.
	ext=""
	for i in "${!packs[@]}"; do
		[[ ${packs[i]} == --pack ]] || continue
		d="$PWD/${packs[i+1]}/br2-external"
		[[ -d $d ]] && ext="${ext:+$ext }$d"
	done
	run make -C "$br" "O=$out/build" "BR2_EXTERNAL=$ext" \
		defconfig "BR2_DEFCONFIG=$out/defconfig"
	run make -C "$br" "O=$out/build"
fi

# 3. Boot. The invocation comes from the board's own readme; only the paths are
#    rewritten, because the readme assumes output/ inside the checkout.
readme="$br/board/qemu/aarch64-virt/readme.txt"
[[ -f $readme ]] || { echo "no board readme at $readme" >&2; exit 2; }
qemu=$(grep -o 'qemu-system-[^#]*' "$readme" | head -1 | sed "s#output/images#$out/build/images#g")
# An image may need more of the machine than the board's readme assumes: k3s
# wants 2GB and two cores, and the readme is written for the smallest system
# that boots. The extras live next to the assertions.
extra="${expect%.expect}.qemu"
[[ -f $extra ]] && qemu="$qemu $(tr -d '\n' < "$extra")"
[[ -n $qemu ]] || { echo "no qemu command found in $readme" >&2; exit 2; }
echo "+ $qemu"
[[ $dry -eq 1 ]] && exit 0

# The scan runs from here, against the guest, because the useful question is
# what an attacker sees rather than what the appliance believes it is
# offering. It needs the guest reachable, so it is skipped unless --scan
# names an address; with QEMU user networking the guest is behind a NAT and
# there is nothing to scan from outside.
if [[ -n ${scan_host:-} ]]; then
	command -v nmap >/dev/null || { echo "nmap not installed; skipping scan" >&2; scan_host=""; }
fi

# 4. Drive the serial console: wait for the login prompt, log in, run each
#    assertion, and end with a line that says the run finished, so a hang is
#    told apart from a failure.
script=$(mktemp) ; trap 'rm -f "$script"' EXIT
{
	echo 'set timeout '"$timeout_s"
	echo 'spawn -noecho sh -c {'"$qemu"'}'
	if [[ ${no_login:-0} -eq 1 ]]; then
		# An image with no getty has no login prompt to wait for. The
		# assertions still run, through the console systemd leaves on the
		# serial port for emergency use; an image that denies even that is
		# one whose evidence has to come from mounting it offline.
		echo 'expect "Welcome to Buildroot" { send "\r" }'
	else
		echo 'expect "buildroot login:" { send "root\r" }'
	fi
	echo 'expect "# "'
	while IFS=$'\t' read -r cmd want; do
		[[ -z $cmd || $cmd == \#* ]] && continue
		printf 'send %s\n' "{$cmd\r}"
		printf 'expect {\n  %s {}\n  timeout { send_user "\\nFAIL: %s\\n"; exit 1 }\n}\n' \
			"{$want}" "$cmd"
		echo 'expect "# "'
	done < "$expect"
	echo 'send "poweroff\r"'
	echo 'send_user "\nALL ASSERTIONS PASSED\n"'
	echo 'expect eof'
} > "$script"

if ! command -v expect >/dev/null; then
	echo "expect(1) is needed to drive the console; install it or run the QEMU command by hand:" >&2
	echo "  $qemu" >&2
	exit 2
fi
expect -f "$script" | tee "$out/boot.log"
grep -q "ALL ASSERTIONS PASSED" "$out/boot.log"

# The external view, recorded beside the internal one. Both go in the
# release: a port list from outside is the evidence a reader cares about,
# and it is worth more than any adjective in a README.
if [[ -n ${scan_host:-} ]]; then
	echo "+ nmap -Pn -sV -p- $scan_host"
	nmap -Pn -sV -p- "$scan_host" | tee "$out/scan-tcp.txt"
	echo "+ nmap -Pn -sU --top-ports 200 $scan_host"
	sudo nmap -Pn -sU --top-ports 200 "$scan_host" | tee "$out/scan-udp.txt"
fi
echo "ok $name booted and passed $(grep -cve '^#' -e '^$' "$expect") assertions"
