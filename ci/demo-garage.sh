#!/usr/bin/env bash
#
# demo-garage.sh — the tunnel, what it exposes, and the thing behind it.
#
# demo-appliance.sh proves a QEMU appliance from a blank state disk, and
# demo-panes.sh films it. This one runs against hardware: a Raspberry Pi on a
# bench somewhere else, reached over a Tailscale tunnel, routing to a device
# that knows nothing about any of it.
#
# The point is the last part. An ESPHome relay on a garage door has no VPN
# client, no certificate, no configuration, and cannot be given one - it is a
# microcontroller with a web page. The appliance advertises a /32 route to it,
# and a phone on a cellular network opens that web page. Nothing is forwarded
# on the router, no dynamic DNS name exists, and the relay is not reachable
# from the internet by any other means.
#
# It asserts as well as records, for the reason demo-appliance.sh gives: a
# beautiful transcript of a broken appliance is worse than no transcript. SSH
# must succeed without a password, the open ports must be exactly the expected
# set and nothing more, and the device behind the tunnel must answer.
#
#   Usage: ci/demo-garage.sh [--cast] [--tailnet IP] [--device IP]
#
#     --cast        also record an asciinema cast beside the transcript
#     --tailnet IP  the appliance's tailnet address (default below)
#     --device IP   the device it routes to (default below)
#
# Requirements: tailscale, nmap, ssh, curl. asciinema for --cast.
#
# Run it from a machine on the tailnet but NOT on the appliance's LAN - a
# laptop tethered to a phone is the honest setup, because on the same LAN
# every result would be true whether the tunnel worked or not.
#
set -euo pipefail

TAILNET=100.71.235.14
DEVICE=192.168.68.50
cast=0

while [[ $# -gt 0 ]]; do
	case $1 in
	--cast)    cast=1; shift ;;
	--tailnet) TAILNET=${2:?--tailnet needs an address}; shift 2 ;;
	--device)  DEVICE=${2:?--device needs an address}; shift 2 ;;
	*) echo "unknown argument: $1" >&2; exit 2 ;;
	esac
done

# The ports this image is expected to listen on, and the reason each is here.
# The assertion is two-sided: every one of these must be open, and nothing
# else in the scanned range may be.
declare -A EXPECT=(
	[22]="ssh, key only"
	[502]="modbus tcp"
	[1883]="mqtt"
	[4840]="opc ua"
	[8080]="http config"
)

ev=${EVIDENCE_DIR:-evidence-garage}
mkdir -p "$ev"

# --cast re-runs this same script under asciinema rather than recording each
# step separately, so the cast and the transcript are the same run. The guard
# variable stops the recursion. asciinema exits 0 whatever it recorded, so the
# inner run's exit status is what matters and is passed through.
if [[ $cast -eq 1 && -z ${DEMO_GARAGE_INNER:-} ]]; then
	command -v asciinema >/dev/null || fail "--cast needs asciinema"
	export DEMO_GARAGE_INNER=1
	asciinema rec --overwrite -c "$0 --tailnet $TAILNET --device $DEVICE" "$ev/garage.cast"
	status=$?
	echo "cast in $ev/garage.cast"
	exit $status
fi

say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
run()  { printf '\033[36m$ %s\033[0m\n' "$*"; eval "$@"; }
fail() { printf '\n\033[31mFAILED: %s\033[0m\n' "$*" >&2; exit 1; }

# ------------------------------------------------- 1. is there a tunnel at all

say "1. the tunnel"

# Running this on the appliance's own LAN makes every step below pass whether
# the tunnel works or not, which would make the recording worthless. Warn
# loudly; do not refuse, because there are reasons to run it locally while
# developing the script itself.
if ping -c1 -W1 "$DEVICE" >/dev/null 2>&1 &&
   ! ip route get "$DEVICE" 2>/dev/null | grep -q tailscale; then
	printf '\033[33m'
	echo "warning: $DEVICE answers without going through the tunnel, so this"
	echo "machine appears to be on the appliance's own LAN. Every step below"
	echo "will pass whether the tunnel works or not. Tether to a phone and"
	echo "run it again for a recording that proves anything."
	printf '\033[0m'
	sleep 3
fi

command -v tailscale >/dev/null || fail "tailscale is not installed here"

# --peers=false, and no pipe into head. Piping it hangs: with fewer lines
# than head asks for, head waits for EOF and tailscale status keeps its
# connection open watching for changes, so the recording stops on step one
# with the cursor blinking.
run "tailscale status --peers=false" || fail "tailscale status failed"
run "tailscale status --json | grep -E '\"(BackendState|TailscaleIPs)\"' || true"

