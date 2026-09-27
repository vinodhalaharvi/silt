---
title: Buildroot ignored my new defconfig
description: You changed the defconfig, ran make, and the image came out the
  same. Buildroot builds from output/.config, which is only regenerated when
  you ask. How to spot it and prevent it.
kind: article
date: 2026-09-20
---

# Buildroot ignored my new defconfig

You added a package to your defconfig. You ran `make`. The build took a
sensible amount of time and produced an image. The package is not in it.

Nothing warned you, because as far as Buildroot is concerned nothing went
wrong.

## Two files, and only one of them is read

Buildroot builds from `output/.config`. The defconfig is a compact input used
to *generate* that file, and only when you ask:

```
make O=output defconfig BR2_DEFCONFIG=/path/to/defconfig
```

A plain `make` uses the `.config` that is already there. If you have a tool or
a script that rewrites the defconfig, the two files drift apart silently, and
the build keeps using the older one. Every further build inherits it.

The check takes two seconds:

```
grep BR2_PACKAGE_YOURTHING defconfig output/.config
```

If the symbol is in one and not the other, that is the bug. Timestamps tell
the same story:

```
ls -la --time-style=full-iso defconfig output/.config
```

A defconfig newer than the `.config` next to it means the `.config` came from
an older version of the file.

## What it looks like from the outside

The reason this wastes an evening is that the symptom lands somewhere else
entirely. In our case a public key was in the defconfig's rootfs overlay, the
build used an older `.config` with no overlay at all, and the visible failure
was SSH asking for a password on a board that was supposed to be key-only. We
inspected sshd config and file permissions on the running system for a while
before looking at the two config files.

Anything can be the symptom: a missing binary, a daemon that does not start, a
library that is not there, a kernel option that did nothing. The common shape
is that the image does not contain something you are certain you configured.

## The fix, and the better fix

Regenerate, verify, then build:

```
make O=output BR2_EXTERNAL=/path/to/external defconfig BR2_DEFCONFIG=$PWD/defconfig
grep BR2_PACKAGE_YOURTHING output/.config     # before spending the build time
make O=output
```

`BR2_EXTERNAL` has to be on the `defconfig` line too, not only on the build
line, or symbols from your external tree will be dropped as unknown while the
defconfig is parsed.

The better fix is to make the drift impossible to miss. If something generates
your defconfig, have it record a hash of the inputs in a comment, and have a
check compare that against the `.config` before a build:

```
# defconfig
# silt-solution: b4950057966312c5534dbb2e
```

Then the question "is this build actually made from what I think it is?" has
an answer you can automate, rather than a habit you have to remember at the
end of a long day.

## While you are here

Two neighbouring traps with the same flavour:

- **`make` after changing a package's source in an external tree.** Buildroot
  will not notice. `make <pkg>-rebuild` re-syncs and rebuilds that package;
  `make <pkg>-dirclean` starts it over.
- **Changing `BR2_ROOTFS_OVERLAY` contents.** The overlay is copied during
  target-finalize, so the rootfs image needs regenerating, but package builds
  are untouched. `make` is enough here, and the more common mistake is
  checking `output/target` instead of the actual filesystem image. Permissions
  and ownership in `output/target` are not what ends up in the image; read the
  image itself with `debugfs` if you want the truth.
