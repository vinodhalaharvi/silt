# Silt — handoff

Nineteen packages, 151 tests, CI green. Two Kconfig trees imported and checked,
three packs, fourteen images, one appliance that has never been built.

This supersedes the earlier handoff: everything its priority list named is done.

---

## 1. Read this first

**Every real finding in this project came from running something against a real
tree.** Not one came from reading code. The list in §9 is twenty-odd bugs and
each was found by importing Buildroot or Linux, evaluating a defconfig, or
diffing against kbuild. When in doubt, run it against `~/buildroot` and
`~/linux`.

**Check against the pinned release, never master.** Four rebuilds were lost to
this before anyone wrote it down.

```bash
git clone --depth 1 --branch 2025.02.16 https://gitlab.com/buildroot.org/buildroot.git ~/buildroot
```

**`git log --oneline -1` after every `git am`.** A stale `.git/rebase-apply`
silently swallowed two patches once, and the failures surfaced much later as
mysterious build behaviour.

**Say what was not checked.** Most of this document's value is in the sentences
that say a thing has not been built or booted. Keep writing them.

---

## 2. What the project is

Silt is a configuration language over Buildroot and the other Kconfig trees a
Buildroot build drives. You write intent:

```lisp
(image qemu-arm-standard
  (compose
    target:qemu-aarch64-virt
    profile:standard
    feature:networking)
  (verified-against (buildroot "2025.02.16")))
```

and Silt composes the fragments, checks every claim against the real trees,
solves the constraint model, predicts what kbuild will make of it, and emits a
defconfig plus one config fragment per other tree.

The problem it attacks is that a Buildroot build configures a dozen Kconfig
trees at different times, each evaluated in a separate process, so a kernel
constraint cannot influence a Buildroot one **even in principle** — Buildroot's
`conf` finished twenty minutes earlier. Silt is a merged model evaluated ahead
of all of them, while every decision is still open.

`DESIGN.md` is the long argument, `GRAMMAR.md` the language, `README.md` the
tour.

---

## 3. Environment

Everything is verified against **Buildroot 2025.02.16** and **Linux 6.12**.

```bash
export SILT_BUILDROOT=~/buildroot      # real-tree tests
export SILT_LINUX=~/linux-6.12         # CONFIG_* verification
export SILT_KBUILD=1                   # tests that run make/conf
go test ./...
make ci BUILDROOT=~/buildroot
```

The Linux tree needs only its Kconfig files for checking, and `scripts/` too if
you want the evaluator's oracle test:

```bash
curl -sL -o /tmp/linux.tgz https://codeload.github.com/torvalds/linux/tar.gz/refs/tags/v6.12
tar xzf /tmp/linux.tgz -C /tmp --wildcards '*/Kconfig*' '*/Makefile' '*/scripts/*' '*/arch/arm64/configs/*'
mv /tmp/linux-6.12 ~/linux-6.12
```

`go.mod` says 1.25; nothing uses past 1.22, so `sed` it down in a sandbox with
an older toolchain and never commit that.

VM: `br2-builder`, zone `us-east1-b`, project `buildroot-vh-c2`, n2-standard-16.
Bills while running, does not stop itself.

---

## 4. The commands

| Command | What it does |
| --- | --- |
| `silt check [--buildroot D] [--linux D]` | parse, compose, verify every claim against the trees |
| `silt emit IMAGE -o DIR` | defconfig + one config file per other tree, with a solution hash |
| `silt solve IMAGE --buildroot D` | SAT: possible or not, the culprits, ranked repairs |
| `silt complete IMAGE --buildroot D [--linux D]` | the full `.config` kbuild will produce, and what it would drop |
| `silt fixpoint IMAGE --config .config` | compare intent with a `.config` kbuild actually produced |
| `silt why TREE:SYMBOL IMAGE` | provenance for one symbol |
| `silt import DEFCONFIG --buildroot D --kbuild` | a real defconfig becomes fragments |
| `silt hash --solution IMAGE` | content address of the configuration, trees, host and carried files |
| `silt fmt`, `silt hash` | canonical form, content address of files |

