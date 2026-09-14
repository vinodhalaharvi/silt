# Silt

## Composable S-expressions over Kconfig, producing real bootable ARM64 images

**Status:** Pre-implementation
**Purpose:** Pedagogical — but the output is a real image, not a report
**Implementation language:** Go
**Architecture scope:** ARM64 (`aarch64`) only
**Corpus:** Linux `arch/arm64` Kconfig, then Buildroot
**Execution backend:** kbuild and Buildroot. Silt does not build anything itself.

---

# 1. What This Project Is

Silt is a **composition frontend** for Kconfig-based systems.

You describe what you want as composable S-expressions. Silt merges those
descriptions, checks them against the real Kconfig constraint model, completes them
into a full valid configuration, explains anything that conflicts, and emits a
`.config` / `defconfig` that Buildroot and kbuild accept unchanged.

Then Buildroot builds it and it boots.

```text
S-expression fragments        (target, profile, features — composable, reusable)
        ↓  merge
requirement set
        ↓  + imported Kconfig constraint model
solve / complete
        ↓
resolved assignment
        ↓
defconfig  ──▶  buildroot  ──▶  output/images/  ──▶  qemu-system-aarch64
        │
        └──▶  explanation + repair when it doesn't work
```

The motivation is composition. Tens of thousands of configuration symbols are managed
today by textual fragment overlays and hand-edited defconfigs. Silt asks whether a
typed, checked, composable representation does that job better — and proves it by
producing an image that boots.

The project is a learning exercise. That governs *scope*, not *ambition*: the end
artifact is a real bootable image, produced by a real build system, from a
configuration Silt composed and solved.

---

# 2. The Core Bet

> **Silt's output artifact is a validated `.config`. Nothing further.**

Building a Linux image is a solved problem with decades of accumulated knowledge —
toolchain bootstrap, sysroot semantics, package patches, cross-compilation, bootloader
integration. Reimplementing that teaches nothing that Silt is trying to teach, and
would consume the entire project.

So Silt does not build. It composes, reasons, and emits. Buildroot does the rest.

This is what makes the project tractable *and* what makes it produce something real.
Every hard part of image construction is delegated. Every hard part of configuration
composition and reasoning is kept.

Corollary: Silt is correct exactly when kbuild accepts its output without changing it.
That gives a precise, automatable definition of correctness — see §10.

---

# 3. What This Project Is Not

- Not a Yocto replacement.
- Not a build system. No DAG, no scheduler, no CAS, no remote execution. Those were in
  an earlier draft of this document and are removed.
- Not a Kconfig reimplementation for its own sake. The native evaluator stays
  authoritative; Silt adds reasoning and composition on top of it.
- Not novel. See §5.

---

# 4. Scope: ARM64 Only

Every symbol set in Kconfig is architecture-dependent. The kernel carries dozens of
architecture-specific specifications, and parsing requires `ARCH`/`SRCARCH` to be
fixed before a single symbol resolves.

**Decision: `arm64` only, everywhere, permanently for this project.**

This is not a limitation to apologize for. It removes an entire dimension from the
constraint model: one architecture, one formula, no cross-architecture conditionals to
reason about.

`arm64` specifically, not 32-bit `arm`, because:

- no legacy per-machine option sprawl;
- platform selection is a clean set of `ARCH_*` symbols;
- it is what modern embedded targets actually use;
- Buildroot has first-class QEMU support for it.

**Concrete targets, in order:**

1. `qemu_aarch64_virt_defconfig` — boots under `qemu-system-aarch64 -machine virt`.
2. A Raspberry Pi 64-bit defconfig — real hardware, same reasoning layer.

Nothing else. No x86 "just to check." If a design decision only makes sense across
architectures, it is out of scope.

---

# 5. Prior Art — Read It, Then Use It As An Oracle

The reasoning half of this has been done in public by people who wrote papers about it.
That is a feature: it means ground truth is available, which hobby projects almost
never have.

