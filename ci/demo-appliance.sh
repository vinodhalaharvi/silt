#!/usr/bin/env bash
#
# demo-appliance.sh — run the deployment procedure and record it.
#
# Not a shortcut of the procedure: the procedure. A buyer boots the appliance,
# reads its public key off the console because there is no way to log in,
# writes a peer onto the state partition, boots again, and connects. This does
# exactly that, in that order, and captures each step.
#
# It asserts as well as records. A beautiful transcript of a broken appliance
# is worse than no transcript, so every step that can fail exits non-zero when
# it does.
#
#   Usage: ci/demo-appliance.sh OUTDIR [--keep] [--mcp]
#
#     OUTDIR   a Buildroot output directory holding build/images/{Image,rootfs.ext2}
#     --keep   leave the state disk in place (default: start from blank, which
#              is what a buyer's first boot looks like)
#     --mcp    also exercise the agent gateway over the tunnel, for an image
#              built from images/qemu-arm-agent-vpn.sx
#
# Requirements on this machine: qemu-system-aarch64, expect, wireguard-tools,
# and sudo for mount(8) and for the local end of the tunnel. asciinema if you
# want a replayable cast as well as the plain transcript. --mcp also needs
# curl and go, for the fake PLC the appliance reads.
#
set -euo pipefail

out=${1:?usage: ci/demo-appliance.sh OUTDIR [--keep] [--mcp]}
shift
keep=0
mcp=0
for arg in "$@"; do
	case $arg in
	--keep) keep=1 ;;
	--mcp)  mcp=1 ;;
	*) echo "unknown argument: $arg" >&2; exit 2 ;;
	esac
done

out=$(cd "$out" && pwd)
images="$out/build/images"
state="$out/state.img"
ev="$out/evidence"

# The appliance's own address on the tunnel, and ours. Both come from the
# pack: /etc/default is not involved, the provisioning script has them.
APPLIANCE_IP=10.9.0.1
LOCAL_IP=10.9.0.2
PORT=51820

say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
need() { command -v "$1" >/dev/null || { echo "missing: $1" >&2; exit 1; }; }

need qemu-system-aarch64
need expect
need wg
if [[ $mcp -eq 1 ]]; then
	need curl
	need go
fi
[[ -f $images/Image && -f $images/rootfs.ext2 ]] || {
	echo "no image in $images — build it first" >&2; exit 1; }

mkdir -p "$ev"

# The kernel enumerates the two virtio disks in the reverse of the order they
# are given, so the root filesystem lands on vdb. Four boots panicked before
# that was believed; the comment is here so a fifth does not.
qemu_cmd() {
	echo qemu-system-aarch64 -M virt -cpu cortex-a53 -nographic -smp 4 -m 2048 \
		-kernel "$images/Image" \
		-append "'rootwait root=/dev/vdb console=ttyAMA0'" \
		-netdev "user,id=eth0,hostfwd=udp::$PORT-:$PORT" \
		-device virtio-net-device,netdev=eth0 \
		-drive "file=$images/rootfs.ext2,if=none,format=raw,id=hd0" \
		-device virtio-blk-device,drive=hd0 \
		-drive "file=$state,if=none,format=raw,id=hd1" \
		-device virtio-blk-device,drive=hd1
}

# Boot until the provisioning service has had its say, then power off. The
# appliance has no login, so there is nothing to type: the script waits for
# the banner the provisioning script prints and then kills the machine.
boot_and_capture() {
	local logfile=$1 marker=$2 timeout=${3:-180}
	local script; script=$(mktemp)
	cat > "$script" <<EOF
set timeout $timeout
log_file -noappend $logfile
spawn -noecho sh -c {$(qemu_cmd)}
expect {
  "$marker" { }
  timeout   { send_user "\nTIMEOUT waiting for: $marker\n"; exit 1 }
}
# A few seconds more, so anything printed just after the marker lands in the
# log rather than being cut off by the kill below.
sleep 5
EOF
	if [[ -n ${WITH_CAST:-} ]] && command -v asciinema >/dev/null; then
		asciinema rec --overwrite -c "expect -f $script" "$ev/$(basename "${logfile%.txt}").cast" || true
	else
		expect -f "$script" || { rm -f "$script"; return 1; }
	fi
	rm -f "$script"
	pkill -f "qemu-system-aarch64.*$images/Image" 2>/dev/null || true
	sleep 2
}

