# Handoff: components, and the agent gateway

What was built after the Kconfig work, why it is shaped this way, and what a
boot found that review did not. Written because the reasoning behind several
of these decisions existed only in a conversation, and the code cannot carry
all of it in comments.

## The thesis

A WebAssembly component cannot call what it did not import, and its imports
are in its binary. So "what may this code do?" is a question about an
artifact rather than about a promise, and Silt can answer it the way it
answers questions about Kconfig: check the image's policy against the facts
before anything is built.

The product this is aimed at is not an MCP server. Those are being written by
everyone, the OPC Foundation included. It is the boundary: a plant can let an
agent read equipment because the artifact provably cannot write to it, rather
than because someone reviewed the code and promised.

## What exists

    silt check          what a component CANNOT reach   (from its binary)
    silt hash           which bytes ship                (sha256 per component)
    try-gateway.sh      what it does with what it can   (wasmtime, a fake PLC)
    try-host.sh         the whole chain over MCP        (host + component + Modbus)
    qemu-arm-agent-vpn  all of it, on an ARM64 appliance

    Claude Desktop --stdio--> mcp-remote --HTTP--> WireGuard
      ARM64 appliance: no login, read-only root
        agent-gateway-host   3MB, hash-pinned, fetched not committed
          gateway.wasm       silt-checked: imports no write
            silt:modbus/read
              Modbus TCP --> device

A model has driven this end to end: it discovered the tool, read registers,
and refused a write because no write tool exists.

## Decisions, and why

**One core change, then packs.** A second tree kind, `wasm-component`, whose
symbols come from a component's imports instead of a Kconfig file. Everything
after it — interfaces, components, policy, hosts — is pack content. The test
for any future core change: would a second, unrelated application need it?

**Component scopes take only `n`.** The claim a component tree makes is what
a component cannot reach. "Must import X" is not a security property.

**The name mapping is injective, not pretty.** `silt:modbus/read@0.1.0`
becomes `SILT__MODBUS__READ`: `__` for the structural separators, `_` for
hyphens, version dropped. With single underscores `a-b:c/d` and `a:b-c/d`
collide, and forbidding one would silently forbid the other. Every pack's
policy is written in this mapping, so it is effectively permanent.

**Fail closed on vocabulary.** An import the pack's WIT cannot name is a
finding, not a pass: policy cannot forbid what it cannot name. This is why
packs vendor the WASI definitions they permit.

**One tree per trust boundary,** never unioned across trees. Everything an
agent can reach is one tree; operator-only tools would be another.

**Policy lives in a feature, not a profile.** An image composes exactly one
profile, the appliances already use `profile:locked`, and a restriction is
additive.

**Two boundaries, checked differently.** What a component may ask for is in
its binary and `silt check` verifies it before the build. What the host
actually provides is 460 reviewable lines. Neither substitutes for the other.

## What booting found that review did not

Every one of these passed review and failed on a device.

- `wasmtime-min`, the runtime pack's default, has the component model
  compiled out. It could never have run a component, precompiled or not.
- The runtime unit named `ReadWritePaths` for a directory nothing created;
  systemd refuses to start such a unit. `StateDirectory` creates it.
- `${GRANT_DIRS}` in a systemd `ExecStart` arrives as one argument. Unbraced
  `$GRANT_DIRS` is what splits into words.
- wasmtime wants a compilation cache under `$HOME`, which a read-only root
  refuses. `-C cache=n`, or precompile and never ship a compiler.
- `profile:locked` set `BR2_TARGET_GENERIC_GETTY=n` and still offered a login:
  that symbol governs Buildroot's getty, not systemd's. Masking the units is
  the only fix and masks are files, which is why the profile became a pack.
- QEMU gave a second disk `vda` and the rootfs `vdb`, so `root=/dev/vda`
  panicked. Device names are an accident of enumeration; the appliance finds
  its state disk by label for the same reason.
- A malformed `peers.conf` failed provisioning and the gateway served anyway,
  because `wg0` and its address exist before peers are applied. Hence
  `Requires=wg-appliance.service`.
- A model asked for "40001 through 40005" applied the 4xxxx convention,
  subtracted one, and read a different part of the register map. The answer
  looked right. Schemas now state the convention and replies repeat it.

## What the guarantee does not cover

Worth saying plainly to anyone who asks:

- **MCP has no authentication.** Reachability is the authentication: the
  gateway listens on the WireGuard address only. On a LAN it would be open.
- **Unit ids are unrestricted.** Behind a TCP-to-RTU gateway that reaches
  every device on the serial segment.
- **Reads are not always harmless.** Read-to-clear registers exist, and a
  read-only path still discloses everything.
- **No rate limiting.** An agent in a loop can saturate a PLC's connection
  budget and disturb the control system's own traffic.
- **The audit log is tamper-evident, not tamper-proof.** A hash chain with no
  signature: signing needs a key, and where that key lives is a deployment
  decision.

## Next

1. Rebuild `gateway.wasm` after any component change — the committed binary
   is what ships and what `silt check` checks.
2. A boot test for `qemu-arm-agent-vpn`, asserting the banner, the unit, and
   an MCP call over the tunnel.
3. `ci/boot-test.sh` builds QEMU command lines: check it does not hardcode
   `root=/dev/vda` for images with a state disk.
4. Named, described tools (`read_tank_level`) instead of raw addresses. The
   description is what a model reasons with, and raw Modbus tells it nothing.
5. Rate limiting and unit allowlists in the component; authentication in the
   host if MCP ever leaves the tunnel.
6. Still undecided from before this work: what `(prefer ...)` should mean.