| Tool | What it does | How Silt uses it |
| --- | --- | --- |
| **kclause** (kmax suite) | Formal semantics of Kconfig; symbolic evaluator emitting SMT-LIB2. Validated against full Linux trees. | Ground truth for the constraint model |
| **klocalizer** | `--save-dimacs` / `--save-smt` extract a formula straight from a kernel tree | Baseline formula to diff against |
| **kismet** | Finds unmet-dependency bugs from kclause models | Shows what the formulas are good for |
| **krepair** | Configuration repair | Baseline for §11 |
| **KConfigReader** | Independent Kconfig → formula extractor | Second opinion; where it and kclause disagree, the semantics are genuinely ambiguous |
| **undertaker** | Kconfig + `#ifdef` + build rules → SAT, finds dead code/files/symbols | Prior art for whole-system variability |
| **`merge_config.sh`** | kbuild's own fragment merger | The thing Silt's composition layer must beat — see §7.1 |

**The rule:** read how these did it before implementing a stage; diff against them
after.

---

# 6. Learning Objectives

1. **Canonicalization and content addressing** — what "semantically identical" must
   mean concretely for two structures to hash the same.
2. **Formalizing a real language** — Kconfig has a spec, an implementation, and a gap.
   Living in that gap is the point.
3. **Lowering to logic** — Tseitin transformation, CNF, encoding choices and their cost.
4. **How SAT actually works** — unit propagation, CDCL, and therefore where unsat cores
   come from.
5. **Evidence to explanation** — MUS, MCS, and the hitting-set duality between them.
6. **Configuration completion as optimization** — MaxSAT, and why `make olddefconfig` is
   a greedy approximation of it.
7. **Language design under constraint** — composable and reusable without becoming a
   programming language (§7).

---

# 7. The S-Expression Composition Layer

This is the motivating idea, so it gets the most design attention.

Fragments are declarative, composable, and reusable:

```lisp
(target aarch64-virt
  (arch arm64)
  (machine qemu-virt)
  (bootloader none))

(profile minimal
  (libc musl)
  (init busybox)
  (rootfs ext2))

(feature networking
  (require BR2_PACKAGE_DHCPCD)
  (require CONFIG_NET)
  (prefer  CONFIG_IPV6 y))

(image dev-board
  (compose target:aarch64-virt
           profile:minimal
           feature:networking))
```

Imported Kconfig, after canonicalization, lives in the same representation with
provenance attached:

```lisp
(symbol CONFIG_ARM64_MTE
  (type bool)
  (depends (and CONFIG_ARM64 CONFIG_AS_HAS_LSE_ATOMICS))
  (source "arch/arm64/Kconfig" 2104))
```

## 7.1 What this has to beat

kbuild's `merge_config.sh` already overlays configuration fragments. It is textual and
last-one-wins: a later fragment silently overrides an earlier one, and symbols whose
dependencies are unmet are quietly dropped by the subsequent `olddefconfig`. You find
out what you actually got by reading the resulting `.config`.

Silt's composition must be strictly better on exactly these points:

- **Merge conflicts are errors, not silent overrides.** If two fragments want different
  values for the same symbol, that is reported with both source locations.
- **Unmet dependencies are explained, not dropped.** If a requested symbol cannot be
  enabled, you get §11's output, not silence.
- **Composition is checked before the build**, against the real constraint model.

If Silt cannot beat `merge_config.sh` on these three, the composition layer has no
reason to exist. That is a real and early finding, and worth knowing in week three
rather than month nine.

## 7.2 The composition/evaluation tension

> Reuse needs abstraction. Abstraction needs parameters. Parameters need substitution.
> Substitution is evaluation. Now you have an interpreter.

Four languages sit at different points on this spectrum and studying their choices is
worth more than another draft of this section:

- **Dhall** — total, explicitly non-Turing-complete, with functions, types, imports.
- **CUE** — constraints and data are the same thing, combined by a commutative,
  idempotent lattice operation. Closest to what Silt wants, since Silt's values and
  constraints-over-values also want one language.
- **Nickel** — gradual types and contracts over a functional core.
- **Jsonnet / Starlark** — accept evaluation, constrain it (hermetic, no I/O).

**Working decision:** start at the CUE end. Composition is **merge/unification** of
declarative structures. No lambdas, no macros, no arbitrary evaluation. Merge conflict
is a first-class, explainable outcome — which is exactly the behaviour §7.1 demands.