# ---------------------------------------------------- 1. a blank appliance

say "1. first boot, blank state disk"

if [[ $keep -eq 0 || ! -f $state ]]; then
	rm -f "$state"
	qemu-img create -f raw "$state" 64M >/dev/null
	echo "created a blank 64MB state disk, which is what a buyer starts with"
fi

boot_and_capture "$ev/boot-1.txt" "WireGuard appliance ready" || {
	echo "the appliance did not finish provisioning; see $ev/boot-1.txt" >&2
	exit 1
}

# What a buyer does next: read the key off the console, because there is no
# other way to get it out of a machine with no login.
pubkey=$(grep -o 'public key: .*' "$ev/boot-1.txt" | tail -1 | sed 's/public key: //' | tr -d '\r ')
[[ -n $pubkey ]] || { echo "no public key in the first boot's output" >&2; exit 1; }
echo "appliance public key: $pubkey"

grep -q "generating a keypair" "$ev/boot-1.txt" ||
	{ echo "expected a first boot to generate a keypair" >&2; exit 1; }
grep -q "no peers configured" "$ev/boot-1.txt" ||
	{ echo "expected a first boot to have no peers" >&2; exit 1; }

# ------------------------------------------------------- 2. add a peer

say "2. writing a peer onto the state partition"

# Our end's identity. The private half never leaves this machine, and the
# appliance is only ever told the public half — which is the same shape as
# the appliance's own key never leaving it.
wg genkey > "$ev/local-private.key"
chmod 600 "$ev/local-private.key"
local_pub=$(wg pubkey < "$ev/local-private.key")
echo "local public key: $local_pub"

mnt=$(mktemp -d)
sudo mount -o loop "$state" "$mnt"
sudo tee "$mnt/wireguard/peers.conf" > /dev/null <<EOF
[Peer]
PublicKey = $local_pub
AllowedIPs = $LOCAL_IP/32
EOF
sudo cp "$mnt/wireguard/peers.conf" "$ev/peers.conf"
sudo umount "$mnt"
rmdir "$mnt"
cat "$ev/peers.conf"

# ------------------------------------------------- 3. boot with the peer

say "3. second boot, with the peer in place"

# The state disk is kept this time: the point of the second boot is that the
# appliance is the same peer it was, and that it has taken the configuration.
boot_started=0
script=$(mktemp)
cat > "$script" <<EOF
set timeout 180
log_file -noappend $ev/boot-2.txt
spawn -noecho sh -c {$(qemu_cmd)}
expect {
  "WireGuard appliance ready" { }
  timeout { send_user "\nTIMEOUT\n"; exit 1 }
}
# Leave it running: the tunnel test below needs a live appliance.
expect timeout
EOF
expect -f "$script" > /dev/null 2>&1 &
qemu_pid=$!
boot_started=1
rm -f "$script"

cleanup() {
	[[ ${plc_started:-0} -eq 1 ]] && kill "${plc_pid:-0}" 2>/dev/null
	[[ $boot_started -eq 1 ]] || return 0
	sudo ip link del wg-demo 2>/dev/null || true
	pkill -f "qemu-system-aarch64.*$images/Image" 2>/dev/null || true
	kill "$qemu_pid" 2>/dev/null || true
}
trap cleanup EXIT

# Wait for the banner to appear in the log rather than sleeping a fixed time.
for _ in $(seq 60); do
	grep -q "WireGuard appliance ready" "$ev/boot-2.txt" 2>/dev/null && break
	sleep 2
done
grep -q "applying peers" "$ev/boot-2.txt" ||
	{ echo "the appliance did not take the peer; see $ev/boot-2.txt" >&2; exit 1; }

pubkey2=$(grep -o 'public key: .*' "$ev/boot-2.txt" | tail -1 | sed 's/public key: //' | tr -d '\r ')
[[ $pubkey2 == "$pubkey" ]] ||
	{ echo "the appliance changed identity across a reboot: $pubkey -> $pubkey2" >&2; exit 1; }
echo "same public key across a reboot: $pubkey"

grep -q "generating a keypair" "$ev/boot-2.txt" &&
	{ echo "the appliance regenerated its key; the state disk is not persisting" >&2; exit 1; }

