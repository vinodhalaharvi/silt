# Fragment library

## The rule

> **State only what Kconfig cannot derive.**

If a line is implied by a `depends on` or a `select` already in the Buildroot or Linux
Kconfig trees, it does not belong in a fragment. Restating a derivable constraint is
not documentation — it is a second copy that goes stale, and it is exactly the
duplication the constraint model exists to eliminate. A fragment full of derivable
lines is a defconfig with parentheses around it, which is strictly worse than a
defconfig: same content, unfamiliar syntax, more characters.

A linter enforces this once the constraint model exists (Rung 5): any line whose
removal does not change the solved model is dead and gets deleted.

## What is left after applying the rule

Five categories, and nothing else:

1. **Choices.** `BR2_cortex_a57` over `BR2_cortex_a53` — Kconfig offers both; picking
   one is intent.
2. **Strengthening.** `(y CONFIG_VIRTIO_BLK)` where Kconfig permits `=m`. The rootfs
   lives on virtio and no module can load before it is mounted. Kconfig cannot express
   boot ordering.
3. **Cross-tree implications.** Buildroot and Linux are separate Kconfig trees. Neither
   can state that `BR2_PACKAGE_WPA_SUPPLICANT` needs `CONFIG_MAC80211`. See
   `rules/cross-tree.sx`.
4. **Negative intent.** `(n BR2_PACKAGE_SYSTEMD)` in a minimal profile, which stays
   enforced when some feature pulls systemd in three levels down. Kconfig has no
   vocabulary for "and keep it out".
5. **Capabilities.** The abstraction that lets a feature be written once and composed
   against any target that can support it.

## Layout

```
targets/    hardware. No packages, no userspace policy.
profiles/   userspace policy. No hardware.
features/   one capability, spanning both Kconfig trees.
rules/      cross-tree implications that belong to no single fragment.
```

If a change to a target forces an edit to a feature, the boundaries are wrong. That is
testable: see EXAMPLES.md §2.
