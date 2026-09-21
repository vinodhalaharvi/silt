#!/usr/bin/env bash
#
# demo-panes.sh — the same appliance, arranged so it can be watched.
#
# demo-appliance.sh proves things: it asserts, and it fails loudly. This
# shows them. Four panes, one recording: the appliance booting on the left,
# and on the right the three things a buyer does — watch the tunnel come up,
# watch equipment being read, and ask the gateway for something it will not
# do.
#
#   ┌────────────────────────────┬──────────────────────┐
#   │ the appliance's console    │ the tunnel           │
#   │ no login, no prompt, one   ├──────────────────────┤
#   │ banner and then silence    │ the equipment        │
#   │                            ├──────────────────────┤
#   │                            │ what an agent asks   │
#   └────────────────────────────┴──────────────────────┘
#
# It runs against a state disk that has already been provisioned, because a
# recording of a first boot is demo-appliance.sh's job and doing both here
# would mean a five minute film with nothing happening in the middle:
#
#   ci/demo-appliance.sh OUTDIR --mcp     # once, to provision and prove
#   ci/demo-panes.sh OUTDIR               # then, to record
#
# Requirements: tmux, asciinema, qemu-system-aarch64, wireguard-tools, curl,
# go, and sudo for this end of the tunnel.
#
set -euo pipefail

out=${1:?usage: ci/demo-panes.sh OUTDIR [--keep-session]}
out=$(cd "$out" && pwd)
images="$out/build/images"
state="$out/state.img"
ev="$out/evidence"
here=$(cd "$(dirname "$0")" && pwd)

# The same addresses demo-appliance.sh uses, which are the pack's.
APPLIANCE_IP=10.9.0.1
LOCAL_IP=10.9.0.2
PORT=51820
SESSION=silt-demo
COLS=200
ROWS=50

need() { command -v "$1" >/dev/null || { echo "missing: $1" >&2; exit 1; }; }
for tool in tmux asciinema qemu-system-aarch64 wg curl go expect; do need "$tool"; done

[[ -f $images/Image && -f $images/rootfs.ext2 ]] ||
	{ echo "no image in $images — build it first" >&2; exit 1; }
[[ -f $state ]] ||
	{ echo "no state disk at $state — run ci/demo-appliance.sh $out --mcp first" >&2; exit 1; }
[[ -f $ev/local-private.key ]] ||
	{ echo "no key at $ev/local-private.key — run ci/demo-appliance.sh first" >&2; exit 1; }

# The appliance's public key, from the provisioned state disk rather than
# from a transcript: this script does not boot it first, so there is no
# banner to read it from yet.
mnt=$(mktemp -d)
sudo mount -o loop "$state" "$mnt"
pubkey=$(sudo cat "$mnt/wireguard/public" 2>/dev/null | tr -d '\n\r ')
sudo umount "$mnt"; rmdir "$mnt"
[[ -n $pubkey ]] || { echo "no public key on the state disk" >&2; exit 1; }

cleanup() {
	tmux kill-session -t "$SESSION" 2>/dev/null || true
	sudo ip link del wg-demo 2>/dev/null || true
	pkill -f "qemu-system-aarch64.*$images/Image" 2>/dev/null || true
	kill "${plc_pid:-0}" 2>/dev/null || true
}
trap cleanup EXIT

# ------------------------------------------------------------ the panes

# A launcher file, not a command string. demo-appliance.sh hands its
# command to expect, which protects it with braces; passing the same string
# through sh -c '...' here put single quotes inside single quotes, the
# kernel got an empty root=, and the machine panicked with "Cannot open
# root device". A file has no quoting to get wrong.
launcher="$ev/panes-qemu.sh"
mkdir -p "$ev"
cat > "$launcher" <<LAUNCHER
#!/bin/sh
exec qemu-system-aarch64 -M virt -cpu cortex-a53 -nographic -smp 4 -m 2048 \\
	-kernel "$images/Image" \\
	-append "rootwait root=/dev/vdb console=ttyAMA0" \\
	-netdev "user,id=eth0,hostfwd=udp::$PORT-:$PORT" \\
	-device virtio-net-device,netdev=eth0 \\
	-drive "file=$images/rootfs.ext2,if=none,format=raw,id=hd0" \\
	-device virtio-blk-device,drive=hd0 \\
	-drive "file=$state,if=none,format=raw,id=hd1" \\
	-device virtio-blk-device,drive=hd1
LAUNCHER
chmod +x "$launcher"