# ------------------------------------------------------- 4. the tunnel

say "4. bringing up this end and sending traffic"

sudo ip link del wg-demo 2>/dev/null || true
sudo ip link add wg-demo type wireguard
sudo ip addr add "$LOCAL_IP/24" dev wg-demo
sudo wg set wg-demo private-key "$ev/local-private.key" \
	peer "$pubkey" \
	allowed-ips "$APPLIANCE_IP/32" \
	endpoint "127.0.0.1:$PORT" \
	persistent-keepalive 25
sudo ip link set wg-demo up

# Give the handshake a moment; it happens on the first packet.
sleep 2
ping -c3 -W2 "$APPLIANCE_IP" | tee "$ev/ping.txt" ||
	{ echo "no traffic through the tunnel" >&2; exit 1; }

sudo wg show wg-demo | tee "$ev/wg-show.txt"
grep -q "latest handshake" "$ev/wg-show.txt" ||
	{ echo "no handshake: the tunnel did not come up" >&2; exit 1; }

# ------------------------------------------------- 4b. the agent gateway

if [[ $mcp -eq 1 ]]; then
	say "4b. an agent reading the plant, over the tunnel"

	# The equipment, outside the appliance as equipment is. The guest
	# reaches this machine at 10.0.2.2 on QEMU's user network, which is
	# what /etc/default/agent-gateway names, so nothing is forwarded in.
	go run "$(dirname "$0")/../tools/fake-plc" -addr 0.0.0.0:15020 \
		> "$ev/fake-plc.txt" 2>&1 &
	plc_pid=$!
	plc_started=1
	sleep 1

	mcp_call() { # id, method, params-json
		curl -s --max-time 10 -X POST "http://$APPLIANCE_IP:8080/mcp" \
			-H 'Content-Type: application/json' \
			-d "{\"jsonrpc\":\"2.0\",\"id\":$1,\"method\":\"$2\",\"params\":$3}"
	}

	# The gateway serves only on the tunnel, so reaching it at all is the
	# first assertion: nothing on any other interface can.
	mcp_call 1 tools/list '{}' | tee "$ev/mcp-tools.json"
	echo
	grep -q read_holding_registers "$ev/mcp-tools.json" ||
		{ echo "the gateway offered no tools; see $ev/mcp-tools.json" >&2; exit 1; }

	mcp_call 2 tools/call \
		'{"name":"read_holding_registers","arguments":{"unit":1,"address":40001,"count":5}}' \
		| tee "$ev/mcp-read.json"
	echo
	grep -q '40001,40002,40003,40004,40005' "$ev/mcp-read.json" ||
		{ echo "the read did not reach the device; see $ev/mcp-read.json" >&2; exit 1; }

	# The point of the product: there is no write tool, because the
	# component imports no interface that could write, which silt check
	# verified against the binary before this image was built.
	mcp_call 3 tools/call \
		'{"name":"write_holding_register","arguments":{"unit":1,"address":40001,"value":0}}' \
		| tee "$ev/mcp-refusal.json"
	echo
	grep -q 'no such tool' "$ev/mcp-refusal.json" ||
		{ echo "a write was not refused; see $ev/mcp-refusal.json" >&2; exit 1; }

	echo "read over the tunnel, write refused by the component"
fi

# ------------------------------------------------------ 5. what is inside

say "5. what the image contains"

# Read from the built filesystem rather than from the running appliance,
# because there is no way into the running appliance — which is the property
# being demonstrated. It is the same filesystem the buyer boots.
target="$out/build/target"
if [[ -d $target ]]; then
	( cd "$target" && ls usr/bin usr/sbin bin sbin 2>/dev/null ) > "$ev/binaries.txt"
	echo "$(grep -c . "$ev/binaries.txt") entries in bin and sbin"
	for forbidden in sshd dropbear gcc python perl; do
		if grep -qx "$forbidden" "$ev/binaries.txt"; then
			echo "FOUND $forbidden in the image, which the appliance claims not to carry" >&2
			exit 1
		fi
	done
	echo "no sshd, dropbear, gcc, python or perl"
fi

say "done"
echo "evidence in $ev:"
ls -1 "$ev"
echo
echo "appliance public key: $pubkey"
