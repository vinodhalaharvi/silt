#!/bin/sh
# First-boot provisioning for a WireGuard appliance.
#
# Idempotent by construction: every step checks before acting, so this runs on
# every boot and does work only on the first. That is deliberate — a "first
# boot" flag file is one more thing that can be wrong.
#
# The state partition is a second disk. The root filesystem is read-only, so
# the private key has to live somewhere that survives a reboot; a tmpfs would
# mean a new identity every boot, which invalidates every peer that trusts
# this one.

set -eu

DATA_DEV=/dev/vdb
DATA_DIR=/data
WG_DIR=$DATA_DIR/wireguard
IFACE=wg0
PORT=51820
ADDR=10.9.0.1/24

log() { echo "wg-appliance: $*"; }

# 1. A blank disk has no filesystem. blkid says nothing about it, which is how
#    this tells "never used" from "already has our data" without a flag file.
if ! blkid "$DATA_DEV" >/dev/null 2>&1; then
	log "formatting $DATA_DEV"
	mkfs.ext4 -q -L wgdata "$DATA_DEV"
fi

mkdir -p "$DATA_DIR"
mountpoint -q "$DATA_DIR" || mount "$DATA_DEV" "$DATA_DIR"
mkdir -p "$WG_DIR"

# 2. The identity. Generated here and never transmitted: the private key does
#    not exist anywhere before this boot, including in the image, so two
#    appliances from the same download are different peers.
if [ ! -f "$WG_DIR/private" ]; then
	log "generating a keypair"
	( umask 077 && wg genkey > "$WG_DIR/private" )
	wg pubkey < "$WG_DIR/private" > "$WG_DIR/public"
fi

# 3. The interface. wg-quick is not used: it is a bash script, and bash is a
#    shell this appliance has no reason to carry. ip and wg do the same work.
ip link show "$IFACE" >/dev/null 2>&1 || ip link add "$IFACE" type wireguard
wg set "$IFACE" private-key "$WG_DIR/private" listen-port "$PORT"
ip addr show dev "$IFACE" | grep -q "${ADDR%%/*}" || ip addr add "$ADDR" dev "$IFACE"
ip link set "$IFACE" up

# 4. Peers, if a deployment has supplied any. Nothing can connect until this
#    file exists, which is the one step that cannot be done in advance: a peer
#    is a public key the appliance has never seen.
if [ -f "$WG_DIR/peers.conf" ]; then
	log "applying peers from $WG_DIR/peers.conf"
	wg addconf "$IFACE" "$WG_DIR/peers.conf"
else
	log "no peers configured: put them in $WG_DIR/peers.conf"
fi

# 5. The public key, on the console. This is the only way to read it off a
#    machine with no login, and it is what a deployment needs to configure the
#    other end.
echo
echo "================================================================"
echo "  WireGuard appliance ready"
echo "  interface : $IFACE, $ADDR, udp/$PORT"
echo "  public key: $(cat "$WG_DIR/public")"
echo "================================================================"
echo
