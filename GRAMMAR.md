# Silt grammar

The complete syntax. Written before the parser, so the parser has something to be
tested against rather than defined by.

Two properties matter more than the productions themselves, and both are visible by
inspection below:

1. **Every list begins with a fixed keyword.** There is no production in which a
   non-keyword appears in head position. That is the formal content of Invariant 1
   ("S-expressions are data, not Lisp"): with no application form, there is nothing to
   apply, and no amount of nesting produces evaluation.
2. **Recursion is bounded and structural.** Only `when` conditions nest, and only
   through `and` / `or` / `not` over symbol tests. Nothing is recursive on values.

## Notation

EBNF. `{ x }` is zero or more, `[ x ]` optional, `|` alternation. Terminals are
quoted. Comments run from `;` to end of line and are not part of any production.

---

## Lexical

```ebnf
name-in-tree   = ( letter | digit | "_" ) { letter | digit | "_" } ;
tree-name      = lower { lower | digit | "-" } ;
symbol         = name-in-tree | tree-name ":" name-in-tree ;
ident          = ( letter | digit | "_" ) { letter | digit | "_" } ;
name           = letter { letter | digit | "-" } ;
fragment-id    = kind ":" name ;
kind           = "target" | "profile" | "feature" ;
string         = '"' { any-char-except-quote } '"' ;
symbol-pattern = symbol [ "*" ] ;
```

A symbol's tree comes from where it is written, never from its spelling. Inside a
scope block a bare name belongs to that scope's tree, and a qualified one must name
the same tree. Everywhere else — rule conditions and consequents, `override`,
`opaque`, `environment`, `unmanaged`, a capability's `symbol` — the `tree:` qualifier
is required. `CONFIG_` is not a namespace: Linux, BusyBox and U-Boot all use it, and
Buildroot builds its own `conf` with the prefix emptied.

Values inside `string` are never interpreted by Silt. A `$(...)` inside one is a make
expansion (DESIGN.md §14.3) and is carried verbatim.

---

## Top level

```ebnf
file     = { toplevel } ;
toplevel = fragment | rules | image | capabilities | tree | pack ;
```

Six things can appear at the top of a file. A file holding more than one
`fragment` is legal but discouraged; the directory layout in DESIGN.md §13 assumes one.

---

## Fragments

```ebnf
fragment        = "(" "fragment" fragment-id { fragment-clause } ")" ;

fragment-clause = doc | provides | requires | scope ;

doc             = "(" "doc" string ")" ;
provides        = "(" "provides" { capability } ")" ;
requires        = "(" "requires" { capability } ")" ;
capability      = "(" "capability" name ")" ;
```

`provides` is legal only in a `target:` fragment; `requires` only in `profile:` and
`feature:`. This is a static check, not a grammatical one.

---

## Capabilities

```ebnf
capabilities = "(" "capabilities" { capability-decl } ")" ;
capability-decl = "(" "capability" name { cap-clause } ")" ;
cap-clause   = "(" "doc" string ")"
             | "(" "symbol" symbol ")" ;
```

One declaration per capability across the whole library. `symbol` is optional:
some capabilities correspond to a Kconfig symbol, and some are outcomes that no
single symbol expresses — `dhcp-client` is satisfied by BusyBox udhcpc under one
profile and systemd-networkd under another.

A library with no declarations keeps the older behaviour, where `provides` and
`requires` match by string alone.

## Scopes and constraints

```ebnf
scope       = "(" "buildroot" { constraint } ")"
            | "(" "linux" { constraint } ")"
            | "(" "scope" tree-name { constraint } ")" ;

constraint  = hard | soft | value | guarded | tree-option ;

hard        = "(" tristate symbol ")"
            | "(" "at-least" tristate symbol ")" ;
tristate    = "y" | "m" | "n" ;

soft        = "(" "prefer" tristate symbol ")" ;

value       = "(" "value" symbol string ")" ;

guarded     = "(" "when" condition { constraint } ")" ;

tree-option = "(" "custom-version" string ")" ;
```

`at-least y X` is legal and means the same as `y X`. `at-least n X` is legal and
vacuous. Both are accepted so that the form can be generated mechanically without
special-casing.

`tree-option` is valid only in the `linux` scope.

`(buildroot ...)` and `(linux ...)` are shorthand for `(scope buildroot ...)` and
`(scope linux ...)`. Every other tree must be declared (below) before a scope may name
it. Symbols are checked against their tree's declared `prefix` at compose time: static
check, and a spelling check only.