Revisit only when a concrete Kconfig pattern cannot be expressed, and record that
pattern here when it happens.

Non-negotiable regardless: no host execution during parsing, no I/O during evaluation,
evaluation is total, results are canonicalizable and hashable.

---

# 8. Kconfig Semantics: Decisions, Not Deferrals

The two places this gets genuinely hard. Both are decided here, and both decisions
changed once the goal became *generating* configurations rather than analyzing them.

## 8.1 `select`

Kconfig's `select` forcibly enables a symbol **without visiting that symbol's own
dependencies**. A configuration can be reached where a selected symbol's stated
`depends on` is violated. This is a known wart, discussed upstream repeatedly.

Consequence: there is no single logical theory both internally consistent and faithful
to the native evaluator.

| | Models | Consequence |
| --- | --- | --- |
| **A. Spec-faithful** | `select` implies the target's dependencies | Clean theory; sometimes disagrees with `menuconfig` |
| **B. Implementation-faithful** | `select` ignores them, as the C code does | Matches reality; theory admits configs violating stated deps |

**Decision: B, with A available as a checking mode.**

For generation this is forced, not merely preferred: Silt must emit what kbuild will
accept *unchanged*, and kbuild implements B. Modeling A would produce configurations
that kbuild then silently rewrites, which breaks the correctness criterion in §10.

The disagreements between A and B are themselves useful output — they are close to what
`kismet` reports as unmet-dependency bugs.

## 8.2 Tristate — revised

An earlier draft proposed collapsing `m` into `y` (one Bool per symbol), matching
kclause, which underapproximates tristate to cut complexity. **That is wrong for this
project.**

If the output is a real image, the difference between built-in and module is the entire
point: module choice drives rootfs size, boot time, initramfs contents, and whether the
thing boots at all.

**Decision: full tristate. Two Booleans per symbol (`sym_y`, `sym_m`) plus at-most-one,
with `n < m < y` ordering and min/max semantics on `depends on`.**

This roughly doubles the variable count. That cost is accepted. It also means formula
comparison against kclause is not apples-to-apples — a `y`/`m`-collapsed projection of
Silt's model is generated specifically for that diff (§10).

## 8.3 Out of scope

`string`, `int`, and `hex` symbols are **not solved**, but they cannot simply be
ignored either.

Verified against a Buildroot checkout: **154 places in Buildroot's own Kconfig compare
a string symbol inside a dependency expression.** These are not help text — they are
load-bearing:

```kconfig
depends on BR2_TARGET_OPENSBI_PLAT != ""
default y if BR2_TARGET_UBOOT_DEFAULT_ENV_FILE != ""
depends on BR2_TOOLCHAIN_BARE_METAL_BUILDROOT_ARCH != "microblazeel-buildroot-elf"
```

An earlier draft of this document excluded string symbols from the constraint model
outright. That was a soundness hole, not merely a reasoning limit: the model would be
missing constraints kbuild enforces, so Silt could report SAT for a configuration
kbuild then rewrites, and the §10 fixpoint diff would point at a symbol whose governing
rule was never in the model.

**Decision, in two tiers:**

1. **Emptiness is modeled.** `!= ""` and `== ""` become a Boolean "is this string set",
   one variable per string symbol. This covers the large majority of the 154 without
   needing any string theory.
2. **Equality against a literal stays opaque** — but is *declared* opaque and reported,
   never silently dropped. Silence here would reinvent the exact failure mode
   documented in §14.5.

Values themselves pass through to the emitted `.config` unchanged.

`range` applies only to int/hex and remains out of scope.

In scope: `bool`, `tristate`, `depends on`, `select`, `imply`, `choice`, `default`,
`visible if`.

---

# 9. Generation: Completion as MaxSAT

A requirement set is a **partial** assignment. Producing a `.config` means completing
it to a total, valid one. This is the core generation problem.

```text
hard constraints  =  Kconfig semantics (§8)
                  +  user requirements  (require / forbid)

soft constraints  =  Kconfig defaults
                  +  user preferences   (prefer)
                  +  minimize deviation from a base defconfig

                  ↓  MaxSAT

total valid assignment
```