Global flags, accepted by every command: `--external DIR` (a br2-external tree),
`--pack DIR` (a pack: fragments plus its external tree), both repeatable.

---

## 5. Architecture

```
sexpr      s-expressions with positions
lang       the language: fragments, images, rules, capabilities, trees, packs
compose    merge fragments, apply overrides, run the rules engine
kconfig    import a Kconfig tree; two views of it (see below)
cnf        lower the tree to CNF, including value domains
solver     CDCL: incremental, assumptions, unsat cores
solve      possible-or-not, culprits (MUS), repairs (MCS)
verify     every stated symbol against the tree it belongs to
emit       defconfig + per-tree config files + solution hash
fixpoint   intent versus a real .config
importer   defconfig -> fragments, plus the kbuild harness
pack       load and check a pack
```

**`kconfig` has two views of the same files, deliberately.**

- `parse.go` builds a `Tree`: one merged dependency per symbol, selects and
  defaults with their own conditions. Enough to ask whether a configuration can
  exist. This is what `cnf` lowers.
- `menu.go` + `eval.go` build a `Menu` and evaluate it exactly as
  `support/kconfig/symbol.c` does: per-property visibility finalised like
  `menu_finalize`, `sym_calc_value`, choice selection, `conf_write`'s rules.
  This is what predicts a `.config`.

The second exists because the first throws away what the evaluation needs:
which declaration a prompt came from, what its enclosing menus required, whether
a prompt exists at all.

---

## 6. What is checked, and how well

| Claim | Evidence |
| --- | --- |
| The evaluator matches kbuild | All 290 shipped Buildroot defconfigs, full `.config` vs full `.config`, identical; 60 randomly perturbed ones too |
| ... on the kernel | Linux 6.12 arm64 defconfig: conf writes 9,154 symbols, Silt 9,115, 54 differ, all compiler probes or downstream of them |
| The CNF model is sound | Every one of those 290 `.config`s, 39,144 value assignments included, satisfies the formula |
| Import round-trips | All 290 defconfigs import, compose, emit back to an identical `.config` |
| Fragments compose | 5 boards × 5 profiles, kbuild keeps every stated line in all 25 |
| Images are honest | Every image in the library run through kbuild; only `edge-camera` fails, as designed |
| `CONFIG_*` claims are real | Every one checked against Linux 6.12, rules included, fired or not |

**Not checked: anything built or booted.** `ci/boot-test.sh` exists, emits,
builds, boots under QEMU and drives the serial console, and **has never been
run end to end**. That is the single largest gap in the project.

---

## 7. Packs

A pack is a directory someone else maintains:

```
packs/k3s/
  silt.sx                     (pack ...) — the declared interface
  fragments/                  what it offers
  br2-external/               the Buildroot machinery that implements it
  tests/<image>.expect        boot assertions
  tests/<image>.qemu          extra QEMU resources, if it needs them
```

```lisp
(pack k3s
  (version "0.1.0")
  (requires (buildroot "2025.02.16") (linux ">=6.1"))
  (external "br2-external")
  (provides (feature k3s)))
```

`--pack DIR` loads the fragments and the br2-external tree together, because
they are two halves of one thing. The declaration is checked **both ways**:
everything promised must exist, and a fragment the pack does not declare is an
error too — a consumer can compose it and the pack does not know it maintains it.

The three packs in the tree:

- **`hello-silt`** — the reference. One package, one feature, an overlay carried
  with `(path ...)`, and boot assertions. Exists to prove the contract.
- **`nanopi-r2s`** — a board, imported from Buildroot's own defconfig, carrying
  its four board files. Written as though by a stranger, to find out what the
  format was missing. It found three things.
- **`k3s`** — the appliance. Upstream release binary, 74 kernel options taken
  from k3s's own `check-config.sh`, and capabilities required rather than stated.

