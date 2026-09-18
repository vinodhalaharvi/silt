package compose

import (
	"fmt"
	"strings"

	"github.com/vinodhalaharvi/silt/lang"
)

// maxDerivation bounds the base chain. Images deriving from each other in a
// cycle must be reported, not spun on.
const maxDerivation = 16

// flatten resolves an image's compose list, following image: references to
// their bases.
//
// What a derived image inherits, and why:
//
//   - the base's fragments, which is the point;
//   - its overrides, opaque values, unmanaged patterns and environment, because
//     those describe the same build and dropping them would silently change it;
//   - its repair policy and kernel version, unless the derived image states
//     its own.
//
// What it may not do is replace the base's target or profile. Those are the
// two exactly-one slots, and a derived image that swapped either would be a
// different image wearing the base's name. Compose a different base instead;
// the parser says so rather than picking one.
func (l *Library) flatten(im *lang.Image, seen []string) (*lang.Image, error) {
	for _, s := range seen {
		if s == im.Name {
			return nil, fmt.Errorf("%s: image derivation cycle: %s",
				im.Pos.Short(), strings.Join(append(seen, im.Name), " -> "))
		}
	}
	if len(seen) >= maxDerivation {
		return nil, fmt.Errorf("%s: image derivation deeper than %d",
			im.Pos.Short(), maxDerivation)
	}

	var baseRef *lang.ID
	var own []lang.ID
	for i := range im.Compose {
		if im.Compose[i].Kind == lang.KindImage {
			baseRef = &im.Compose[i]
			continue
		}
		own = append(own, im.Compose[i])
	}
	if baseRef == nil {
		return im, nil
	}

	base, ok := l.Images[baseRef.Name]
	if !ok {
		return nil, fmt.Errorf("%s: no such image %s", im.Pos.Short(), *baseRef)
	}
	base, err := l.flatten(base, append(seen, im.Name))
	if err != nil {
		return nil, err
	}

	out := *im
	out.Compose = append(append([]lang.ID(nil), base.Compose...), own...)

	// Declarations accumulate. The derived image's own entries come last so
	// that an override of an override wins, which is the only sensible reading
	// of a form that already means last-wins.
	out.Override = append(append([]lang.Constraint(nil), base.Override...), im.Override...)
	out.Opaque = append(append([]lang.Constraint(nil), base.Opaque...), im.Opaque...)
	out.Environment = append(append([]lang.Constraint(nil), base.Environment...), im.Environment...)
	out.Unmanaged = append(append([]lang.SymbolID(nil), base.Unmanaged...), im.Unmanaged...)
	out.Delegate = append(append([]lang.Delegation(nil), base.Delegate...), im.Delegate...)

	for sc, cs := range base.Constraints {
		out.Constraints[sc] = append(append([]lang.Constraint(nil), cs...), im.Constraints[sc]...)
	}

	if out.Version == "" {
		out.Version = base.Version
	}
	if len(out.Policy.Minimize) == 0 && len(out.Policy.Keep) == 0 && out.Policy.Baseline == "" {
		out.Policy = base.Policy
	}

	// verified-against is inherited, and a derived image pinning a different
	// release than its base is an error rather than a silent reinterpretation:
	// the base's fragments were checked against one tree and cannot be assumed
	// correct for another.
	if out.VerifiedAgainst == nil {
		out.VerifiedAgainst = base.VerifiedAgainst
	} else {
		for tree, want := range base.VerifiedAgainst {
			if got, ok := out.VerifiedAgainst[tree]; ok && got != want {
				return nil, fmt.Errorf(
					"%s: %s pins %s %s but its base %s pins %s",
					im.Pos.Short(), im.Name, tree, got, base.Name, want)
			}
			if _, ok := out.VerifiedAgainst[tree]; !ok {
				out.VerifiedAgainst[tree] = want
			}
		}
	}

	if err := checkFlattenedShape(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// checkFlattenedShape enforces exactly one target and one profile once the
// chain is resolved, which is the only point at which the counts are known.
func checkFlattenedShape(im *lang.Image) error {
	var targets, profiles []lang.ID
	for _, id := range im.Compose {
		switch id.Kind {
		case lang.Target:
			targets = append(targets, id)
		case lang.Profile:
			profiles = append(profiles, id)
		}
	}
	if len(targets) != 1 {
		return fmt.Errorf("%s: image %s resolves to %d targets; exactly one is required",
			im.Pos.Short(), im.Name, len(targets))
	}
	if len(profiles) != 1 {
		return fmt.Errorf("%s: image %s resolves to %d profiles; exactly one is required",
			im.Pos.Short(), im.Name, len(profiles))
	}
	return nil
}
