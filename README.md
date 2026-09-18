# silt

Composable S-expressions over Kconfig, producing real bootable ARM64 images.

You describe what you want as reusable fragments. Silt merges them, checks them
against the real Kconfig constraint model, completes them into a valid configuration,
explains anything that conflicts, and emits a `defconfig` plus a kernel `.config` that
Buildroot and kbuild accept unchanged. Buildroot builds it. It boots.

Silt compiles nothing. Its output artifact is a validated configuration.

> **Status: pre-implementation.** There is no Go in this repository yet. What follows
> is the design, written against a real Buildroot tree — every factual claim below was
> verified and carries the command to re-verify it. See [Status](#status).

---

## Why

### Kconfig deletes your request without telling you

Take a working Buildroot config, ask for a camera, and ask for static linking.
libcamera has `depends on !BR2_STATIC_LIBS`, so this cannot be satisfied:

```sh
cd buildroot
make qemu_aarch64_virt_defconfig
echo 'BR2_PACKAGE_LIBCAMERA=y' >> .config
echo 'BR2_STATIC_LIBS=y'       >> .config
make olddefconfig
grep LIBCAMERA .config
```

```
exit=0
stderr: 0 bytes
BR2_PACKAGE_LIBCAMERA    ABSENT ENTIRELY
```

Not `# BR2_PACKAGE_LIBCAMERA is not set`. The symbol is **removed from the file**. You
asked for a camera, got a config that builds cleanly without one, and the only way to
find out is to diff what you wrote against what kbuild kept.

Sixty seconds to reproduce. This is the defect Silt exists to catch — and the standard
Silt is held to, because *if Silt's own losses are silent, it has reproduced the bug it
was built to fix.*

Written as fragments, the same request is a conflict with two named sources rather than
a deletion:

```lisp
(fragment profile:minimal (buildroot (y BR2_STATIC_LIBS)))
(fragment feature:camera  (buildroot (y BR2_PACKAGE_LIBCAMERA)))
;; libcamera has `depends on !BR2_STATIC_LIBS` → UNSAT, with both file:line cited
```

### Buildroot expresses kernel requirements in English

Nineteen package `Config.in` files reference kernel `CONFIG_*` symbols. Every single
reference sits inside a `help` block, because Buildroot's Kconfig cannot reach into the
kernel's namespace — they are two separate trees:

```
fscryptctl     "requires a kernel with CONFIG_EXT4_ENCRYPTION=y"
18xx-ti-utils  "CONFIG_NL80211_TESTMODE must be enabled in the kernel"
bcc            "Compile kernel with CONFIG_IKHEADERS"
dpdk            nine symbols under "Optional but recommended kernel configurations"
```

Nothing enforces any of it. You read the prose, you go edit a different file in a
different format, and if you forget, it fails at runtime on the device.

One rule per help string, checked instead of read:

```lisp
(rules cross-tree
  (when (y BR2_PACKAGE_FSCRYPTCTL)    (y CONFIG_EXT4_ENCRYPTION))
  (when (y BR2_PACKAGE_BCC)           (y CONFIG_IKHEADERS))
  (when (y BR2_PACKAGE_18XX_TI_UTILS) (y CONFIG_NL80211_TESTMODE)))
```

```sh
grep -rl 'CONFIG_[A-Z0-9_]' buildroot/package/*/Config.in | wc -l    # 19
```

---

## What you write

Fragments are declarative and composable. A target fixes hardware and mentions no
packages. A profile fixes userspace policy and mentions no hardware.

```lisp
;; fragments/targets/qemu-aarch64-virt.sx
(fragment target:qemu-aarch64-virt

  (buildroot
    (y BR2_aarch64)
    (y BR2_cortex_a57)
    (value BR2_TARGET_GENERIC_GETTY_PORT "ttyAMA0"))

  (linux
    ;; Kconfig permits =m for both. The rootfs is on virtio and the console is
    ;; needed before userspace, so neither can be a module. Kconfig cannot express
    ;; boot ordering — this is why the fragment exists at all.
    (y CONFIG_VIRTIO_BLK)
    (y CONFIG_SERIAL_AMBA_PL011_CONSOLE))

  (provides
    (capability mmu)
    (capability virtio)
    (capability serial-console)))
```

Features are written against **capabilities**, not against a specific board's symbols,
which is what makes them reusable across targets:

```lisp
;; fragments/features/camera.sx  — three lines
(fragment feature:camera
  (requires  (capability csi-camera))
  (buildroot (y BR2_PACKAGE_LIBCAMERA))
  (linux     (at-least m CONFIG_VIDEO_DEV)))
```

An image is a composition and almost nothing else:

```lisp
;; images/qemu-arm-dev.sx
(image qemu-arm-dev
  (compose target:qemu-aarch64-virt profile:minimal feature:networking)
  (linux (custom-version "6.18.7")))
```

### The rule that keeps fragments small

> **State only what Kconfig cannot derive.**

Anything implied by an existing `depends on` or `select` is omitted. `feature:camera`
is three lines; a first draft had fourteen, and the other eleven were all derivable —
deleting them changed nothing about the solved model. What survives the rule falls
into five categories: **choices**, **strengthening** (Kconfig permits `=m`, boot order
does not), **cross-tree implications**, **negative intent**, and **capabilities**.

A linter enforces this once the constraint model exists: any line whose removal does
not change the solved model is dead.

### The forms you write most of the time

| Form | Meaning | Lowers to |
| --- | --- | --- |
| `(y SYM)` | hard: built in | `sym_y` |
| `(m SYM)` | hard: module | `sym_m` |
| `(n SYM)` | hard: off | `¬sym_y ∧ ¬sym_m` |
| `(at-least m SYM)` | module or built-in | `sym_y ∨ sym_m` |
| `(prefer y SYM)` | soft; yields, and says so | MaxSAT soft clause |
| `(value SYM "str")` | string/int/hex | emptiness modeled, value passed through |
| `(when A B)` | guarded | `A ⇒ B` |

Those seven cover almost everything you type. The full language is thirty-four
keywords — composition, escape hatches, repair policy — plus nine more the importer
emits. All of it is specified in [GRAMMAR.md](GRAMMAR.md), written before the parser so
there is something for the parser to be tested *against* rather than defined by.

`at-least` exists only because this builds real images — module versus built-in changes
what lands in the rootfs. `when` never executes anything; it lowers to an implication
and the solver decides, which is how the representation stays data rather than becoming
a Lisp.

### Reuse without functions

No lambdas, no macros, no parameters. Reuse is **under-specification plus merge**, the
CUE-style answer. `target:qemu-aarch64-virt` never names a rootfs format;
`profile:minimal` never names hardware. Composing them unifies the two and each fills
the other's hole. Swapping the target changes hardware and nothing else — and that is
testable:

```sh
silt diff images/qemu-arm-dev.sx --swap target:qemu-aarch64-virt=target:rpi4-64
```

If that swap requires editing a feature, the boundaries are wrong.

### The cross-tree rules

The file that does what neither Kconfig tree can. Those 19 help strings, made
machine-checkable:

```lisp
;; fragments/rules/cross-tree.sx
(rules cross-tree
  (when (y BR2_PACKAGE_WPA_SUPPLICANT) (at-least m CONFIG_MAC80211))
  (when (y BR2_PACKAGE_LIBCAMERA)      (at-least m CONFIG_VIDEO_DEV))
  (when (y BR2_PACKAGE_DHCPCD)         (y CONFIG_PACKET))
  (when (y BR2_INIT_SYSTEMD)           (y CONFIG_CGROUPS)
                                       (y CONFIG_INOTIFY_USER)
                                       (y CONFIG_FHANDLE))
  ;; A module-only filesystem cannot hold the root filesystem: nothing can load it
  ;; before it is mounted. Neither tree can express boot ordering.
  (when (y BR2_TARGET_ROOTFS_EXT2)     (n CONFIG_EXT4_FS_MODULE)))
```

---

## What comes out

```console
$ silt solve images/qemu-arm-dev.sx

SAT   qemu-arm-dev
  stated         9 buildroot, 6 linux
  completed    451 buildroot, 1,847 linux
  opaque         3  carried verbatim, not solved
  env-derived    4  BR2_VERSION BR2_HOSTARCH BR2_BASE_DIR BR2_HOST_GCC_VERSION

  cross-tree rules fired 3:
    BR2_PACKAGE_DHCPCD     → CONFIG_PACKET=y     rules/cross-tree.sx:27
    BR2_TARGET_ROOTFS_EXT2 → CONFIG_EXT4_FS=y    rules/cross-tree.sx:35

$ silt emit images/qemu-arm-dev.sx -o out/
  out/defconfig        18 symbols  → BR2_DEFCONFIG
  out/linux.config     76 symbols  → BR2_LINUX_KERNEL_CUSTOM_CONFIG_FILE
```

One description, both trees, guaranteed consistent. That is the step with no
equivalent today.

```console
$ silt build images/qemu-arm-dev.sx --buildroot ~/go-projects/buildroot
  fixpoint over 449 managed symbols: kbuild changed 0    ✓
  ✓ toolchain  ✓ linux 6.18.7  ✓ busybox  ✓ rootfs      41m18s

$ silt run images/qemu-arm-dev.sx
  Welcome to Buildroot
  buildroot login:
```

Ask before you build:

```console
$ silt consequences images/qemu-arm-dev.sx --add feature:camera

  SAT — 7 symbols change
    + BR2_PACKAGE_LIBCAMERA=y    + BR2_PACKAGE_GNUTLS=y     (select)
    + CONFIG_VIDEO_DEV=m         + BR2_INSTALL_LIBSTDCPP=y  (depends)
    ~ BR2_STATIC_LIBS y → n      (libcamera: depends on !BR2_STATIC_LIBS)

  warning: BR2_STATIC_LIBS flipped, which profile:minimal states deliberately.
           Today kbuild resolves this by deleting BR2_PACKAGE_LIBCAMERA entirely.
```

Ask why anything is the way it is, across both trees:

```console
$ silt why CONFIG_VIDEO_DEV images/edge-camera.sx

CONFIG_VIDEO_DEV = m
  feature:camera                 fragments/features/camera.sx:12
  cross-tree rule                fragments/rules/cross-tree.sx:22
  m not y: (at-least m) allows both; baseline has it m, so m costs 0 changes
```

---

## Checking against the tree

A fragment asserts things that are true of a Buildroot *release*, not of
Buildroot in general. `silt check --buildroot` imports the release's 9,000-odd
symbols and verifies every claim against them:

```console
$ silt check --buildroot ~/buildroot

ok  dev-board          18 symbols checked against buildroot 2025.02.16

edge-camera — 1 problem(s), buildroot 2025.02.16
  fragments/features/camera.sx:10: BR2_PACKAGE_LIBCAMERA requires !BR2_STATIC_LIBS
                                   (package/libcamera/Config.in:7)
  but BR2_STATIC_LIBS is stated y by profile:minimal at fragments/profiles/minimal.sx:25
```

It catches symbols absent from that release, type mismatches, and dependencies
the composition contradicts. Passing `--buildroot` to `emit` as well lets rules
fire on symbols Kconfig's own `select` machinery will enable — a rule on
`BR2_PACKAGE_SYSTEMD` is true of any image choosing `BR2_INIT_SYSTEMD`, which
selects it, even though nothing states it. Images record the release they were checked against
with `(verified-against (buildroot "2025.02.16"))`, and a mismatch is reported
before anything else — the findings only mean something for the tree they were
checked on.

## Building and booting it

Everything above checks configuration: that a symbol exists, that kbuild keeps
it, that two trees agree. None of it can catch a setting which is real, spelled
correctly, passes every check and does nothing — `BR2_TARGET_UBOOT_BOARDNAME`
under the Kconfig build system was exactly that. Only a boot can.

```console
$ ci/boot-test.sh images/qemu-arm-boot.sx --buildroot ~/buildroot
+ silt emit images/qemu-arm-boot.sx -o out-boot --buildroot ~/buildroot --external br2-external
solution 1f5006a8a88f3dae5025920d
+ make -C ~/buildroot O=out-boot/build BR2_EXTERNAL=... defconfig BR2_DEFCONFIG=out-boot/defconfig
+ make -C ~/buildroot O=out-boot/build
+ qemu-system-aarch64 -M virt -cpu cortex-a53 -nographic ...
ok qemu-arm-boot booted and passed 6 assertions
```

The QEMU invocation is not invented: it is read from the `readme.txt` of the
board Buildroot itself ships, so it stays matched to the release in use. The
assertions live in `ci/boot/<image>.expect` and are about what the fragments
claimed — `hello-silt` prints its line because `feature:hello-silt` stated the
symbol that builds it.

Two images, run on demand rather than on every push: a build is tens of minutes,
and a test nobody waits for is a test nobody runs. `qemu-arm-boot` is
`qemu-arm-dev` with one fragment added, so if it boots and `qemu-arm-dev` does
not, the difference is that fragment.

The package it boots comes from a pack: a directory holding fragments, the
`br2-external` tree that implements them, and a declaration of what it offers.

```console
$ silt check --buildroot ~/buildroot --pack packs/hello-silt
ok  pack hello-silt    0.1.0, 1 fragment(s), br2-external
ok  qemu-arm-boot      18 buildroot symbols checked against buildroot 2025.02.16
```

```lisp
(pack hello-silt
  (version "0.1.0")
  (requires (buildroot "2025.02.16"))
  (external "br2-external")
  (provides (feature hello-silt)))
```

`packs/nanopi-r2s` is the same shape with no packages in it at all: a board,
imported from Buildroot's own defconfig, carrying the four files that defconfig
pointed at. Composed with this repository's `profile:minimal`, which knows
nothing about the board, and checked before anyone waits for a build:

```console
$ silt check --buildroot ~/buildroot --pack packs/nanopi-r2s
ok  pack nanopi-r2s     0.1.0, 1 fragment(s), br2-external
ok  nanopi-r2s-minimal  48 buildroot symbols checked against buildroot 2025.02.16
```

A pack can carry files as well as symbols:

```lisp
(path BR2_ROOTFS_OVERLAY "overlay")
```

which is checked to exist before anything builds, emitted as
`BR2_ROOTFS_OVERLAY="$(BR2_EXTERNAL_SILT_PATH)/overlay"` so the defconfig names no
machine's directory layout, and hashed into the solution — an overlay that
changed is a different configuration, even with identical symbols.

That declaration is the interface, and it is checked both ways: everything
promised must exist, and a fragment the pack does not declare is an error —
a consumer can compose it, and the pack does not know it maintains it. `--pack`
loads the fragments and the tree together, because the fragment states
`BR2_PACKAGE_HELLO_SILT` and the tree is what makes that symbol exist.

A checkout with a package added to `package/Config.in` is no longer the release
it claims to be, and every `verified-against` pin here is about a release. The
generated `.br2-external.in.*` files come from Buildroot's own
`support/scripts/br2-external`, not from a reimplementation of its format.

## Asking the model

`silt check` answers conservatively — it reports only explicit contradictions,
because a false positive would block a configuration kbuild accepts. `silt solve`
sees the whole formula at once and catches compositions that are
over-constrained in combination rather than in any single pair:

```console
$ silt solve images/edge-camera.sx --buildroot ~/buildroot

UNSAT  edge-camera — these cannot hold together:

  BR2_PACKAGE_LIBCAMERA = y
      stated by feature:camera at fragments/features/camera.sx:10
  BR2_STATIC_LIBS = y
      stated by profile:minimal at fragments/profiles/minimal.sx:25

  2 of 12 assumptions; the rest are satisfiable without them
```

The narrowing is deletion-based minimisation: each assumption is dropped in turn
and kept only if the rest become satisfiable without it. That is the difference
between a core and an explanation. Each culprit then becomes a candidate repair,
verified rather than assumed, and ranked by the image's `repair-policy`:

```
Repairs, ranked:
  1. drop feature:camera                1 change(s)
  2. drop profile:minimal               6 change(s)
```

Repairs name fragments, not symbols. "Drop feature:camera" is actionable; "set
BR2_PACKAGE_LIBCAMERA=n" leaves you to work out which fragment said it and why.

## Honest scope

**Silt is lossy, and says so on every run.** It models 2 of the 12 Kconfig trees
Buildroot orchestrates. `option env=` makes the model host-dependent. Make expands
values after Kconfig stores them. Patch directories and post-image scripts are
arbitrary shell. The claim is not losslessness:

> No information loss. Bounded reasoning loss. The boundary explicit and reported.

```lisp
(image qemu-arm-dev
  (compose target:qemu-aarch64-virt profile:minimal)
  (opaque      (value BR2_ROOTFS_POST_IMAGE_SCRIPT "board/qemu/post-image.sh"))
  (unmanaged   BR2_TARGET_UBOOT_*)
  (delegate    uboot (custom-config-file "board/qemu/uboot.config"))
  (environment (value BR2_HOST_GCC_VERSION "13 2")))
```

Escape hatches are first-class — `(opaque ...)`, `(unmanaged ...)`, `(delegate ...)`,
and a `silt shell` / `silt absorb` round trip through real `menuconfig`. You are never
trapped. Details in [DESIGN.md §14–15](DESIGN.md).

**Not novel, and that is deliberate.** kclause gives a formal Kconfig semantics and
emits SMT-LIB; `klocalizer --save-dimacs` extracts a formula straight from a kernel
tree; krepair does repair; KConfigReader is a second extractor; undertaker did whole
-system variability years ago. These are not competitors — they are **oracles**. Having
ground truth is a luxury most projects of this kind never get, and the plan is to diff
against them at every stage.

What is actually new is narrow and worth naming: one description emitting both trees
consistently, and cross-tree requirements as machine-checkable rules instead of help
text.

**ARM64 only, permanently.** Kconfig symbol sets are architecture-dependent. Fixing one
architecture removes a whole dimension from the constraint model rather than merely
shrinking the corpus.

```lisp
(y BR2_aarch64)                      ; stated as a choice, in every target
;; (when (= arch arm64) …)           ; never — an arch conditional means scope leaked
```

**Purpose is pedagogical.** The deliverable is a bootable image, but the point is
understanding how a messy real language gets formalized, lowered to CNF, solved, and
explained. Some of the plan — writing a CDCL solver and then deleting it — is tuition,
and is labelled as such.

---

## Status

Nothing is implemented. The ladder in [DESIGN.md §12](DESIGN.md) is deliberately
ordered so a **bootable image arrives at rung 3, before any solver exists**: compose,
emit a flat defconfig, let `olddefconfig` fill the gaps, boot it in QEMU. Every rung
after that replaces a piece of scaffolding with something Silt actually understands.

| | |
| --- | --- |
| 1 | S-expression core — parser, canonical form, hashing |
| 2 | Compose and emit a defconfig |
| **3** | **Bootable image in QEMU** — no solver yet |
| 4 | Kconfig importer, full tristate, provenance |
| 5 | Constraint IR → CNF |
| 6 | A toy CDCL solver, then delete it |
| 7 | MaxSAT completion replaces `olddefconfig` |
| 8 | Explanation and repair — MUS, MCS |

The first real test of the whole thesis is cheap and should happen early: factor five
real Buildroot defconfigs into fragments and check they recompose byte-identically
after `savedefconfig`. If they do not, the premise is wrong and a weekend found out.

**Run, against Buildroot 2025.02.16** (`importer/experiment_test.go`, with
`SILT_BUILDROOT` and `SILT_KBUILD=1`):

- All 290 shipped defconfigs import, compose and emit back to a `.config` identical to
  the one kbuild makes from the source. The model accepts all 290.
- qemu_aarch64_virt, odroidc2, raspberrypi4_64, orangepi_pc2 and pine64, split into
  target and profile and crossed 5×5: kbuild keeps every stated line in all 25.
- The same five imported targets with the hand-written `profile:minimal` and
  `profile:standard`: every line kept, and a deliberately impossible control profile
  fails on every target, in kbuild and in the model.

What this does not show: vendor defconfigs carry almost no userspace policy (imported
profiles are 2–11 lines), so the 5×5 matrix is weaker evidence than it looks, and
"kbuild keeps every line" is not "it builds and boots". It also found that
minimising a fragment with the model is wrong: 1,325 of 6,778 lines in the shipped
defconfigs are forced by the others in the model, and `savedefconfig` keeps every one,
because the model does not distinguish `select` from `depends on`.

---

## Repository

```
fragments/targets/    hardware
fragments/profiles/   userspace policy
fragments/features/   one capability, spanning both Kconfig trees
fragments/rules/      cross-tree implications
images/               compositions
packs/                packs: fragments, their br2-external tree, their tests
ci/                   image checks and the build-and-boot test
tools/                one-off migration scripts
GRAMMAR.md            the complete syntax, in EBNF
DESIGN.md             architecture, loss surface, the ladder
EXAMPLES.md           the language by demonstration
```

Module: `github.com/vinodhalaharvi/silt` · MIT