**`(path SYMBOL "rel" [(as "-c {}")])`** names a file the pack carries: checked
to exist at load, emitted as `$(BR2_EXTERNAL_NAME_PATH)/rel` for Buildroot to
expand, and hashed into the solution. A `(value ...)` that happens to look like
a path is still a string nobody checks, which is how a MIPS patch directory got
into an ARM image.

---

## 8. Priority order

Work in this order. Reasoning below each.

| # | Task | Kind | Why now |
| --- | --- | --- | --- |
| 1 | Build and boot something | evidence | Everything is checked and nothing is true |
| 2 | `(forbids ...)` | language | The hardened-appliance claim has no form |
| 3 | Image composition | language | A base/dev/production family cannot be expressed |
| 4 | A protocol pack and its matrix | evidence | The combinatorics thesis is still unrun where it should show |
| 5 | `silt why` over the evaluator | capability | Provenance stops at stated symbols |
| 6 | Completion inside `silt check` | ergonomics | One command should answer both questions |
| 7 | `BR2_EXTERNAL` in `silt import` | gap | Importing a defconfig that uses one fails |
| 8 | Decide what `(prefer ...)` means | cleanup | Dead syntax that looks live |

### 1 — Build and boot something

`ci/boot-test.sh images/qemu-arm-boot.sx --buildroot ~/buildroot` on the VM.
It emits, builds with `BR2_EXTERNAL`, boots under QEMU using the invocation from
the board's own `readme.txt`, logs in and runs the assertions in
`packs/hello-silt/tests/qemu-arm-boot.expect`. `expect(1)` must be installed.

Then the same for `images/qemu-arm-k3s.sx` with `--timeout 900`. If
`kubectl get nodes` prints Ready, every layer from a fragment to a running pod
has been exercised once. Expect trouble in three places: the `K3S_EXTRACT_CMDS`
override (k3s is a bare binary, not an archive), memory and rootfs size, and
whether k3s can reach the network to pull its images on first boot.

Also worth running there: `silt complete -o pred.config`, then `make defconfig`
on the emitted defconfig, then diff. They should be identical apart from
`BR2_DEFCONFIG`. The VM's gcc differs from the sandbox's, and the host is part
of the model.

### 2 — `(forbids ...)`

The hardened-appliance pitch rests on being able to say what is **absent**, and
today the only form is `(n BR2_PACKAGE_DROPBEAR)` — one symbol at a time, by
name, with no way to say "nothing that provides a shell".

Wanted:

```lisp
(fragment profile:hardened
  (forbids
    (capability remote-shell)
    (capability compiler)))
```

Design notes, in rough order of difficulty:

- A capability is currently `provides`/`requires` only. `forbids` is the third
  relation: composing a fragment that provides a forbidden capability is an
  error, named at both ends.
- The interesting version also forbids **symbols** that imply the capability,
  so a package nobody declared cannot sneak in. That needs the capability
  vocabulary to bind more symbols than it does (`fragments/capabilities.sx`,
  where `symbol` is already optional and mostly unset).
- `silt complete` knows the full predicted `.config`. A forbidden capability
  whose symbol is on in the prediction is the check that actually protects the
  customer, and it is a few lines once the form exists.
- Emit is unaffected: forbidding is a compose-time and predict-time property,
  not something a defconfig can express.

Do not make `forbids` a synonym for `(n ...)`. The point is the absence claim
being checkable against the whole predicted configuration, not one symbol.

### 3 — Image composition

```lisp
(image customer-base (compose target:x profile:minimal feature:modbus))
(image customer-dev  (compose image:customer-base profile:development feature:ssh))
```

`compose` takes fragments only, and requires exactly one target and one profile.
A family of images differing by a feature or two currently means repeating the
base in each. Open questions worth deciding before coding: does composing an
image bring its overrides and `unmanaged` patterns with it (probably yes), can
a derived image replace the base's profile (probably not — say so clearly), and
what `verified-against` means for a derived image (probably inherited, with a
mismatch being an error).

### 4 — A protocol pack and its matrix

