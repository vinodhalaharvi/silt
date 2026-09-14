# Silt by example

The language by demonstration. If something here is awkward to write, that is a
language bug, not a user error.

---

## 1. Where the saving actually is

The honest accounting, because "S-expressions compose nicely" is not by itself an
argument — text fragments already compose, and you can `cat` them.

**Not in the grammar.** Parentheses buy nothing over a `.config` fragment for
expressing `SYMBOL=y`. Any claim that they do is decoration.

**In the combinatorics.** Buildroot ships a couple of hundred defconfigs in
`configs/`, many near-duplicates. 8 boards × 3 profiles × 5 features is 120
hand-maintained files that drift apart, and fixing "minimal userspace" means finding
every defconfig that embodies it. The fragment version is 16 files.

**In the cross-tree unit.** A Buildroot defconfig cannot contain `CONFIG_*` symbols;
those live in a separate kernel fragment in a different format, and nothing checks the
two halves agree. "Camera" as one atomic thing spanning both trees does not exist
today. See `fragments/rules/cross-tree.sx`.

**In saying what Kconfig cannot.** Capabilities. Soft preferences that yield and
report that they yielded. Negative intent that stays enforced when a feature pulls
something in three levels down.

**In refusing to compose.** `merge_config.sh` is last-wins and quiet. Erroring on a
genuine conflict requires knowing a value came from fragment X at line N.

The specific case for *S-expressions* over YAML: imported Kconfig and authored
fragments land in one uniform representation, and you merge, diff, hash and explain
across both constantly. One node type for both sides makes that cheap. Real, but the
weakest of the five — worth being clear about that.

### The experiment that tests this

Before trusting any of the above, factor five real Buildroot defconfigs into fragments
and check they recompose:

```bash
silt factor buildroot/configs/qemu_aarch64_virt_defconfig \
            buildroot/configs/raspberrypi4_64_defconfig \
            ... -o fragments/

silt emit images/*.sx && diff <(make savedefconfig) original_defconfig
```

If they do not round-trip byte-identically after `savedefconfig`, the thesis is in
trouble. That is a weekend, and it should happen early.

---

## 2. The vocabulary

These seven are what you write most of the time. The complete language — thirty-four authored keywords plus nine the importer emits — is in [GRAMMAR.md](GRAMMAR.md).

| Form | Meaning | Lowers to |
| --- | --- | --- |
| `(y SYM)` | hard: built in | `sym_y` |
| `(m SYM)` | hard: module | `sym_m` |
| `(n SYM)` | hard: off | `¬sym_y ∧ ¬sym_m` |
| `(at-least m SYM)` | module or built-in | `sym_y ∨ sym_m` |
| `(prefer y SYM)` | soft; yields, and says so | MaxSAT soft clause |
| `(value SYM "str")` | uninterpreted (string/int/hex) | passthrough, not solved |
| `(when A B)` | guarded | `A ⇒ B` |

`at-least` exists only because this project builds real images. Under the y/m collapse
that kclause uses, the form would be meaningless — and `CONFIG_MAC80211` as module
versus built-in changes what ends up in the rootfs.

`when` never executes anything. It lowers to an implication and the solver decides.
That is how the representation stays data. It appears exactly once in the library, in
`rules/cross-tree.sx`; anywhere else it has so far turned out to be restating a
`select`.

Scopes: `(buildroot ...)` and `(linux ...)` are separate trees with separate
namespaces, solved as two models joined by capabilities.

---

## 3. Reuse without functions

No lambdas, no macros, no parameters. Reuse is **under-specification plus merge**.

`target:qemu-aarch64-virt` never mentions a rootfs format. `profile:minimal` never
mentions hardware. Composing them unifies the two and each fills the other's hole.

The portability claim is testable:

```bash
silt diff images/dev-board.sx --swap target:qemu-aarch64-virt=target:rpi4-64
```

If that swap requires editing a feature, the boundaries are wrong.

---

## 4. Compose and emit (Rung 2)

```console
$ silt emit images/dev-board.sx --scope buildroot -o out/defconfig

composed dev-board
  target:qemu-aarch64-virt      fragments/targets/qemu-aarch64-virt.sx
  profile:minimal               fragments/profiles/minimal.sx
  feature:networking            fragments/features/networking.sx

capabilities  mmu virtio serial-console
  profile:minimal requires mmu                      ✓
stated        9 buildroot, 6 linux
overrides     1  (BR2_TARGET_ROOTFS_EXT2_SIZE, images/dev-board.sx:17)

wrote out/defconfig
```

Nine stated Buildroot symbols become a few hundred after completion. That ratio is the
saving, and it is worth printing on every run so it stays honest.

---

## 5. Merge conflicts are errors

The first thing Silt must beat `merge_config.sh` at.

```console
$ silt emit images/dev-board.sx

error: conflicting values for BR2_PACKAGE_SYSTEMD

  n   profile:minimal        fragments/profiles/minimal.sx:18
  y   feature:logging        fragments/features/logging.sx:6

Neither is an override. Resolve by:
  - composing profile:standard instead, or
  - adding an explicit (override (y BR2_PACKAGE_SYSTEMD)) to images/dev-board.sx
```

Both locations, no silent winner, nothing built.

---

## 6. Boot it (Rung 3 — before any solver exists)

```bash
silt emit images/dev-board.sx --scope buildroot -o out/defconfig
cd buildroot
make defconfig BR2_DEFCONFIG=$PWD/../out/defconfig
make olddefconfig          # kbuild completes the partial config, for now
make

qemu-system-aarch64 -M virt -cpu cortex-a53 -nographic -smp 1 \
  -kernel output/images/Image -append "rootwait root=/dev/vda console=ttyAMA0" \
  -netdev user,id=eth0 -device virtio-net-device,netdev=eth0 \
  -drive file=output/images/rootfs.ext4,if=none,format=raw,id=hd0 \
  -device virtio-blk-device,drive=hd0
```

