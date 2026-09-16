package compose

import "github.com/vinodhalaharvi/silt/kconfig"

// selectClosure returns every symbol that will be enabled by Kconfig's own
// `select` machinery, given what the composition states.
//
// Kconfig's select forcibly enables a symbol WITHOUT visiting that symbol's own
// dependencies (DESIGN.md §8.1), so the closure follows selects unconditionally
// and stops only at conditions it can see are false.
//
// This exists because rules could not fire on selected symbols. A rule reading
//
//	(when (y BR2_PACKAGE_SYSTEMD) (n CONFIG_SYSFS_DEPRECATED))
//
// never fired on an image that stated BR2_INIT_SYSTEMD, because nothing states
// BR2_PACKAGE_SYSTEMD — BR2_INIT_SYSTEMD selects it. The antecedent was true of
// the built image and unknown to Silt.
func selectClosure(tree *kconfig.Tree, stated map[string]bool) map[string]string {
	if tree == nil {
		return nil
	}
	// value -> the symbol whose select brought it in, for explanation.
	out := map[string]string{}
	frontier := make([]string, 0, len(stated))
	for s, on := range stated {
		if on {
			frontier = append(frontier, s)
		}
	}

	seen := map[string]bool{}
	for len(frontier) > 0 {
		name := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		if seen[name] {
			continue
		}
		seen[name] = true

		sym, ok := tree.Symbols[name]
		if !ok {
			continue
		}
		for _, sel := range sym.Selects {
			// A select guarded by a condition the composition refutes does
			// not fire. Anything else is taken: unknown conditions are
			// treated as possible, which matches select's own disregard for
			// whether the target is selectable.
			if sel.Cond != nil && refutedBy(sel.Cond, stated) {
				continue
			}
			if _, already := out[sel.Target]; !already && !stated[sel.Target] {
				out[sel.Target] = name
			}
			frontier = append(frontier, sel.Target)
		}
	}
	return out
}

// refutedBy reports whether the composition definitely makes e false.
// Conservative in the same direction as everything else: unstated is unknown.
func refutedBy(e *kconfig.Expr, stated map[string]bool) bool {
	if e == nil {
		return false
	}
	switch e.Op {
	case kconfig.ExprSym:
		on, known := stated[e.Sym]
		return known && !on
	case kconfig.ExprNot:
		sub := e.Args[0]
		if sub.Op == kconfig.ExprSym {
			on, known := stated[sub.Sym]
			return known && on
		}
		return false
	case kconfig.ExprAnd:
		for _, a := range e.Args {
			if refutedBy(a, stated) {
				return true
			}
		}
		return false
	case kconfig.ExprOr:
		for _, a := range e.Args {
			if !refutedBy(a, stated) {
				return false
			}
		}
		return true
	}
	return false
}
