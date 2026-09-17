"""Qualify BR2_/CONFIG_ symbols written outside a scope block.

A migration aid for fragments written before symbols carried their tree, and
the one place prefix inference is still allowed: it is a guess about files
that were written when the guess was the rule. Silt itself never infers a
tree from a prefix, which is why this is a script and not a command.

Inside (buildroot ...), (linux ...) or (scope TREE ...) symbols stay bare;
everywhere else - rule conditions and consequents, override, opaque,
environment, unmanaged, a capability's symbol - BR2_X becomes
buildroot:BR2_X and CONFIG_X becomes linux:CONFIG_X. Comments and strings
are left alone.

    python3 tools/qualify.py fragments/features/ssh.sx images/qemu-go.sx
    silt check --buildroot ~/buildroot --linux ~/linux   # then read the diff

Files are rewritten in place, so run it on something git can undo.
"""
import re, sys

TOKEN = re.compile(r'"(?:\\.|[^"\\])*"|;[^\n]*|\(|\)|[^\s()";]+|\s+')

def qualify(src):
    out, stack, expect_head = [], [], False
    for m in TOKEN.finditer(src):
        tok = m.group(0)
        if tok == "(":
            stack.append(None); expect_head = True; out.append(tok); continue
        if tok == ")":
            if stack: stack.pop()
            expect_head = False; out.append(tok); continue
        if tok.isspace() or tok.startswith(";") or tok.startswith('"'):
            out.append(tok); continue
        if expect_head:
            stack[-1] = tok; expect_head = False; out.append(tok); continue
        scoped = any(h in ("buildroot", "linux", "scope") for h in stack)
        if not scoped and ":" not in tok:
            if tok.startswith("BR2_"): tok = "buildroot:" + tok
            elif tok.startswith("CONFIG_"): tok = "linux:" + tok
        out.append(tok)
    return "".join(out)

if __name__ == "__main__":
    for path in sys.argv[1:]:
        s = open(path).read()
        if path.endswith(".go"):
            # Only raw strings that are S-expression sources.
            s2 = re.sub(r"`(\s*\(.*?)`", lambda m: "`" + qualify(m.group(1)) + "`", s, flags=re.S)
        else:
            s2 = qualify(s)
        if s2 != s:
            open(path, "w").write(s2); print("qualified", path)