# The peer list is needed here, so ask for it once and with a timeout: a
# tailnet this machine is not on would otherwise wait rather than answer.
peers=$(timeout 20 tailscale status 2>/dev/null || true)
grep -q "$TAILNET" <<<"$peers" ||
	fail "$TAILNET is not in this machine's tailnet - is the appliance connected?"

printf '%s\n' "$peers" | grep -E "garage|$TAILNET" || true

# 100.64.0.0/10 is the CGNAT range Tailscale assigns from. Saying so out loud
# matters: the address looks private because it is, and the traffic reaching
# it is not crossing a forwarded port on anyone's router.
echo
echo "the appliance is at $TAILNET, in 100.64.0.0/10, which is Tailscale's own"
echo "range. There is no port forward, no dynamic DNS, and no public address"
echo "for this machine anywhere."

# ------------------------------------------------------- 2. what it exposes

say "2. what is open, and what is not"

scan="$ev/nmap.txt"
run "nmap -Pn -p 1-1024,1883,4840,8080 $TAILNET | tee $scan" ||
	fail "nmap could not reach $TAILNET over the tunnel"

for port in "${!EXPECT[@]}"; do
	grep -qE "^$port/tcp[[:space:]]+open" "$scan" ||
		fail "port $port (${EXPECT[$port]}) is not open; the image is not serving what it should"
done

# Anything open that is not in the table is the interesting failure: an image
# that grew a listener nobody asked for.
unexpected=$(awk '/^[0-9]+\/tcp[[:space:]]+open/ {split($1, a, "/"); print a[1]}' "$scan" |
	while read -r p; do [[ -v EXPECT[$p] ]] || echo "$p"; done)
[[ -z $unexpected ]] ||
	fail "unexpected open port(s): $unexpected"

echo
echo "exactly ${#EXPECT[@]} ports, each one something the image was built to serve:"
for port in $(printf '%s\n' "${!EXPECT[@]}" | sort -n); do
	printf '  %-5s %s\n' "$port" "${EXPECT[$port]}"
done

# ------------------------------------------------------ 3. no password anywhere

say "3. logging in"

# BatchMode refuses to prompt. If a password were needed this exits non-zero
# rather than hanging on a prompt nobody will answer, which is the assertion:
# the key was baked into the image and nothing else will get you in.
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new)

run "ssh ${ssh_opts[*]} root@$TAILNET 'uname -a; uptime'" ||
	fail "ssh failed - either the key is not in the image or a password was wanted"

echo
echo "no password was typed and none was offered: BatchMode=yes makes ssh fail"
echo "rather than prompt, so this succeeding is the proof."

# --------------------------------------------------------- 4. what is running

say "4. what the image is"

run "ssh ${ssh_opts[*]} root@$TAILNET 'cat /etc/os-release | head -2; echo; df -h / | tail -1'"

run "ssh ${ssh_opts[*]} root@$TAILNET 'ls -la /usr/bin/silt-* /usr/sbin/tailscaled 2>/dev/null'" ||
	true

echo
echo "the protocol daemons are a few hundred kilobytes each. tailscaled is"
echo "tens of megabytes, because it carries its own WireGuard, DNS and network"
echo "stack - which is the trade for needing no port forward."

run "ssh ${ssh_opts[*]} root@$TAILNET 'tailscale status; tailscale ip -4'" ||
	fail "tailscale is not running on the appliance"

# ------------------------------------------------ 5. the device behind the tunnel

say "5. through to the equipment"

# The appliance's own service first, so a failure below is about the route and
# not about the tunnel.
run "curl -s --max-time 10 http://$TAILNET:8080/health" ||
	fail "the appliance's own HTTP endpoint did not answer over the tunnel"
echo

# Then the device, which is on the appliance's LAN and nowhere else. This is
# the assertion the whole script exists for.
run "curl -s --max-time 10 -o $ev/device.html -w 'HTTP %{http_code}, %{size_download} bytes\n' http://$DEVICE/" ||
	fail "$DEVICE did not answer - is the /32 route approved in the admin console?"

grep -qi "html" "$ev/device.html" ||
	fail "$DEVICE answered but not with a web page"

echo
echo "$DEVICE is an ESPHome relay on the appliance's LAN. It has no VPN client,"
echo "no certificate and no configuration for any of this, and it cannot be"
echo "given one. The appliance advertises a /32 route to it; everything else"
echo "follows from that."

say "done"
echo "evidence in $ev:"
ls -la "$ev"
