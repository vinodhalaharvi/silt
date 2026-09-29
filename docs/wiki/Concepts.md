# Concepts

Everything here is an example first. The ideas are small; the reason they exist
is always a specific failure.

## A fragment states symbols

    (fragment feature:modbus
      (doc "Modbus TCP, client and server")
      (buildroot
        (y BR2_PACKAGE_LIBMODBUS)))

That is the whole form. `(y ...)`, `(n ...)`, `(m ...)` for tristates,
`(at-least m ...)` when a module will do, `(value ... "string")` for strings and
numbers, `(path-append ... "dir")` for the space-separated lists Buildroot uses
for overlays and fragment files.

## Two trees, two namespaces

A Buildroot `.config` uses `BR2_`. A kernel `.config` uses `CONFIG_`. So does
BusyBox's, in a third file. Silt keeps them apart:

    (fragment feature:usb-gadget-net
      (buildroot
        (path-append BR2_ROOTFS_OVERLAY "overlay"))
      (linux
        (y CONFIG_USB_GADGET)
        (m CONFIG_USB_DWC2)
        (m CONFIG_USB_ETH)))

`silt check --buildroot DIR` verifies the first against Buildroot's Kconfig;
`--linux DIR` verifies the second against a real kernel's. A symbol that exists
in neither is caught by name, which is how a rule naming
`CONFIG_SYSFS_DEPRECATED` was found still in the tree after the kernel had
deleted it.

## Target, profile, feature

The split is not aesthetic. It is what makes a board reusable.

**A target is exactly one board** - the SoC, the kernel, the bootloader, the
device tree. Silt enforces "one target per image", which is how the first CM5
model was rejected:

    error: compose names 2 targets (target:cm5, target:cm5-io);
           a target is exactly one board

With a compute module, the *board* is the carrier: the thing with connectors.
The module composes onto it.

**A profile is userspace taste** - init system, libc, rootfs format, whether
there is a shell at all:

    (fragment profile:locked
      (buildroot
        (y BR2_INIT_SYSTEMD)
        (n BR2_TARGET_GENERIC_GETTY)
        (n BR2_TARGET_ENABLE_ROOT_LOGIN)))

**A feature is a capability you add** - a protocol, a daemon, a tunnel:

    (fragment feature:tailscale
      (requires (capability mmu))
      (buildroot
        (y BR2_PACKAGE_TAILSCALE)
        (y BR2_PACKAGE_CA_CERTIFICATES)
        (y BR2_PACKAGE_CHRONY))
      (linux
        (at-least m CONFIG_TUN)))

## An image composes them

    (image raspberrypi3-64-garage
      (compose
        image:raspberrypi3-64-bringup
        feature:tailscale)
      (override
        (value buildroot:BR2_TARGET_ROOTFS_EXT2_SIZE "256M"))
      (ci build)
      (verified-against (buildroot "2025.02.16")))

An image may compose another image, which is how the store gets its prefixes:
`raspberrypi3-64-garage` extends `raspberrypi3-64-bringup`, so their configs
share 466 symbols and the build is the difference.

## Capabilities: what a board has, what a feature needs

Neither is a Kconfig symbol, because no configuration option knows whether a
transceiver is fitted:

    (fragment target:cm5-io-wireless
      (provides
        (capability rs485)
        (capability can-bus)
        (capability nvme)
        (capability cellular)))

    (fragment feature:modbus-rtu
      (requires (capability rs485)))

Compose that feature onto a board without RS485 and it fails at check time,
with the reason, rather than on a bench. Only targets and profiles may provide
capabilities - a feature providing one makes composition order-dependent, which
silt refuses:

    error: a feature may not provide capabilities (feature:cm5-module);
           only targets and profiles may, or composition becomes order-dependent

## Escape hatches, and when each is honest

**`(override ...)`** - when two fragments disagree and only the image can
decide:

    ;; profile:locked has no getty, so the port symbol the target states
    ;; does not exist. Say so, rather than let it vanish silently.
    (unmanaged buildroot:BR2_TARGET_GENERIC_GETTY_PORT)

**`(unmanaged ...)`** - silt makes no claim about this symbol. Used above
because a getty port in an image with no getty is meaningless, not lost.

**`(opaque ...)`** - a value silt passes through without modelling.

**`(expect-problems "...")`** - this image is a fixture and must keep failing:

    ;; edge-camera asks for libcamera with static libs. kbuild must drop
    ;; something, and if it ever stops, the fixture has stopped testing.
    (expect-problems "libcamera depends on !BR2_STATIC_LIBS and profile:minimal sets it")

An override reduces to "I know" written down. Every one in this repository
carries the reason beside it.

## Cross-tree rules

The thing neither Kconfig can say: enabling something in one tree constrains
the other.

    (rules cross-tree
      (when (y buildroot:BR2_PACKAGE_WPA_SUPPLICANT)
        (at-least m linux:CONFIG_MAC80211))

      ;; A module-only filesystem cannot hold the root filesystem: nothing
      ;; can load it before it is mounted. Neither tree can express boot order.
      (when (y buildroot:BR2_TARGET_ROOTFS_EXT2)
        (y linux:CONFIG_EXT4_FS))

      ;; systemd's ten mandatory kernel options, from its own README rather
      ;; than from memory.
      (when (y buildroot:BR2_INIT_SYSTEMD)
        (y linux:CONFIG_DEVTMPFS)
        (y linux:CONFIG_CGROUPS)
        (y linux:CONFIG_INOTIFY_USER)))

## Packs

A pack is fragments plus the `br2-external` tree they need, versioned together:

    (pack tailscale
      (version "0.1.0")
      (requires (buildroot "2025.02.16"))
      (external "br2-external")
      (provides (feature tailscale)))

`silt externals IMAGE.sx` prints the `BR2_EXTERNAL` value an image needs, so the
list is derived rather than remembered:

    $ silt externals images/raspberrypi0-tailscale.sx --buildroot ~/buildroot
    /home/you/silt/packs/dev-ssh/br2-external:/home/you/silt/packs/tailscale/br2-external:/home/you/silt/packs/usb-gadget/br2-external
