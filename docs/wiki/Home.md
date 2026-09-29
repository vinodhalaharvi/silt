# silt

Silt composes Buildroot and Linux configurations from small, checkable pieces,
and tells you what a build will produce before you spend forty minutes finding
out.

A Buildroot image is two Kconfig trees that must agree and nothing that checks
they do. Kconfig also deletes requests it cannot satisfy, silently: ask for a
package whose dependency you turned off elsewhere and the line is simply gone
from the `.config`, with no error and no warning. Silt states the composition,
predicts the `.config` both trees will produce, and refuses the build when the
prediction and the request disagree.

## Start here

- **[Quickstart](Quickstart)** — boot the appliance in QEMU in five minutes, no
  hardware
- **[Concepts](Concepts)** — targets, profiles, features, capabilities, images,
  packs
- **[Boards](Boards)** — what runs where, and what each board taught
- **[Building](Building)** — the cache, and why a build costs what is new in it
- **[CI and downloads](CI-and-downloads)** — what runs on every commit, what is
  gated, where the images end up
- **[Troubleshooting](Troubleshooting)** — the failures that cost real evenings,
  and what each one actually was

## In one example

    $ silt check --buildroot ~/buildroot
    ok  raspberrypi3-64-bringup 43 buildroot symbols checked against
        buildroot 2025.02.16, prediction agrees
    ok  127 files, 54 fragments, 1 rule blocks, 55 images

    $ ci/silt-build.sh images/raspberrypi3-64-vpn.sx
    prefix raspberrypi3-64-vpn  0358447fa5648b8e  (from raspberrypi3-64-bringup,
           466 symbols shared)
    store  raspberrypi3-64-vpn  0358447fa5648b8e  (65s)

Adding WireGuard to an appliance that already exists costs 65 seconds, because
that is what is new in it. The same image from nothing is 1132.

## What it is not

Not a distribution, not a build system, not a replacement for Buildroot. Silt
emits a defconfig and a kernel fragment; Buildroot does the building. The
contribution is everything before `make`, plus the verification afterwards that
`make` did what was asked.