Buildroot has `libmodbus`, `mosquitto`, `paho-mqtt-c`, `open62541`,
`wireguard-tools`, `nftables`, `dnsmasq` — everything needed, with no vendor
BSP and no board.

Build `feature:modbus` and `feature:mqtt`, compose the four combinations, and
see whether the fragments stay disjoint or whether every pair needs a special
case. **This is the first real test of the combinatorics argument.** The
factoring experiment in the README showed vendor defconfigs share almost
nothing, which is a weak result for a duplication argument; protocol translators
are where the saving should actually appear. It can come back negative.

Then `images/qemu-arm-vpn.sx`: WireGuard, nftables, dnsmasq, hardened profile.
It runs entirely under QEMU, so the boot test can assert a tunnel comes up —
the portfolio artifact and the thesis test in one.

### 5 — `silt why` over the evaluator

`why` answers only for symbols a fragment stated or a rule derived. The
evaluator knows why **every** symbol has its value — which default fired, which
select forced it, whether the prompt was invisible — because that is what
`sym_calc_value` computes. `kconfig.Evaluation.Explain` already exists for
debugging and is most of it. DESIGN §11.4 asks for exactly this.

### 6 — Completion inside `silt check`

`check` is conservative by design: unstated is unknown, never false, so it
catches contradictions but never "you did not say enough". `complete` catches
exactly that. Running it as part of `check` means one command answers both, and
CI catches a dropped line the day it appears.

### 7 — `BR2_EXTERNAL` in `silt import`

`silt import` loads the tree without external trees, so importing a defconfig
that sets a symbol from one fails. The machinery exists
(`kconfig.PrepareExternal`); it needs threading through `cmd/silt/import.go`.

### 8 — `(prefer ...)`

`solve.Complete`, the greedy preference handling, is no longer what any command
runs. `(prefer n BR2_ENABLE_DEBUG)` currently affects nothing observable.
Either wire preferences into the evaluator path — they would mean "set this
where kbuild leaves it free" — or delete the form and the code. Dead syntax
that looks live is precisely what this project keeps finding in other people's
trees.

---

## 9. Bugs found so far, and what they have in common

**None was an algorithmic gap.** Every one was the encoding of reality being
wrong.

| Bug | Shape |
| --- | --- |
| Backslash continuations over three lines | Kconfig lexing |
| `bool"selftests"` with no separator | Kconfig lexing; one symbol lost its type |
| Lexical scoping crossing `source` | `if C / source x / endif` puts x inside C |
| `comment` blocks carrying their own `depends on` | Poisoned the symbol above; 172 symbols impossible |
| Repeated declarations conjoined, not disjoined | `BUSYBOX && !BUSYBOX`; every systemd image UNSAT |
| Watch list truncated while iterating an aliasing slice | Solver |
| Propagation frontier taken after asserting | Learned clauses inert |
| Contradictory unit clauses dropped at construction | `A ∧ ¬A` reported SAT |
| Core returned every assumption | Repairs could not be minimal |
| `CONFIG_` treated as a namespace | Two trees' symbols were one variable |
| Prefix treated as part of the name | Every kernel symbol read as missing |
| `HOST_GCC_VERSION` hard-coded as `"13 2"` | Wrong twice; every `BR2_HOST_GCC_AT_LEAST_*` wrong |
| Choice members nested in `if` not counted | cortex-A32 chosen for a cortex-A5 board |
| Automatic submenus counted as choice members | Thirty openssl options became SSL-library choices |
| `option modules` ignored | 1,167 kernel symbols promoted from m to y |
| Relational comparisons evaluated as false | `NR_CPUS >= 4` silently wrong |
| Int and hex values emitted quoted | kbuild discards `SUBSIZE="2048"` |
| Empty `linux.config` always wired in | Nothing could round-trip |
| Absolute `source` paths joined to the root | Every br2-external tree resolved to nothing, silently |
| `--pack` twice loaded one pack | The loop consumed every occurrence and kept the last |

