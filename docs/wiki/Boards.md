# Boards

Ten images across three architectures. Each entry says what the board is for
and, where there is one, what importing it taught.

## The appliance

The same simulated device on every board: Modbus TCP, OPC UA, MQTT, CAN and
HTTP config, all reading one shared tag table in `/dev/shm`. One JSON file
describes the device; nothing about it is compiled in.

| image | board | notes |
| --- | --- | --- |
| `raspberrypi3-64-bringup` | Pi 3 B/B+ | the one that has run all week |
| `raspberrypi3-64-vpn` | Pi 3 | plus WireGuard; one package, 65s to build |
| `raspberrypi3-64-media` | Pi 3 | plus cameras; use MJPEG, x264 will not keep up |
| `raspberrypi3-64-garage` | Pi 3 | plus Tailscale; runs in the house |
| `raspberrypi0-bringup` | Pi Zero | reachable over its own USB cable |
| `rock5b-bringup` | Radxa Rock 5B | RK3588, the fastest board here |
| `imx8mp-evk-bringup` | NXP i.MX 8M Plus EVK | two Ethernet, two real CAN |
| `cm5-io-appliance` | CM5 + official IO board | |
| `qemu-arm-appliance` | QEMU virt | no hardware needed; the evaluation image |

## Compute Module 5

One module, three carriers, an image each. The module carries the SoC and the
kernel; the carrier states its device tree and what it has populated.

    (image cm5-io-appliance
      (compose
        image:cm5-io              ;; ← the only line that changes
        feature:dev-ssh
        feature:modbus
        feature:fieldbus-test))

| carrier | what it has |
| --- | --- |
| `cm5-io` | official IO board: 1 Ethernet, M.2, dual MIPI, RTC |
| `cm5-io-wireless` | Waveshare: 2× isolated RS485, isolated CAN, 4G/5G or LoRa, NVMe |
| `cm5-dual-eth` | Waveshare: 1G + 2.5G Ethernet, 4× RS485, NVMe, cellular |

The two Waveshare device trees are marked UNVERIFIED and currently use the
official board's. Their RS485 sits behind a UART expander and their CAN behind
an MCP251x-class controller, so both want overlays the vendor's wiki names.

Swapping carriers costs 66 seconds, because they share 444 symbols and the
store keeps the rest.

## What each board taught

**i.MX 8M Plus** — importing it found a bug that had been in
`feature:fieldbus-test` since it was written. `BR2_PACKAGE_KMOD_TOOLS` sits
behind `BR2_PACKAGE_BUSYBOX_SHOW_OTHERS`, which all three Raspberry Pi profiles
happen to set, so the feature composed *by accident* everywhere it had been
tried. On this board's profile the prediction said:

    buildroot:BR2_PACKAGE_KMOD_TOOLS: asked for y, kbuild would write absent

The visible failure would have been `vcan0` missing after a forty-minute build,
with nothing in the logs pointing at the cause.

**Pi Zero** — no Ethernet jack, so `feature:usb-gadget-net` makes the data USB
port a network interface. Three things must agree, in three different places,
which is why it is a feature and not a note in a README: `dtoverlay=dwc2` in
`config.txt`, `dwc2` and `g_ether` in the kernel, and userspace loading them.

It also produced a conflict with no correct automatic answer:

    error: conflicting values for BR2_PACKAGE_RPI_FIRMWARE_CONFIG_FILE
      "board/raspberrypi0/config_default.txt"            target:raspberrypi0
      "$(BR2_EXTERNAL_USB_GADGET_PATH)/files/config.txt" feature:usb-gadget-net

Silently preferring one firmware config over another would be a guess about how
the board boots. The image says which, with an `(override ...)`.

**Pi 5** — Buildroot 2025.02.16 pins a 6.6.28 kernel from April 2024 that does
not boot a Model B Rev 1.1. Establishing that took the decisive test: stock
`raspberrypi5_defconfig`, no silt involved, fails identically. The target now
pins the tip of `rpi-6.12.y`.

**Rock 5B** — an earlier hand-built attempt failed at the last step after
twenty-eight minutes, because the RK3588's kernel modules do not fit the
profile's 250M rootfs. A size is a value rather than a minimum, so an image that
needs more says so:

    (override (value buildroot:BR2_TARGET_ROOTFS_EXT2_SIZE "512M"))

**QEMU** — the Pi and CM5 images cannot boot here: there is no `bcm2712`
machine. That is not worth working around, because an image is a board plus a
userspace and only the board half is missing. The same four features compose
onto `target:qemu-aarch64-virt` and the same device answers on the same ports.

Writing that image walked straight into the conflict the README opens with. The
first draft used `profile:minimal`:

    packs/fieldbus-test/.../fieldbus-test.sx:86: BR2_PACKAGE_KMOD requires
      !BR2_STATIC_LIBS (package/kmod/Config.in:1)
    but buildroot:BR2_STATIC_LIBS is stated y by profile:minimal

## Adding a board

    silt import /path/to/buildroot/configs/<board>_defconfig \
      --buildroot /path/to/buildroot --kbuild

`--kbuild` proves the round trip: emit the fragments, turn them back into a
defconfig, and check `savedefconfig` produces the original. Then declare what
the board has, which no defconfig can tell you:

    (provides
      (capability csi-camera)
      (capability sdcard)
      (capability serial-console)
      (capability nvme))

    ;; Not claimed: dual-ethernet (one RJ45 populated), can-bus and cellular
    ;; (no transceiver, no modem slot), wifi-sdio (the M.2 E-key is empty).

The second comment matters as much as the first. A capability a board does not
have is a feature that will not compose onto it.