A shell prompt here is the first real milestone, reached with no constraint model and
no solver.

---

## 7. The validation loop (Rung 7)

Once Silt completes the configuration itself, `olddefconfig` stops being the mechanism
and becomes the oracle:

```console
$ silt check images/dev-board.sx --against buildroot/

emitted 412 symbols, kbuild changed 3

  BR2_PACKAGE_DHCPCD           y → n
    kbuild dropped it: depends on BR2_USE_MMU && BR2_TOOLCHAIN_HAS_THREADS
    silt model left BR2_TOOLCHAIN_HAS_THREADS unconstrained
    → importer bug: select-implied symbol not propagated

  BR2_TARGET_ROOTFS_EXT2_SIZE  "256M" → "256M"   (quoting normalized, benign)
  BR2_SYSTEM_DHCP              "eth0" → "eth0"   (quoting normalized, benign)

FAIL — 1 semantic mismatch
```

Fixpoint is the criterion. kbuild altering anything means the model is wrong, and the
diff names the symbol and therefore the rule.

---

## 8. Explaining a failure (Rung 8)

`images/edge-camera.sx` is checked in expecting UNSAT:

```console
$ silt solve images/edge-camera.sx

UNSAT — edge-camera cannot be realized.

  profile:minimal requires BR2_STATIC_LIBS=y   fragments/profiles/minimal.sx:22
  feature:camera requires BR2_PACKAGE_LIBCAMERA
                                       fragments/features/camera.sx:11
    └── depends on !BR2_STATIC_LIBS    buildroot/package/libcamera/Config.in:14
    └── depends on BR2_TOOLCHAIN_HAS_THREADS
                                       buildroot/package/libcamera/Config.in:11
    └── depends on BR2_INSTALL_LIBSTDCPP
                                       buildroot/package/libcamera/Config.in:10

minimal unsatisfiable set: 2 constraints

Repairs, ranked by (minimize changed-symbols):
  1. profile:minimal: BR2_STATIC_LIBS y → n       1 change
  2. drop feature:camera                          1 change
  3. profile:minimal → profile:standard           4 changes

Today kbuild answers this by deleting BR2_PACKAGE_LIBCAMERA from the .config
entirely — no "is not set" line, exit 0, no stderr. Verified; see DESIGN.md §14.5.
```

Note what makes this readable: the chain crosses from a fragment the user wrote into
Buildroot's own Kconfig and back. That is what the provenance requirement in
DESIGN.md §11.1 buys, and why it cannot be retrofitted.

---

## 9. Explaining a success

Equally important, and the real advantage over reading a 3,000-line `.config`:

```console
$ silt why CONFIG_MAC80211 images/edge-camera.sx

CONFIG_MAC80211 = m

  required by  feature:wifi            fragments/features/wifi.sx:8
  also implied by cross-tree rule      fragments/rules/cross-tree.sx:19
    (when (y BR2_PACKAGE_WPA_SUPPLICANT) (at-least m CONFIG_MAC80211))

  m rather than y: (at-least m) permits both; the completion solver chose m
  because (minimize changed-symbols) scores it against the baseline defconfig,
  where it is already m.
```

---

## 10. Escape hatches

Silt models 2 of the 12 Kconfig trees Buildroot orchestrates, cannot evaluate make
expansion, and cannot reason about patch directories or post-image scripts. See
DESIGN.md §14. Those losses are declared rather than hidden, and every run reports
them:

```console
$ silt solve images/qemu-arm-dev.sx

SAT   qemu-arm-dev
  stated         9 buildroot, 6 linux
  completed    451 buildroot, 1,847 linux
  opaque         3  carried verbatim, not solved
  unmanaged      2 prefixes → 0 symbols present
  env-derived    4  BR2_VERSION BR2_HOSTARCH BR2_BASE_DIR BR2_HOST_GCC_VERSION
                    recorded in the solution hash; model is host-specific
```

When Silt cannot express something, leave and come back:

```console
$ silt shell images/qemu-arm-dev.sx     # real make menuconfig
$ silt absorb images/qemu-arm-dev.sx

  9 changes
    6 representable → proposed fragment edits (review)
    2 unmanaged     → BR2_TARGET_UBOOT_* already declared, ignored
    1 opaque        → BR2_TARGET_UBOOT_CUSTOM_CONFIG_FILE recorded verbatim
```

The claim is not losslessness. It is: no information loss, bounded reasoning loss,
boundary reported every run.

---

## 11. Asking before building

The 41 minutes get paid back here.

```console
$ silt consequences images/qemu-arm-dev.sx --add feature:camera

  SAT — 7 symbols change
    + BR2_PACKAGE_LIBCAMERA=y     + BR2_PACKAGE_GNUTLS=y      (select)
    + BR2_PACKAGE_LIBYAML=y       + BR2_INSTALL_LIBSTDCPP=y   (depends)
    + CONFIG_VIDEO_DEV=m          + CONFIG_MEDIA_SUPPORT=y
    ~ BR2_STATIC_LIBS y → n       (libcamera: depends on !BR2_STATIC_LIBS)

  warning: BR2_STATIC_LIBS flipped, which profile:minimal states deliberately.
           Today kbuild resolves this by deleting BR2_PACKAGE_LIBCAMERA from the
           .config entirely — exit 0, no stderr. See DESIGN.md §14.5.
```

Note what this is *not* claiming: `make olddefconfig` also runs in seconds. The gap
is not speed. It is that kbuild answers by silently rewriting the request, within one
tree only, with no explanation.
