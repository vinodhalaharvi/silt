# Troubleshooting

Every entry here cost real time. They are written as symptom first, because
that is what you have when you arrive.

## The board does not appear on the network

**`arp -a` shows nothing.** It shows only hosts your machine has spoken to
recently, so an empty result proves nothing at all. This cost four hours on a
board that had been up the whole time at the address it always had.

    # useless
    arp -a | grep -i b8:27:eb

    # actually probes
    sudo nmap -sn 192.168.68.0/24 | grep -B2 -i raspberry

Use `nmap`. Also check the router's client list, which sees DHCP requests
directly.

**Ethernet lights are on but nothing answers.** Those come from the PHY, which
powers up and negotiates on its own. A Pi with no SD card at all lights its
Ethernet jack. They tell you the cable and switch port are fine and nothing
about whether Linux booted.

**Ping works, every port is closed.** The kernel is up and userspace stopped
partway through init. On a BusyBox system the scripts run in order and each one
waits:

    S40network  →  S41tailscale  →  S50dropbear  →  S88..S94 daemons

An init script that waits fifteen seconds for something that is not ready yet
holds up everything behind it - ssh, the protocols, the getty. Nothing appears
in any log, because the thing that writes logs has not started. Put anything
that waits last.

    debugfs -R "ls -l /etc/init.d" rootfs.ext4      # see the order

## The board does not boot

**Compare the boot partition against a card that works.**

    ci/boot-diff.sh /path/to/raspios.img build/images/sdcard.img

The boot partition is the whole handoff from firmware to kernel: what to load,
what to tell it, what the hardware is, what may be turned on. When a board boots
one card and not another, the difference is often here rather than in either
kernel - and it is invisible, because both partitions are inside image files.
It takes an image, a mounted directory or an .img.zst, mounts nothing and needs
no root. The output is a unified diff, because that is a format every reader
already knows:

    == config.txt
    -arm_64bit=1
    -auto_initramfs=1
    -dtoverlay=vc4-kms-v3d
    +kernel=Image
     disable_overscan=1

    == every other file
    Only in ours: Image
    Files theirs/bcm2712-rpi-5-b.dtb and ours/bcm2712-rpi-5-b.dtb differ
    Only in theirs: overlays/ (58 entries)

The last kind of line is the one worth watching: a device tree with the same
name and different contents is a plausible cause that neither "only in" list
would catch.

It says nothing about either kernel, deliberately: a kernel image carries no
configuration. If the diff comes back with nothing that explains the failure,
the difference is inside one of them and the next tool is a serial console.

**Rainbow screen on a Pi, or a steady LED and nothing else.** Firmware ran, the
kernel did not start. Distinguish image from hardware with one test: flash
stock Raspberry Pi OS. If that boots and gets an address, the board is fine and
the image is the suspect. If it does not, stop looking at software.

Then the second test: build Buildroot's own defconfig for that board, with no
silt involved. If *that* fails too, the fault is upstream. This is how the Pi 5
kernel problem was established rather than guessed.

**An SError in pinctrl, on a Pi 5.**

    SError Interrupt on CPU1, code 0x00000000be000011
    pc : brcmstb_pull_config_set+0x64/0xf0
    Kernel panic - not syncing: Asynchronous SError Interrupt

The page size. Buildroot's raspberrypi5_defconfig applies
`linux-4k-page-size.fragment`, whose whole content is
`CONFIG_ARM64_4K_PAGES=y`, and a Pi 5 Rev 1.1 with a 4k-page kernel takes a bus
fault on the first pinctrl pad write. `bcm2712_defconfig` selects 16k on its
own, so the fix is to delete the fragment rather than state a replacement.

Four hours of this looked like a driver problem, because the first thing to
write a pad register was the 8250 BCM7271 UART and blacklisting its initcall
moved the panic rather than removing it. What settled it was reading the
config of a kernel that works - Raspberry Pi OS keeps it at
`/boot/config-$(uname -r)` - and finding `CONFIG_ARM64_16K_PAGES=y` where ours
had 4k. `getconf PAGESIZE` on a running board says the same thing in one line.

