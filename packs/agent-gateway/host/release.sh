#!/bin/sh
# Cross-compile the host for the appliance, pack it, and write the hash
# Buildroot checks it against.
#
# The binary is not committed: it is tens of megabytes and changes with
# every edit. What is committed is the hash, which makes a fetched binary
# exactly as pinned as a carried one — the same bargain the k3s and
# wasmtime packs already make.
#
# Needs zig and cargo-zigbuild (brew install zig; cargo install cargo-zigbuild).
# musl and static, so the appliance runs one file that carries no assumptions
# about the image's C library.
set -eu
cd "$(dirname "$0")"

version=$(sed -n 's/^version = "\(.*\)"/\1/p' Cargo.toml | head -1)
target=aarch64-unknown-linux-musl
tarball="agent-gateway-host-$version-aarch64.tar.xz"
hashfile=../br2-external/package/agent-gateway-host/agent-gateway-host.hash

cargo zigbuild --target "$target" --release --locked

staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
cp "target/$target/release/agent-gateway-host" "$staging/"
tar -C "$staging" -cJf "$tarball" agent-gateway-host

sum=$(shasum -a 256 "$tarball" | cut -d' ' -f1)
cat > "$hashfile" <<HASH
# Written by packs/agent-gateway/host/release.sh. Buildroot refuses a
# download that does not match, so this line is what ties the binary an
# appliance runs to the source in this repository.
sha256  $sum  $tarball
HASH

cat <<DONE

$tarball  $(wc -c < "$tarball") bytes
sha256 $sum
hash written to $hashfile

Next: upload $tarball as release agent-gateway-host-v$version in the
releases repository the package's SITE names, then commit the hash file.
DONE
