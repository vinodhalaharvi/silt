# Building

    ci/silt-build.sh images/raspberrypi3-64-bringup.sx

Three kinds of answer, in order of how little work they are:

    hit      this exact image was built before; copy the artifact out
    prefix   a stored tree is a safe starting point; build only the difference
    miss     nothing safe to start from; build from nothing

## What it costs

Measured on a 32-core VM, Buildroot 2025.02.16:

| | build | what was new |
| --- | --- | --- |
| `raspberrypi3-64` | **1132s** | everything: toolchain, kernel, BusyBox |
| `cm5-io` | **1158s** | everything, different SoC |
| `raspberrypi3-64-bringup` | **246s** | ~13 libraries and 6 daemons |
| `raspberrypi3-64-media` | **734s** | GStreamer, ffmpeg, x264, boost |
| `raspberrypi3-64-vpn` | **65s** | one package |
| `cm5-io-wireless` | **66s** | a different carrier board |
| anything already built | **3s** | nothing |

The cost of an image is what is new in it, not what is in it. Two of those
numbers are 65 and 66 seconds and they are different kinds of delta - adding a
package, and swapping to another physical board.

## Why a prefix is safe

Buildroot rebuilds from stamp files, not from inputs. Drop in a tree built with
another configuration and every package already stamped is skipped, whatever
the new configuration says. So a stored tree is only safe when nothing already
built in it would have been built differently.

Silt can answer that without running make, because `silt complete` predicts the
full `.config` an image will become:

    added symbol     fine, Buildroot builds it
    changed value    unsafe, whatever depended on it is already stamped
    removed symbol   unsafe - the monotonicity rule, made mechanical

Worth seeing it refuse:

    target = the plain Pi 3 board
      reject raspberrypi3-64-bringup   (BR2_PACKAGE_CAN_UTILS=y)

`can-utils` is built and installed in that tree and nothing would uninstall it,
so the appliance tree may not be a starting point for an image that never asked
for it. Different architectures reject on `BR2_ARCH`; a different FPU on
`BR2_ARM_FPU_VFPV4`.

## Two exclusions, and why each exists

Symbols read *after* every package is built, by steps that re-run on every
make - the overlays, post-build and post-image scripts, the filesystem size,
the firmware config files. A changed overlay cannot have affected a compiled
package, because nothing reads it until `target-finalize`. Without this, a board
image could never be a prefix of the appliance built on it.

Symbols Buildroot fills in from the invocation rather than the configuration:
`BR2_DEFCONFIG`, the kernel fragment path, `BR2_DL_DIR`, `BR2_JLEVEL`, and every
`BR2_EXTERNAL_*`. That last one matters more than it looks:
`BR2_EXTERNAL_<NAME>_VERSION` is `git describe` of the external tree, so it
changes on every commit. Before it was excluded, **every stored tree was
disqualified the moment anything was committed**, and a one-package image took
twenty-two minutes instead of one.

## Two checks bracket every build

    $ ci/silt-build.sh images/broken-on-purpose.sx
    ./ci/silt-build.sh: this image does not compose:
    error: images/broken-on-purpose.sx:1: no such fragment feature:no-such-feature

`silt solve` first, 431ms: can this image exist? Then, after `make defconfig`
and before the long `make`:

    prediction and kbuild agree on every symbol kbuild knew

That compares all ~460 symbols kbuild knew about, set and unset, against the
`.config` it actually wrote. `silt fixpoint` checks the 43 the image *states*;
this checks the four hundred it does not - which are the ones the prefix test
compares. Without it the store's safety would rest on an unverified prediction.

## Where things live

    /srv/silt/slots            the store, shared, group siltbuild, setgid
      <hash>/ sdcard.img       or Image + rootfs.ext4, per board
              defconfig
              predicted.config
              artifacts        which files this slot holds
              tree.tar.zst     the output tree, ~2GB compressed
              manifest

    $SILT_WORK                 this build's scratch tree, wiped on any non-hit
    $SILT_LOCK                 the lock guarding it
    ~/.cache/buildroot-dl      downloads, shared, outside the work tree

Builds always happen in one fixed directory, because a Buildroot output tree is
not relocatable: absolute paths live in `.config`, the generated Makefile,
libtool archives and every stamp. `-o` receives the image and the defconfig, not
a build tree.

## Housekeeping

    ci/silt-build.sh --list           what is in the store
    ci/silt-build.sh --prune 4        keep trees for the 4 most recent slots

Pruning drops trees and keeps images: an exact rebuild is still a copy
afterwards, but a near-miss goes back to building. Trees are gigabytes and
answer the prefix case; images are megabytes and answer the hit.

## Two people, one machine

The store is shared, keyed by content, so a person at a terminal and a CI job
reuse each other's trees. The work directory is not shared - it is scratch,
wiped at the start of every non-hit, and two builds sharing one would delete
each other's tree mid-build.

    export SILT_CACHE=/srv/silt
    export SILT_WORK=$HOME/.cache/silt/work
    export BUILDROOT=$HOME/buildroot