**A stale file from a cached tree.** A build that restores an output tree
inherits its `target/` directory, and nothing removes a file an overlay stopped
providing. Renaming an init script left both versions in the image, and the old
one kept blocking init:

    S41tailscale    ← the old one, still there
    S95tailscale    ← the new one

If an image contains something you deleted, delete that slot and the work
directory, and rebuild:

    rm -rf ~/.cache/silt/slots/<hash> ~/.cache/silt/work

## TLS fails and blames a certificate

    x509: certificate has expired or is not yet valid:
    current time 1970-01-01T00:02:18Z is before 2026-08-03T05:43:45Z

Not a certificate problem. A Raspberry Pi has no RTC, so every boot starts at
1970 and every certificate on the internet is "not yet valid". Set the clock:

    ssh root@$PI "date -s '$(date -u '+%Y-%m-%d %H:%M:%S')'"

Permanently, the image needs a time client. chrony's init script is S49, before
anything that needs TLS, and its default policy must be changed - chrony slews
small offsets and refuses large ones, which suits a clock that is roughly
correct rather than one fifty-six years out:

    makestep 1 -1

## The build fails

**"no hash found".** A custom kernel tarball has no published hash, and
`BR2_DOWNLOAD_FORCE_CHECK_HASHES` demands one. Either add a hash or turn the
check off for that board, with a comment saying what is lost.

**"built, but no sdcard.img to store".** Not every board produces a card image.
A QEMU target produces a kernel and a filesystem, to be passed to
`qemu-system-*` separately. The store records what each slot holds in an
`artifacts` file rather than assuming a name.

**A changed defconfig is ignored.** Buildroot builds from `output/.config`, and
a plain `make` leaves it alone. Always:

    make -C $BR O=$OUT defconfig BR2_DEFCONFIG=$OUT/defconfig
    make -C $BR O=$OUT

That one cost an evening, and it is why `silt-build.sh` runs `defconfig` every
time.

## A feature composes on one board and not another

    buildroot:BR2_PACKAGE_KMOD_TOOLS: asked for y, kbuild would write absent
    stated by feature:fieldbus-test

Something the feature depends on is provided by a *profile* rather than by the
feature, and it happens to be set on the boards you tried.
`BR2_PACKAGE_KMOD_TOOLS` sits behind `BR2_PACKAGE_BUSYBOX_SHOW_OTHERS`, which
all three Raspberry Pi profiles set. The fix is for the feature to state what it
needs.

## CI fails where local does not

**`ssh host "command"` reads no profile.** A non-interactive shell has none of
what a login shell puts on `PATH`:

    make: go: No such file or directory

on a machine where `go` works when you log in. Export `PATH` explicitly. The
general form: anything a person's shell sets up is absent from a CI job by
construction.

**gcloud caches a token per user.** After widening a service account's scope,
an account that already has a token keeps presenting the old one. The same
command as a different user on the same machine works, which is a confusing
difference. Either clear the cache or - better - do the privileged thing
somewhere else.

**A test disagrees with the tool about what the library is.** Three tests each
had their own globs over `fragments/` and `packs/`, and no two agreed, so the
same missing piece appeared as three unrelated-looking errors:

    no such image image:cm5-io
    no such fragment feature:dev-ssh
    provides capability "no-console-login", which is not declared

There is one list now, `compose.LibraryPaths`. If you write something that
loads `.sx` files, use it.

## Running what CI runs

    make ci BUILDROOT=~/buildroot
    make ci BUILDROOT=~/buildroot LINUX=~/linux

Five minutes on a machine with the trees, against twenty in CI, and it is the
same commands in the same order. `make ci-fast` leaves out the kbuild
experiments for a change that cannot touch a defconfig.