---

## Packs

```ebnf
pack        = "(" "pack" name { pack-clause } ")" ;
pack-clause = "(" "version" string ")"
            | "(" "doc" string ")"
            | "(" "external" string ")"
            | "(" "requires" { "(" name string ")" } ")"
            | "(" "provides" { "(" kind name ")" } ")" ;
```

A pack is a directory someone else maintains: `silt.sx` holding this declaration,
`fragments/` holding what it offers, and usually a `br2-external` tree that implements
them. `silt ... --pack DIR` loads all of it, the tree included, because a fragment that
states `BR2_PACKAGE_X` and the tree that makes `BR2_PACKAGE_X` exist are two halves of
one thing.

`provides` is an interface and is checked both ways: everything promised must be
defined, and a fragment the pack does not declare is an error, because a consumer can
compose it and the pack does not know it maintains it. `requires` names releases —
`(buildroot "2025.02.16")`, `(linux ">=6.1")` — and is checked against whatever trees
the run was given; exact versions and `>=` are understood and nothing else, since a
constraint nobody enforces reads as a promise.

---

## Trees

```ebnf
tree        = "(" "tree" tree-name { tree-clause } ")" ;
tree-clause = "(" "kind" "kconfig" ")"
            | "(" "prefix" string ")"
            | "(" "consumed-by" symbol ")"
            | "(" "source" string ")" ;
```

`buildroot` (prefix `BR2_`) and `linux` (prefix `CONFIG_`, consumed by
`BR2_LINUX_KERNEL_CONFIG_FRAGMENT_FILES`) are built in. `kind` is required and only
`kconfig` exists; devicetree is not a constraint system and is rejected. `consumed-by`
names the Buildroot symbol that receives the tree's emitted config file; `silt check
--buildroot` verifies it exists and is a string.

---

## Conditions

```ebnf
condition = hard
          | "(" "set?" symbol ")"
          | "(" "equal?" symbol string ")"
          | "(" "and" condition { condition } ")"
          | "(" "or"  condition { condition } ")"
          | "(" "not" condition ")" ;
```

`set?` is the string-emptiness test from DESIGN.md §8.3. `equal?` compares a stated
value with a literal. Both are three-valued over what the composition states: a string
nobody stated is unknown, never unequal, because kbuild may give it that value by
default. Rules fire only on known conditions. (In the Kconfig model, comparisons against
literals are exact: see `cnf/values.go`.)

A condition may cross scopes. That is the entire point of `rules`.

---

## Rules

```ebnf
rules = "(" "rules" name { guarded } ")" ;
```

Only `guarded` forms. A `rules` block containing a bare constraint is a syntax error —
an unconditional cross-tree assertion is a fragment's job, not a rule's.

---

## Images

```ebnf
image         = "(" "image" name { image-clause } ")" ;

image-clause  = doc | compose | scope | override
              | opaque | unmanaged | delegate | environment
              | verified-against | repair-policy ;

compose       = "(" "compose" { fragment-id } ")" ;
override      = "(" "override" { constraint } ")" ;

opaque        = "(" "opaque" { value } ")" ;
unmanaged     = "(" "unmanaged" { symbol-pattern } ")" ;
delegate      = "(" "delegate" tree-name delegation ")" ;
environment   = "(" "environment" { value } ")" ;

verified-against = "(" "verified-against" { tree-version } ")" ;
tree-version     = "(" tree-name string ")" ;

tree-name     = "linux" | "uboot" | "barebox" | "busybox" | "uclibc" | name ;
delegation    = "(" "custom-config-file" string ")"
              | "(" "config-fragment-files" { string } ")" ;

repair-policy = "(" "repair-policy" { policy-clause } ")" ;
policy-clause = "(" "minimize" metric ")"
              | "(" "keep" keepable ")"
              | "(" "baseline" string ")" ;
metric        = "changed-symbols" | "packages" ;
keepable      = "target" | "profile" | "toolchain" ;
```

`compose` takes exactly one `target:`, exactly one `profile:`, and zero or more
`feature:` fragments. Static check, and the reason profiles and features are not
interchangeable (DESIGN.md §7).

---

## Imported representation

Kconfig, after import and canonicalization, lives in the same S-expression
representation. This half is **generated, never authored** — but it is specified here
because the canonical form is what gets hashed, and a hash over an unspecified form is
not reproducible.

```ebnf
imported     = "(" "symbol" symbol { symbol-clause } ")" ;
symbol-clause = "(" "type" symbol-type ")"
              | "(" "depends" condition ")"
              | "(" "selects" symbol [ "if" condition ] ")"
              | "(" "implies" symbol [ "if" condition ] ")"
              | "(" "default" default-value [ "if" condition ] ")"
              | "(" "choice" name ")"
              | "(" "source" string integer ")" ;