`make olddefconfig` solves this procedurally and greedily: walk symbols in order, take
the first default whose condition holds. That works, and it is why the vertical slice
in §12 can use it directly before Silt has a solver at all.

A solver does it globally, which buys three things `olddefconfig` cannot give:

1. **It can fail loudly.** An over-constrained requirement set is UNSAT, with a core,
   instead of quietly producing a config missing what you asked for.
2. **It can optimize.** "Smallest deviation from `qemu_aarch64_virt_defconfig`" is a
   well-posed objective; greedy ordering has no notion of it.
3. **It can enumerate.** More than one completion may satisfy the requirements, and the
   alternatives are showable.

Optimize only statically known quantities: changed-symbol count, package count, explicit
preferences. Image size and boot time are *observed outputs* — measurable once §12's
vertical slice exists, and only then eligible as objectives.

---

# 10. The Validation Loop

This is the project's correctness oracle, and it is nearly free.

```text
Silt emits defconfig
        ↓
make defconfig BR2_DEFCONFIG=<silt output>
        ↓
make olddefconfig
        ↓
diff .config against Silt's predicted assignment
        ↓
any difference  =  Silt's model is wrong, here, at this symbol
```

**Fixpoint is the criterion:** if kbuild changes anything Silt emitted, Silt was wrong,
and the diff points at exactly which symbol and therefore which semantic rule.

This beats randomized differential testing because it is automatic, it is exact, and it
localizes the failure to a single symbol without any search.

Round-tripping through `make savedefconfig BR2_DEFCONFIG=<path>` additionally tests
minimality — it strips values equal to their defaults, so Silt's own notion of
"minimal defconfig" can be compared against kbuild's. Note that `savedefconfig` has
historically been reported to drop symbols users expected to survive; disagreements
here are interesting rather than automatically Silt's fault, and belong in `testdata/`
with a comment.

Three oracles in total:

1. **kbuild fixpoint** (above) — behavioral, exact, primary.
2. **kclause / klocalizer** — formula-level, on the `y`/`m`-collapsed projection.
3. **KConfigReader** — second formula extractor; its disagreements with kclause mark
   genuinely ambiguous semantics.

---

# 11. Explanation and Repair

Explanation exists here to serve generation. When a composition cannot be realized, the
answer must be actionable.

## 11.1 Provenance is not a later feature

Every solver assertion traces back through every transformation:

```text
Kconfig statement (file:line)  |  S-expression fragment (file:line)
              ↓
      canonical S-expression node
              ↓
      normalized constraint
              ↓
      CNF clause
              ↓
      solver assertion label
```

Retrofitting this means touching every stage, so it goes in from the first commit.

## 11.2 Target output

```lisp
(image dev-board
  (compose target:aarch64-virt profile:minimal feature:camera))
```

```text
$ silt solve dev-board.sx

UNSAT — dev-board cannot be realized.

  feature:camera requires BR2_PACKAGE_LIBCAMERA      features/camera.sx:4
    └── requires BR2_TOOLCHAIN_HAS_THREADS = y
          └── profile:minimal sets BR2_PTHREAD_DEBUG = n
                                                      profiles/minimal.sx:11

Minimal repair:
  profile:minimal: enable threads                    (1 change)

Alternatives:
  drop feature:camera                                (1 change)
  target:aarch64-virt → target:rpi4-64               (7 changes)
```

## 11.3 The algorithm

1. UNSAT → extract core.
2. Core → **MUS** (minimal unsatisfiable subset), via deletion-based MUS or QuickXplain.
3. All MUSes → **MCS** (minimal correction sets) as **minimal hitting sets** of the MUSes.
4. Each MCS is a candidate repair; rank by policy.

```lisp
(repair-policy
  (minimize changed-symbols)
  (prefer preserve-target)
  (prefer preserve-toolchain))
```

## 11.4 SAT results need explaining too

`why` matters as much as `why-not`: which fragment forced this symbol on, which
alternatives existed, what rejected the other branch of a `choice`. A generated
configuration should be auditable, not merely valid — that is the real advantage over
reading a 3,000-line `.config` by hand.

