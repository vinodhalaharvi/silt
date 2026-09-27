# silt

Composable S-expressions over Kconfig, producing real bootable Buildroot/Linux images.

You describe what you want as reusable fragments. Silt merges them, checks them
against the real Kconfig constraint model, completes them into a valid configuration,
explains anything that conflicts, and emits a `defconfig` plus a kernel `.config` that
Buildroot and kbuild accept unchanged. Buildroot builds it. It boots.

Silt compiles nothing. Its output artifact is a validated configuration.

> **Status: implemented and in use.** The parser, composition model, Kconfig importer,
> CNF lowering, solver, completion, provenance, fixpoint checking, component-policy
> checking and image emission are all real commands. The repository also carries
> bootable QEMU and board images plus a build store that reuses safe Buildroot output
> trees. See [Status](#status).

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
;; fragments/features/camera.sx
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

Anything implied by an existing `depends on` or `select` is omitted. What survives the
rule falls into five categories: **choices**, **strengthening** (Kconfig permits `=m`,
boot order does not), **cross-tree implications**, **negative intent**, and
**capabilities**.

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

`at-least` exists only because this builds real images — module versus built-in changes
what lands in the rootfs. `when` never executes anything; it lowers to an implication
and the solver decides, which is how the representation stays data rather than becoming
a Lisp.

### Reuse without functions

No lambdas, no macros, no parameters. Reuse is **under-specification plus merge**.
A target never names userspace policy; a profile never names hardware. Composing them
unifies the two and each fills the other's hole.

### The cross-tree rules

The file that does what neither Kconfig tree can. Those help strings, made
machine-checkable:

```lisp
(rules cross-tree
  (when (y BR2_PACKAGE_WPA_SUPPLICANT) (at-least m CONFIG_MAC80211))
  (when (y BR2_PACKAGE_LIBCAMERA)      (at-least m CONFIG_VIDEO_DEV))
  (when (y BR2_PACKAGE_DHCPCD)         (y CONFIG_PACKET))
  (when (y BR2_INIT_SYSTEMD)           (y CONFIG_CGROUPS)
                                       (y CONFIG_INOTIFY_USER)
                                       (y CONFIG_FHANDLE)))
```

---

## What comes out

```console
$ silt solve images/qemu-arm-dev.sx --buildroot ~/buildroot
SAT   qemu-arm-dev

$ silt emit images/qemu-arm-dev.sx -o out --buildroot ~/buildroot
composed qemu-arm-dev
wrote out/defconfig
wrote out/linux.config

$ silt complete images/qemu-arm-dev.sx --buildroot ~/buildroot -o out/predicted.config
completed  ... symbols written
```

One description, both trees, checked before a long build starts.

Ask why anything is the way it is, across both trees:

```console
$ silt why CONFIG_VIDEO_DEV images/edge-camera.sx --buildroot ~/buildroot

CONFIG_VIDEO_DEV = m
  feature:camera                 fragments/features/camera.sx:12
  cross-tree rule                fragments/rules/cross-tree.sx:22
```

## Commands

The command line is deliberately small; the source of truth is `silt` with no
arguments:

```text
silt check [PATH...]              parse and validate; report problems
silt emit IMAGE.sx [-o DIR]       compose and write defconfig + linux.config
silt fmt [-w] [PATH...]           canonical form
silt externals IMAGE.sx           BR2_EXTERNAL for that image, colon-joined
silt hash [PATH...]               content address of each file
silt hash --solution IMAGE.sx     content address of a composed image
silt why SYMBOL IMAGE.sx          explain a value and its provenance
silt solve IMAGE.sx               ask the Kconfig model whether it can exist
silt complete IMAGE.sx            predict the full .config kbuild will produce
silt fixpoint IMAGE.sx            compare a real kbuild .config with the image
silt import DEFCONFIG             split a real defconfig into target/profile/image
```

Tree-aware commands take `--buildroot DIR`; `check` can also take `--linux DIR`,
`--external DIR`, and `--pack DIR`. `silt externals` is the bridge to Buildroot:
it prints the colon-joined `BR2_EXTERNAL` value for exactly the packs the composed
image uses, so build scripts do not carry a second hand-maintained list.

---

## Checking against the tree

A fragment asserts things that are true of a Buildroot *release*, not of
Buildroot in general. `silt check --buildroot` imports the release's symbols and
verifies every claim against them:

```console
$ silt check --buildroot ~/buildroot
ok  dev-board          symbols checked against buildroot 2025.02.16
```

It catches symbols absent from that release, type mismatches, dependencies the
composition contradicts, capability mismatches, pack/interface mistakes, and component
policy findings. Passing `--buildroot` to `emit`, `solve`, `complete` and `why` lets
those commands reason against the real tree rather than a fragment-only view.

Images record the release they were checked against with
`(verified-against (buildroot "2025.02.16"))`; a mismatch is reported before anything
else, because findings only mean something for the tree they were checked on.

## Building and booting it

Everything above checks configuration: that a symbol exists, that kbuild keeps it, that
two trees agree. None of it can catch a setting which is real, spelled correctly,
passes every check and does nothing. Only a build and a boot can.

```console
$ ci/boot-test.sh images/qemu-arm-boot.sx --buildroot ~/buildroot
...
ok qemu-arm-boot booted and passed its assertions
```

The assertions live in `ci/boot/<image>.expect` and are about what the fragments
claimed, not merely whether a login prompt appeared.

### Reusing Buildroot work

`ci/silt-build.sh` is the build path for images that would otherwise spend most of
their time rebuilding a board that has already been paid for. It has three answers:

```
hit      this exact image was built before; copy the stored artifact out
prefix   a stored output tree is a safe starting point; build only the difference
miss     no safe stored tree exists; build from nothing
```

`prefix` is intentionally strict. Silt predicts the full Buildroot config for the
stored tree and for the target; a stored tree is considered a prefix only when its
build-affecting predicted config is a **subset of the target's with the same value for
every shared symbol**. Added symbols are fine. A changed value or removed symbol makes
the tree unsafe, because Buildroot's stamp files would otherwise skip work that should
have been rebuilt.

Every non-hit build happens in `$SILT_CACHE/work`. Buildroot output trees are not
relocatable: absolute paths end up in `.config`, generated makefiles, libtool archives
and stamp files. Restoring the same tree under a different path is therefore not a safe
cache operation; one fixed work directory keeps those paths stable, and `-o` receives
the finished image and defconfig rather than the build tree.

`ci/silt-build.sh --list` shows the store and `--prune N` drops old trees while keeping
small image artifacts, so an exact rebuild can remain a hit after its large prefix tree
has been pruned.

The packages an image needs can live in packs: directories holding fragments, the
`br2-external` tree that implements them, tests, and the declaration of what they
offer. `--pack` loads the fragments and the tree together, because a fragment that
states an external symbol and the tree that defines that symbol are two halves of one
thing.

A pack can carry files as well as symbols. Paths are checked before anything builds,
emitted relative to the pack's `BR2_EXTERNAL` root, and included in the solution hash,
so changing an overlay or other owned input changes the solution even when the Kconfig
symbols do not.

## Asking the model

`silt check` answers conservatively — it reports explicit contradictions and invalid
claims. `silt solve` sees the whole formula at once and catches compositions that are
over-constrained only in combination:

```console
$ silt solve images/edge-camera.sx --buildroot ~/buildroot

UNSAT  edge-camera — these cannot hold together:

  BR2_PACKAGE_LIBCAMERA = y
      stated by feature:camera
  BR2_STATIC_LIBS = y
      stated by profile:minimal
```

The useful unit of explanation is provenance: which fragment stated a constraint,
which rule derived another, which component imported a capability, and which tree made
a symbol legal. `silt why` reports that chain instead of returning a naked value.

## Honest scope

**Silt is lossy, and says so.** Buildroot orchestrates more configuration languages
than Silt models. Make expands values after Kconfig stores them. Patch directories and
post-image scripts are arbitrary shell. External hardware facts such as an RS485
transceiver populated on a carrier are not Kconfig symbols at all. The claim is not
losslessness:

> No information loss where Silt claims ownership. Bounded reasoning loss elsewhere,
> with the boundary explicit.

Escape hatches and delegated files exist because pretending arbitrary build machinery
is declarative would be worse than naming the boundary.

**Not novel, and that is deliberate.** Kconfig has existing formal work and extraction
tools; Silt uses the real tree as the authority and checks its own predictions back
against kbuild. The useful part here is the composition boundary: one image joins
hardware facts, Buildroot intent, Linux intent, pack-owned inputs and cross-tree rules,
and the build is checked against that prediction rather than trusted.

Architecture is a property of the target, not a global Silt mode. The repository now
contains targets across more than one ARM generation; features that require an MMU,
64-bit userspace, a CSI connector, RS485 or any other hardware property say so as
capabilities instead of relying on an architecture-wide assumption.

---

## Status

The implementation has passed the point the original design called the ladder. Current
pieces include:

| | |
| --- | --- |
| parser / formatter | S-expression core, canonical form and hashing |
| composition | fragments, images, packs, capabilities and provenance |
| Kconfig model | Buildroot/Linux import, tristate/value handling and cross-tree rules |
| solver | CNF lowering, satisfiability and explanations |
| completion | predicts the full Buildroot/Linux configs before kbuild runs |
| fixpoint | checks those predictions against real `.config` output |
| import | factors real Buildroot defconfigs into Silt fragments and images |
| component policy | checks Wasm component imports against a closed capability vocabulary |
| build store | exact image hits plus monotone safe-prefix reuse of Buildroot output trees |
| boot tests | QEMU assertions and real-board bring-up images |

The important standard is still the original one: **the model is checked against
reality rather than trusted**. `ci/config-agrees.awk` compares the full predicted
Buildroot config with the config kbuild actually wrote before the long build, including
symbols no fragment stated. A cache whose safety depends on the prediction has to prove
the prediction on every build.

---

## Repository

```
fragments/targets/    hardware
fragments/profiles/   userspace policy
fragments/features/   reusable capabilities across trees
fragments/rules/      cross-tree implications
images/               compositions
packs/                fragments, br2-external trees, runtime content and tests
ci/                   image checks, build store, demos and boot tests
component/            Wasm component inspection and policy checking
cnf/                  lowering into the solver representation
compose/              composition, derivation and provenance
fixpoint/             prediction-vs-kbuild checks
importer/             Buildroot defconfig import
solve/                satisfiability and explanation
GRAMMAR.md            the syntax
DESIGN.md             architecture and reasoning model
EXAMPLES.md           the language by demonstration
HANDOFF.md            component/agent-gateway design notes
```

Module: `github.com/vinodhalaharvi/silt` · MIT