symbol-type  = "bool" | "tristate" | "string" | "int" | "hex" ;
default-value = tristate | string | integer ;
```

```lisp
(symbol CONFIG_ARM64_MTE
  (type bool)
  (depends (and CONFIG_ARM64 CONFIG_AS_HAS_LSE_ATOMICS))
  (source "arch/arm64/Kconfig" 2104))
```

`source` is mandatory on every imported symbol. Provenance is Invariant 3, and an
imported symbol without a `file:line` cannot appear in an explanation.

Note `selects` is recorded separately from `depends` rather than being folded into it.
That separation is what makes the two `select` modes of DESIGN.md §8.1 —
implementation-faithful and spec-faithful — expressible over the same imported data.

---

## The complete keyword set

Forty-six authored, plus nine that only the importer emits. The documentation
previously claimed seven, which counted only the constraint forms and was wrong.

| group | keywords |
| --- | --- |
| top level | `fragment` `rules` `image` `capabilities` |
| fragment | `doc` `provides` `requires` `capability` |
| capability decl | `symbol` |
| scope | `buildroot` `linux` |
| constraint | `y` `m` `n` `at-least` `prefer` `value` `when` |
| tree | `tree` `scope` `kind` `prefix` `consumed-by` `source` |
| pack | `pack` `version` `external` |
| condition | `set?` `equal?` `and` `or` `not` |
| tree option | `custom-version` |
| image | `compose` `override` `opaque` `unmanaged` `delegate` `environment` `verified-against` |
| delegation | `custom-config-file` `config-fragment-files` |
| policy | `repair-policy` `minimize` `keep` `baseline` |
| imported (generated) | `symbol` `type` `depends` `selects` `implies` `default` `choice` `source` |

Seven of those thirty-four are what a user writes most of the time, which is probably
what the original claim meant. It should have said so.

---

## Resolved ambiguities

Recorded because they were real defects, found by writing this document.

**`prefer` was overloaded.** `(prefer y SYM)` is a soft constraint; `(prefer
preserve-target)` was a repair-policy directive. Same head, different arity, unrelated
meaning. The policy form is now `(keep target)`.

```lisp
(prefer n BR2_ENABLE_DEBUG)      ; soft constraint
(repair-policy (keep target))    ; policy directive
```

**`require` and `requires` both appeared.** `(require BR2_X)` in prose, `(requires
(capability x))` in the library. `require` is gone — it was only ever a longhand for
`(y BR2_X)`. `requires` now means capability requirement and nothing else.

**No `if`, no `define`, no `lambda`, no application.** Absent by construction, not by
policy. See the head-position property at the top of this file.

---

## Worked example against the grammar

```lisp
(fragment target:qemu-aarch64-virt                    ; fragment, fragment-id
  (buildroot                                          ; scope
    (y BR2_aarch64)                                   ; hard
    (value BR2_TARGET_GENERIC_GETTY_PORT "ttyAMA0"))  ; value
  (linux
    (y CONFIG_VIRTIO_BLK)
    (at-least m CONFIG_MAC80211))                     ; hard, at-least form
  (provides (capability mmu)))                        ; provides, capability

(rules cross-tree
  (when (y BR2_PACKAGE_DHCPCD) (y CONFIG_PACKET))     ; guarded, crosses scopes
  (when (set? BR2_TARGET_OPENSBI_PLAT)                ; condition: set?
        (y BR2_TARGET_OPENSBI)))

(image qemu-arm-dev
  (compose target:qemu-aarch64-virt profile:minimal)
  (linux (custom-version "6.18.7"))                   ; tree-option
  (opaque (value BR2_GLOBAL_PATCH_DIR "board/qemu/patches"))
  (unmanaged BR2_TARGET_UBOOT_*)                      ; symbol-pattern
  (environment (value BR2_HOST_GCC_VERSION "13 2"))
  (repair-policy (minimize changed-symbols) (keep target)))
```

Rung 1 is done when a parser accepts exactly this and rejects everything outside the
productions above.
