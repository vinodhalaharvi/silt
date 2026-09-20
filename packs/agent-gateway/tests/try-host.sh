#!/bin/sh
# Drive the host over MCP, against a fake Modbus device, and fail if any
# answer changes.
#
# This is the whole chain in one command: an MCP request arrives over HTTP,
# the host calls the gateway component, the component calls silt:modbus/read,
# the host puts Modbus TCP on the wire, and the answer comes back. Every
# refusal on the way is pinned too, because which layer refuses is the part
# that matters: the component refuses a tool it does not have, the device
# refuses a unit it does not serve, and the component refuses a count the
# protocol cannot carry.
#
# Needs python3, curl and a Rust toolchain. Nothing here needs the appliance.
set -eu
cd "$(dirname "$0")"

port=18080
plc_port=15020
tmp=$(mktemp -d)
gw=../br2-external/files/opt/components/gateway.wasm
[ -f "$gw" ] || { echo "no gateway component: run components/gateway/build.sh first" >&2; exit 2; }

cleanup() {
	[ -n "${host_pid:-}" ] && kill "$host_pid" 2>/dev/null || true
	[ -n "${plc_pid:-}" ] && kill "$plc_pid" 2>/dev/null || true
	rm -rf "$tmp"
}
trap cleanup EXIT

# Debug, not release: this tests what the host does rather than how fast it
# does it, and a release build of a wasmtime-embedding binary is minutes.
(cd ../host && cargo build --locked)

python3 fake-plc.py "$plc_port" > "$tmp/plc.log" 2>&1 &
plc_pid=$!

COMPONENT="$gw" LISTEN="127.0.0.1:$port" MODBUS_DEVICE="127.0.0.1:$plc_port" \
	AUDIT_LOG="$tmp/audit.log" ../host/target/debug/agent-gateway-host > "$tmp/host.log" 2>&1 &
host_pid=$!

# Wait for the listener rather than sleeping a guessed interval.
i=0
while ! curl -s --max-time 1 -o /dev/null "http://127.0.0.1:$port/mcp" \
	-X POST -d '{"jsonrpc":"2.0","id":0,"method":"ping"}'; do
	i=$((i + 1))
	[ "$i" -gt 50 ] && { echo "host did not start:"; cat "$tmp/host.log"; exit 1; }
	sleep 0.2
done

fail=0
expect() { # description, request, text the answer must contain
	got=$(curl -s --max-time 10 -X POST "http://127.0.0.1:$port/mcp" \
		-H 'Content-Type: application/json' -d "$2")
	case "$got" in
	*"$3"*) printf 'ok    %s\n' "$1" ;;
	*) printf 'FAIL  %s\n      got: %s\n      want: %s\n' "$1" "$got" "$3"; fail=1 ;;
	esac
}

expect "initialize reports the protocol version" \
	'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
	'"protocolVersion":"2025-06-18"'
expect "tools/list offers one read-only tool" \
	'{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
	'this gateway cannot write'
expect "a read reaches the device and returns its registers" \
	'{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_holding_registers","arguments":{"unit":1,"address":40001,"count":3}}}' \
	'[40001,40002,40003]'
expect "the component refuses a write tool" \
	'{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"write_holding_register","arguments":{"unit":1,"address":1,"value":0}}}' \
	'no such tool: write_holding_register'
expect "the refusal from the device itself is passed back" \
	'{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"read_holding_registers","arguments":{"unit":9,"address":1,"count":1}}}' \
	'device refused: exception 11'
expect "a count the protocol cannot carry is refused" \
	'{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"read_holding_registers","arguments":{"unit":1,"address":1,"count":500}}}' \
	'count must be 1..=125'
expect "an unimplemented method is a JSON-RPC error" \
	'{"jsonrpc":"2.0","id":7,"method":"resources/list"}' \
	'no such method: resources/list'

# Every tool call is in the log, refusals included, and the chain holds.
# Four, not seven: initialize and tools/list ask the gateway about itself
# and touch no equipment, so they are not what the log is for.
python3 - "$tmp/audit.log" <<'PY' || fail=1
import hashlib, json, sys

prev, n = "genesis", 0
for line in open(sys.argv[1]):
    line = line.rstrip("\n")
    if not line.strip():
        continue
    n += 1
    record = json.loads(line)
    if record["prev"] != prev:
        print(f"FAIL  audit chain broken at record {n}")
        raise SystemExit(1)
    prev = hashlib.sha256(line.encode()).hexdigest()
if n != 4:
    print(f"FAIL  audit has {n} records, want 4: every tool call, refusals included")
    raise SystemExit(1)
print(f"ok    {n} audit records, chain intact")
PY

exit $fail