**Fragment bugs, same lesson:** an invented kernel version; a patch directory
for MIPS; a header-series symbol absent from the release; a defconfig name that
cannot be spelled; `CONFIG_EXT4_FS_MODULE`, which no kernel declares;
`CONFIG_SYSFS_DEPRECATED`, removed before 6.12; `CONFIG_EXT4_ENCRYPTION`,
renamed — and copied from Buildroot's own help text, which is still wrong;
`BR2_TARGET_UBOOT_BOARDNAME`, which **exists, is spelled correctly, passes
`silt check`, and is inert**.

Silt confirms symbols are real. It cannot confirm they are effective. Only a
boot can, which is why §8.1 is first.

---

## 10. Deliberately out of scope

**Silt does not generate `.mk` files.** A package is a build recipe;
Buildroot's package infrastructure is the right abstraction for it. The moment
Silt generates make, it is a code generator with a loss surface it cannot check,
and its whole claim is that its output is a validated configuration.

**Devicetree is not a constraint system.** `CONFIG_MMC_MESON_GX=y` compiles a
driver registering `"amlogic,meson-gx-mmc"`; the `.dts` node declares the same
string; they bind at runtime by string match and neither file mentions the
other. `(tree ... (kind devicetree))` is rejected with that reason. A kind that
is never implemented is a promise in the schema.

**There is no merged DAG over symbols, and there cannot be.** The constraint
evaluations are separate processes at separate times. That temporal gap is what
Silt is.

**Kconfig itself is not a DAG either.** `depends on` and `select` point opposite
ways and cycles are routine. Constraints have no direction, which is why
lowering to CNF was right and why cycles stopped being an error.

---

## 11. Smaller known gaps

- **A feature cannot ask for a minimum value.** k3s needs a rootfs big enough
  for a 60MB binary, but stating a size in the feature conflicts with the
  profile that chose one. `at-least` exists for tristates, not values.
- **Capability vocabulary is repo-wide.** A pack requiring a capability the
  consumer has not declared is an error, so a stranger's pack and the consuming
  library are coupled through `fragments/capabilities.sx`.
- **Two packs providing the same fragment id** collide with a plain redefinition
  error: correct, unhelpful.
- **A pack cannot ship a `configs/` defconfig** for people not using Silt.
- **Nothing resolves or fetches.** `--pack DIR` is a directory; making it a URL
  needs decisions about pinning and trust that a real second-party pack should
  drive.
- **`edge-camera` is a deliberately failing fixture.** `ci/check-images.sh`
  requires it to fail, and to fail in the verifier specifically.
- **The README still says "Status: pre-implementation. There is no Go in this
  repository yet."** Nineteen packages later that is the most misleading
  sentence in the tree.
- **Compiler probes.** `$(cc-option,...)` is answered by kbuild running the
  compiler. Silt reads them as `n` and reports how many; for a cross build the
  answer depends on a toolchain the checking machine may not have.

---

## 12. Things worth not forgetting

**Source authorities rather than memory.** systemd's README lists ten mandatory
kernel options; the rule had three, twice. k3s's `check-config.sh` is the
authority for k3s's kernel options, and transcribing it found twenty more that
kbuild would have dropped. Buildroot's own `support/scripts/br2-external`
generates the external-tree files rather than Silt reimplementing the format.
Buildroot's own `conf` is the oracle for the evaluator, and the kernel's own
`conf` for the kernel.

**The authority can be stale too.** Buildroot's help text still says
`CONFIG_EXT4_ENCRYPTION`. The authority for a kernel symbol is the kernel.

**`make defconfig BR2_DEFCONFIG=<missing file>` exits zero** and quietly builds
an i586 default. This cost two builds.

**Test the property, not the output.** The repair tests check that every
proposed repair makes the composition solve and that putting any member back
breaks it again — not that the text matches. The one place output is pinned,
`solve/testdata/edge-camera.expected`, is pinned deliberately, because
explanation quality is the part most likely to rot quietly.

**A comment describing a test that does not exist is worse than no comment.**
`edge-camera` claimed its output was asserted for weeks before it was.