---

# 12. The Ladder

Each rung is small and independently satisfying. **Do not skip rungs, and do not start
the next before the current one runs.**

The ordering is deliberate: **a bootable image comes before the solver.** The vertical
slice proves the whole pipeline early, with `olddefconfig` standing in for reasoning
Silt cannot yet do. Everything afterwards replaces a piece of that scaffolding with
something Silt actually understands.

### Rung 1 — S-expression core

Parser, AST, canonical serialization, hashing. One evening.
*Done when:* two differently-written equivalent expressions hash identically, and
canonical round-trip is stable.

### Rung 2 — Compose and emit

Merge fragments (§7), emit a flat `BR2_*=y` defconfig. No constraint model yet, no
solver — just unification and text output.
*Done when:* `make defconfig BR2_DEFCONFIG=out/defconfig` accepts it.

### Rung 3 — Vertical slice: a booting image

```bash
silt emit dev-board.sx > out/defconfig
make defconfig BR2_DEFCONFIG=$PWD/out/defconfig
make
qemu-system-aarch64 -M virt -cpu cortex-a53 -nographic -smp 1 \
  -kernel output/images/Image -append "rootwait root=/dev/vda console=ttyAMA0" \
  -netdev user,id=eth0 -device virtio-net-device,netdev=eth0 \
  -drive file=output/images/rootfs.ext4,if=none,format=raw,id=hd0 \
  -device virtio-blk-device,drive=hd0
```

*Done when:* it boots to a shell. **This is the project's first real milestone**, and it
arrives before any solver exists.
*Teaches:* where the actual seams are, which is not where a design document guesses.

### Rung 4 — Kconfig importer

Import `arch/arm64` Kconfig into canonical S-expressions with `file:line` provenance.
Full tristate (§8.2), implementation-faithful `select` (§8.1).
*Done when:* imported symbol values match the native evaluator on a generated test set.

### Rung 5 — Constraint IR and CNF

Lower to constraint form, Tseitin-transform to CNF by hand. Roughly 200 lines.
*Done when:* an off-the-shelf solver accepts it, and the `y`/`m`-collapsed projection
agrees with `klocalizer --save-dimacs` on satisfiability.

### Rung 6 — A toy CDCL solver

Write one. Roughly 300 lines: unit propagation, clause learning, backjumping.

This deliberately contradicts the standard advice never to write your own solver. That
advice is right for a product and exactly inverted for learning — unsat cores stop
being magic once you have watched a learned clause come out of a conflict.

*Done when:* it agrees with MiniSat/CaDiCaL on the Rung 5 corpus. **Then delete it and
link a real solver.**

### Rung 7 — Completion replaces `olddefconfig`

MaxSAT completion (§9). Silt now emits a *total* assignment.
*Done when:* the §10 validation loop reaches fixpoint — kbuild changes nothing — and the
image from Rung 3 still boots, now built from a config Silt solved rather than one
`olddefconfig` filled in.

### Rung 8 — Explanation and repair

MUS, MCS via hitting sets, `why` / `why-not` / `repair`.
*Done when:* §11.2's output is produced for a real conflict, and is more useful than a
raw core.

### Later, optional

Raspberry Pi 64-bit target; counterfactual queries (`what breaks if I disable X`);
solver-result caching; measured objectives now that images are actually being built;
ASP/SMT backend comparison. None are on the critical path.

---

# 13. Repository Layout

Only what exists or is being built this week. Directories appear when their first real
file is written, not in advance.

```text
silt/
├── sexpr/          # rung 1: lexer, parser, ast, canonical, hash
├── compose/        # rung 2: fragment merge, unification, conflict reporting
├── emit/           # rung 2: defconfig / .config output
├── kconfig/        # rung 4: importer, symbol, provenance
├── constraint/     # rung 5: expr, normalize, cnf
├── solver/         # rung 6: interface, cdcl (temporary), external
├── complete/       # rung 7: maxsat completion
├── explain/        # rung 8: why, whynot, mus, repair
├── cmd/silt/
├── fragments/      # composable .sx library: targets, profiles, features
├── testdata/       # captured arm64 Kconfig subtrees + expected results
├── DESIGN.md
├── README.md
└── go.mod
```