# The equipment, outside the appliance as equipment is. Started before the
# panes so the appliance has something to read the moment it is up.
go build -o "$ev/fake-plc" "$here/../tools/fake-plc"
"$ev/fake-plc" -addr 0.0.0.0:15020 > "$ev/panes-plc.log" 2>&1 &
plc_pid=$!
for _ in $(seq 50); do
	(exec 3<>/dev/tcp/127.0.0.1/15020) 2>/dev/null && { exec 3>&-; break; }
	sleep 0.2
done

tmux kill-session -t "$SESSION" 2>/dev/null || true
tmux new-session -d -s "$SESSION" -x "$COLS" -y "$ROWS" "$launcher"
tmux set -t "$SESSION" -g pane-border-status top
tmux set -t "$SESSION" -g pane-border-format ' #{pane_title} '
tmux select-pane -t "$SESSION:0.0" -T 'the appliance: no login, no prompt'

tmux split-window -h -t "$SESSION:0.0" -l "$((COLS * 45 / 100))" \
	"watch -n1 -t sudo wg show wg-demo"
tmux select-pane -t "$SESSION:0.1" -T 'the tunnel'

tmux split-window -v -t "$SESSION:0.1" \
	"tail -f $ev/panes-plc.log"
tmux select-pane -t "$SESSION:0.2" -T 'the equipment (a Modbus device)'

tmux split-window -v -t "$SESSION:0.2" "bash --norc"
tmux select-pane -t "$SESSION:0.3" -T 'what an agent asks'
ASK="$SESSION:0.3"

# ------------------------------------------------------------ the script

# Typed rather than pasted: a command appearing character by character reads
# as someone working, and a wall of text appearing at once reads as a log.
type_line() {
	local pane=$1 line=$2 i
	for ((i = 0; i < ${#line}; i++)); do
		tmux send-keys -t "$pane" -l "${line:i:1}"
		sleep 0.02
	done
	tmux send-keys -t "$pane" Enter
}

say() { tmux send-keys -t "$ASK" -l "" ; type_line "$ASK" "# $1"; }

mcp_line() { # id, method, params — one curl, formatted for reading
	echo "curl -s -X POST http://$APPLIANCE_IP:8080/mcp -H 'Content-Type: application/json' -d '{\"jsonrpc\":\"2.0\",\"id\":$1,\"method\":\"$2\",\"params\":$3}' | jq -c ."
}

drive() {
	# 1. the appliance comes up on its own
	sleep 45

	# 2. this end of the tunnel
	sudo ip link del wg-demo 2>/dev/null || true
	sudo ip link add wg-demo type wireguard
	sudo ip addr add "$LOCAL_IP/24" dev wg-demo
	sudo wg set wg-demo private-key "$ev/local-private.key" \
		peer "$pubkey" allowed-ips "$APPLIANCE_IP/32" \
		endpoint "127.0.0.1:$PORT" persistent-keepalive 25
	sudo ip link set wg-demo up
	sleep 5

	say "the appliance has no login. this is the only way in."
	sleep 2
	type_line "$ASK" "ping -c2 $APPLIANCE_IP"
	sleep 6

	say "what does it offer?"
	type_line "$ASK" "$(mcp_line 1 tools/list '{}')"
	sleep 6

	say "read five holding registers from the device"
	type_line "$ASK" "$(mcp_line 2 tools/call \
		'{\"name\":\"read_holding_registers\",\"arguments\":{\"unit\":1,\"address\":40001,\"count\":5}}')"
	sleep 6

	say "now ask it to write one"
	type_line "$ASK" "$(mcp_line 3 tools/call \
		'{\"name\":\"write_holding_register\",\"arguments\":{\"unit\":1,\"address\":40001,\"value\":0}}')"
	sleep 6

	say "there is no write tool. the component imports no interface that"
	say "could write, and silt check verified that from the binary before"
	say "this image was built."
	sleep 8

	tmux kill-session -t "$SESSION" 2>/dev/null || true
}

drive &

# ------------------------------------------------------------ recording

mkdir -p "$ev"
cast="$ev/demo-panes.cast"

# stty first: without a size the cast records 0x0 and plays back unusably,
# which is what happens under CI where there is no terminal to ask.
stty rows "$ROWS" cols "$COLS" 2>/dev/null || true
asciinema rec --overwrite --title "Silt agent gateway" \
	-c "tmux attach -t $SESSION" "$cast"

echo
echo "recorded to $cast"
echo "play:    asciinema play $cast"
echo "share:   agg $cast ${cast%.cast}.gif    # if agg is installed"
