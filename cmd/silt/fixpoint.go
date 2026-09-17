package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/fixpoint"
	"github.com/vinodhalaharvi/silt/lang"
)

// cmdFixpoint is DESIGN.md §10: compare an image with the .config files kbuild
// produced from Silt's output, and name every stated symbol kbuild did not
// honour. A symbol kbuild deleted satisfies (n X); nothing else is excused.
//
//	silt fixpoint IMAGE.sx --buildroot DIR --config output/.config
//	    [--config linux=output/build/linux-6.12/.config] [-L DIR]
func cmdFixpoint(args []string) error {
	configs := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] != "--config" {
			rest = append(rest, args[i])
			continue
		}
		i++
		if i >= len(args) {
			return fmt.Errorf("--config needs [TREE=]PATH")
		}
		tree, path := "buildroot", args[i]
		if k, v, ok := strings.Cut(args[i], "="); ok {
			tree, path = k, v
		}
		configs[tree] = path
	}
	if len(configs) == 0 {
		return fmt.Errorf("usage: silt fixpoint IMAGE.sx --buildroot DIR --config [TREE=].config ...")
	}
	res, _, name, err := composeWithTree(rest)
	if err != nil {
		return err
	}

	trees := make([]string, 0, len(configs))
	for t := range configs {
		if _, ok := res.Trees[t]; !ok {
			return fmt.Errorf("--config %s: no such tree", t)
		}
		trees = append(trees, t)
	}
	sort.Strings(trees)

	bad := 0
	for _, t := range trees {
		f, err := os.Open(configs[t])
		if err != nil {
			return err
		}
		cfg, err := fixpoint.Read(f)
		f.Close()
		if err != nil {
			return err
		}
		ms := fixpoint.Check(res, lang.Scope(t), cfg)
		stated := 0
		for _, c := range res.Constraints[lang.Scope(t)] {
			if !c.Soft {
				stated++
			}
		}
		if len(ms) == 0 {
			fmt.Printf("ok  %s  %s: kbuild honoured all %d stated symbols (%s)\n", name, t, stated, configs[t])
			continue
		}
		bad += len(ms)
		fmt.Printf("\n%s  %s: kbuild did not honour %d of %d stated symbols (%s)\n", name, t, len(ms), stated, configs[t])
		for _, m := range ms {
			fmt.Printf("  %v\n", m)
		}
	}
	// Trees with constraints that were not compared are reported, not
	// silently passed: Invariant 9.
	for sc, cs := range res.Constraints {
		if _, ok := configs[string(sc)]; !ok && len(cs) > 0 {
			fmt.Printf("    %s: %d constraint(s) not compared; pass --config %s=PATH\n", sc, len(cs), sc)
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d symbol(s) differ from what %s asked for", bad, name)
	}
	return nil
}
