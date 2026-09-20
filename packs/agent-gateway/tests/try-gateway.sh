#!/bin/sh
# Run the committed gateway the way an agent would call it, against a fake
# Modbus device, and fail if any answer changes.
#
# The gateway imports silt:modbus/read and nothing else from the plant. Here
# that import is satisfied by fake-plc.wat, in which register N holds N, so a
# correct read is recognisable on sight. On the appliance it will be
# satisfied by the host's real Modbus adapter; the gateway cannot tell the
# difference, which is the point of the interface.
#
# Needs wasm-tools, wac and wasmtime (brew install wasm-tools wasmtime;
# cargo install wac-cli).
set -eu
cd "$(dirname "$0")"
gw=../br2-external/files/opt/components/gateway.wasm
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

wasm-tools parse fake-plc.wat -o "$tmp/fake-plc.wasm"
wac plug "$gw" --plug "$tmp/fake-plc.wasm" -o "$tmp/agent.wasm"

fail=0
expect() { # description, invocation, text the answer must contain
	got=$(wasmtime run --invoke "$2" "$tmp/agent.wasm" 2>&1 | tail -1)
	case "$got" in
	*"$3"*) printf 'ok    %s\n' "$1" ;;
	*) printf 'FAIL  %s\n      got: %s\n      want: %s\n' "$1" "$got" "$3"; fail=1 ;;
	esac
}

expect "lists exactly one tool, read-only" \
	'list-tools()' 'Read-only: this gateway cannot write'
expect "reads registers 40001-40005" \
	'call-tool("read_holding_registers", "{\"unit\":1,\"address\":40001,\"count\":5}")' \
	'[40001,40002,40003,40004,40005]'
expect "refuses a write tool by name" \
	'call-tool("write_holding_register", "{\"unit\":1,\"address\":40001,\"value\":0}")' \
	'no such tool: write_holding_register'
expect "refuses a count over the Modbus limit" \
	'call-tool("read_holding_registers", "{\"unit\":1,\"address\":0,\"count\":200}")' \
	'count must be 1..=125'
expect "refuses a read past register 65535" \
	'call-tool("read_holding_registers", "{\"unit\":1,\"address\":65530,\"count\":10}")' \
	'runs past register 65535'
expect "refuses a smuggled extra field" \
	'call-tool("read_holding_registers", "{\"unit\":1,\"address\":0,\"count\":1,\"value\":9}")' \
	'unknown field `value`'
expect "refuses arguments that are not JSON" \
	'call-tool("read_holding_registers", "hello")' \
	'invalid arguments'

exit $fail
