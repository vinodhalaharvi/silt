package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/vinodhalaharvi/silt/kconfig"
)

func main() {
	t, err := kconfig.Load("Config.in", kconfig.Options{
		Root: os.Args[1],
		Env: map[string]string{
			"BR2_BASE_DIR":     "/tmp/nonexistent",
			"HOSTARCH":         "x86_64",
			"HOST_GCC_VERSION": "13 2",
			"BR2_VERSION_FULL": "2025.02",
			"BR2_EXTERNAL":     "",
		},
	})
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
	byType := map[string]int{}
	withDeps, withSel, withDef := 0, 0, 0
	for _, s := range t.Symbols {
		byType[s.Type.String()]++
		if s.Depends != nil {
			withDeps++
		}
		withSel += len(s.Selects)
		withDef += len(s.Defaults)
	}
	fmt.Printf("symbols   %d\n", len(t.Symbols))
	fmt.Printf("choices   %d\n", len(t.Choices))
	fmt.Printf("depends   %d symbols\n", withDeps)
	fmt.Printf("selects   %d\n", withSel)
	fmt.Printf("defaults  %d\n", withDef)
	keys := []string{}
	for k := range byType {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-9s %d\n", k, byType[k])
	}
	big := 0
	var bigc string
	for _, c := range t.Choices {
		if len(c.Members) > big {
			big, bigc = len(c.Members), fmt.Sprintf("%s:%d", c.File, c.Line)
		}
	}
	fmt.Printf("largest choice: %d members at %s\n", big, bigc)
	for _, n := range t.Order {
		if t.Symbols[n].Type == kconfig.Unknown {
			s := t.Symbols[n]
			fmt.Printf("untyped: %s at %s:%d\n", s.Name, s.File, s.Line)
		}
	}
	if s, ok := t.Symbols["BR2_PACKAGE_LIBCAMERA"]; ok {
		fmt.Printf("\nBR2_PACKAGE_LIBCAMERA (%s) %s:%d\n", s.Type, s.File, s.Line)
		fmt.Printf("  depends %s\n", s.Depends)
		for _, sel := range s.Selects {
			fmt.Printf("  select  %s if %s\n", sel.Target, sel.Cond)
		}
	}
}