---

# 14. The Loss Surface

Silt is a lossy view of Kconfig/kbuild. Every item below was verified against a real
Buildroot checkout; the commands to re-verify are given so this section can be
re-checked rather than trusted.

## 14.1 Silt models 2 of 12 Kconfig trees

Buildroot orchestrates separate Kconfig-based configurations for Linux, BusyBox,
uClibc, U-Boot, Barebox (×2), Xvisor, at91bootstrap3, TI K3 R5 loader, linux-backports,
exim and UBI — each with its own `_CUSTOM_CONFIG_FILE` / `_CONFIG_FRAGMENT_FILES` pair.

```sh
grep -rhoE 'BR2_[A-Z0-9_]*_(CUSTOM_CONFIG_FILE|CONFIG_FRAGMENT_FILES)' \
  --include='Config.in' . | sort -u
```

The cross-tree rules in `fragments/rules/cross-tree.sx` cover Buildroot↔Linux. The
other ten are carried opaquely.

## 14.2 The model is host-dependent

`option env=` gives four symbols their values from the environment at parse time.
Note the symbol and the environment variable have different names:

```kconfig
config BR2_HOSTARCH          option env="HOSTARCH"
config BR2_BASE_DIR          option env="BASE_DIR"
config BR2_VERSION           option env="BR2_VERSION_FULL"
config BR2_HOST_GCC_VERSION  option env="HOST_GCC_VERSION"
```

`BR2_HOST_GCC_VERSION` is load-bearing, and it is consumed by *string comparison
against a literal* — the §8.3 tier-2 case and host-dependence in one line:

```kconfig
config BR2_HOST_GCC_AT_LEAST_4_9
	bool
	default y if BR2_HOST_GCC_VERSION = "4 9"
```

Worse, the *shape of the tree* depends on the environment too. `Config.in` contains:

```kconfig
source "$BR2_BASE_DIR/.br2-external.in.paths"
```

Silt cannot know which `Config.in` files to parse without first resolving an
environment variable, and that file is generated by make. The importer must therefore
take the environment as an explicit, recorded input rather than reading it implicitly.

The constraint model is consequently not a pure function of the source tree, and
env-derived values must be recorded *in* the solution hash (Invariant 10).

## 14.3 Values are not final until make runs

`qemu_aarch64_virt_defconfig` contains:

```
BR2_ROOTFS_POST_SCRIPT_ARGS="$(BR2_DEFCONFIG)"
```

Kconfig stores the literal; make expands it later. Silt cannot evaluate it and must
not try.

## 14.4 Arbitrary shell is part of the configuration

`BR2_GLOBAL_PATCH_DIR` and `BR2_ROOTFS_POST_IMAGE_SCRIPT` name a patch directory and a
shell script. Patches can change a package's dependencies; the post-image script runs
*after* Silt's model is finished and can rewrite the image. Neither is expressible as
a constraint, and Silt's solution says nothing about what either does.

## 14.5 What kbuild does with an impossible request

Verified empirically. Starting from a working `qemu_aarch64_virt_defconfig`, append
`BR2_PACKAGE_LIBCAMERA=y` and `BR2_STATIC_LIBS=y` (libcamera has
`depends on !BR2_STATIC_LIBS`) and run `make olddefconfig`:

```
exit=0
stderr: 0 bytes
BR2_PACKAGE_LIBCAMERA    ABSENT ENTIRELY
```

Not `# BR2_PACKAGE_LIBCAMERA is not set`. The symbol is removed from the file. You
asked for a camera and got a config that builds cleanly without one.

This is the single clearest justification for the project, and it is also the standard
Silt is held to: **if Silt's own losses are silent, Silt has reproduced the defect it
exists to catch.**

## 14.6 Buildroot expresses kernel requirements in English

Nineteen package `Config.in` files reference kernel `CONFIG_*` symbols. Every single
reference is inside a `help` block, because Buildroot's Kconfig cannot reach the
kernel's namespace:

```
fscryptctl     "requires a kernel with CONFIG_EXT4_ENCRYPTION=y"
18xx-ti-utils  "CONFIG_NL80211_TESTMODE must be enabled in the kernel"
bcc            "Compile kernel with CONFIG_IKHEADERS"
dpdk            nine symbols under "Optional but recommended kernel configurations"
```

Nothing enforces any of them. These help strings are the source corpus for
`fragments/rules/cross-tree.sx`, and they are incomplete by construction — they are
only the packages whose authors bothered to document it.

---

# 15. Escape Hatches

Silt must never be a wall. The governing rule is §14.5's: losses are **visible and
counted**, never silent.

## 15.1 Three declarations

```lisp
;; carried verbatim to output, never solved, reported in every run
(opaque
  (value BR2_GLOBAL_PATCH_DIR "board/qemu/patches")
  (value BR2_ROOTFS_POST_IMAGE_SCRIPT "board/qemu/post-image.sh"))

;; symbol prefixes Silt does not own; excluded from the §10 fixpoint diff
(unmanaged BR2_TARGET_UBOOT_* BR2_PACKAGE_BUSYBOX_CONFIG_*)

;; hand a whole Kconfig tree to its native tooling
(delegate linux (custom-config-file "board/qemu/aarch64-virt/linux.config"))
```

## 15.2 Round trip

```console
$ silt shell images/dev-board.sx      # drops into real make menuconfig
$ silt absorb images/dev-board.sx     # re-import what changed

  9 changes
    6 representable → proposed fragment edits (review)
    2 unmanaged     → already declared, ignored
    1 opaque        → recorded verbatim
```

`absorb` is what makes the lossiness survivable: you can always leave for the native
tool and come back, and Silt states exactly what it can and cannot carry.

## 15.3 The honest claim

Not "lossless". The accurate property is:

> **No information loss. Bounded reasoning loss. The boundary is explicit and
> reported on every run.**


---

# 16. Invariants

1. **S-expressions are data.** No host execution, no I/O, no unbounded evaluation.
2. **Silt emits configuration, never artifacts.** kbuild and Buildroot build. If a
   feature requires Silt to compile something, it is out of scope.
3. **Provenance survives every transformation.** Every constraint traces to a
   `file:line`, in Kconfig or in a fragment.
4. **Canonical form is deterministic.** Same meaning, same bytes, same hash.
5. **The Constraint IR is solver-independent.** Frontend syntax never leaks into a
   backend; the backend is replaceable.
6. **kbuild is the oracle over managed symbols.** If kbuild alters a managed symbol,
   Silt is wrong. Opaque, unmanaged and env-derived symbols are excluded from the
   diff *by declaration*, never by accident — an undeclared exclusion is
   indistinguishable from a bug.
7. **Semantic decisions are written down before they are tested.** §8.1 and §8.2 are
   the template: state the choice and its consequence, then test.
8. **Encoding choices are reversible and documented**, with their cost recorded.
9. **Lossiness is visible and counted.** Every report prints the
   managed / opaque / unmanaged / env-derived split. A loss that does not appear in
   the output is a bug, whatever else is true about it.
10. **Environment-derived inputs are recorded in the solution hash, not excluded.**
   Excluding them makes two hosts produce different configurations from the same
   source under the same hash.

---

# 17. A Rule About This Document

This file has been rewritten three times and the project has not yet been run once.
That is the actual risk to it.

**Rule: this document does not grow ahead of the code.** A section describing an
unimplemented stage is either deleted or explicitly marked a sketch. As the ladder
advances, this document *shrinks*: the speculative version of a section is replaced by
what was actually learned, including the parts that turned out wrong.

Those `## Learned` notes are the real output. Everything above them is hypothesis.

---

# 18. First Action

Not more architecture.

```bash
git clone https://gitlab.com/buildroot.org/buildroot.git
cd buildroot && make qemu_aarch64_virt_defconfig && make
# boot it by hand, unmodified, before Silt emits anything

make savedefconfig BR2_DEFCONFIG=$PWD/baseline_defconfig
# this file is Rung 2's target output and Rung 7's optimization baseline
```

Then Rung 1: `go test ./sexpr/...`
