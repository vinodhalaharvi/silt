#!/bin/sh
# Build the component and put it where the image carries it from.
#
# Run on the development machine (Rust with the wasm32-wasip2 target); the
# built binary is committed, so the build VM never needs Rust. The last line
# printed is the SHA-256 silt hash --solution will show.
set -eu
cd "$(dirname "$0")"

cargo build --target wasm32-wasip2 --release --locked

out=../../br2-external/files/opt/components/hello.wasm
mkdir -p "$(dirname "$out")"
cp target/wasm32-wasip2/release/hello.wasm "$out"

wasm-tools validate "$out"
echo "--- what this component can reach:"
wasm-tools component wit "$out" | sed -n '/^world/,/^}/p'
echo "--- sha256:"
shasum -a 256 "$out"
