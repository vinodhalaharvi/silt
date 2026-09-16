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
symbol         = "BR2_" ident | "CONFIG_" ident ;
ident          = ( letter | digit | "_" ) { letter | digit | "_" } ;
name           = letter { letter | digit | "-" } ;
fragment-id    = kind ":" name ;
kind           = "target" | "profile" | "feature" ;
string         = '"' { any-char-except-quote } '"' ;
symbol-pattern = symbol [ "*" ] ;
```

Values inside `string` are never interpreted by Silt. A `$(...)` inside one is a make
expansion (DESIGN.md §14.3) and is carried verbatim.

---

## Top level

```ebnf
file     = { toplevel } ;
toplevel = fragment | rules | image ;
```

Exactly three things can appear at the top of a file. A file holding more than one
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

## Scopes and constraints

```ebnf
scope       = "(" scope-name { constraint } ")" ;
scope-name  = "buildroot" | "linux" ;

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

Symbols must match their scope: `BR2_*` in `buildroot`, `CONFIG_*` in `linux`. Static
check.

---

## Conditions

```ebnf
condition = hard
          | "(" "set?" symbol ")"
          | "(" "and" condition { condition } ")"
          | "(" "or"  condition { condition } ")"
          | "(" "not" condition ")" ;
```

`set?` is the string-emptiness test from DESIGN.md §8.3 — it models `!= ""`, which is
what the great majority of Buildroot's 154 string comparisons actually use. Equality
against a literal is deliberately absent: it stays opaque and must be declared.

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

Thirty-five authored, plus nine that only the importer emits. The documentation
previously claimed seven, which counted only the constraint forms and was wrong.

| group | keywords |
| --- | --- |
| top level | `fragment` `rules` `image` |
| fragment | `doc` `provides` `requires` `capability` |
| scope | `buildroot` `linux` |
| constraint | `y` `m` `n` `at-least` `prefer` `value` `when` |
| condition | `set?` `and` `or` `not` |
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
