// Package pack loads a Silt pack: a directory someone else maintains,
// containing fragments, the br2-external tree that implements them, and a
// declaration of what it offers.
//
// The point of the declaration is that consuming a pack should not require
// reading it. A pack says which fragments it exports and which releases it
// was written against, and Silt checks both rather than discovering them by
// composing something and watching it fail.
package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vinodhalaharvi/silt/lang"
)

// Pack is a loaded pack: its declaration, its fragment files, and the
// absolute path of its br2-external tree if it has one.
type Pack struct {
	Decl     *lang.PackDecl
	Files    []*lang.File
	External string
}

// Load reads dir/silt.sx and every .sx file under dir/fragments.
func Load(dir string) (*Pack, error) {
	declPath := filepath.Join(dir, "silt.sx")
	src, err := os.ReadFile(declPath)
	if err != nil {
		return nil, fmt.Errorf("%s is not a pack: %w", dir, err)
	}
	f, err := lang.ParseFile(string(src), declPath)
	if err != nil {
		return nil, err
	}
	if len(f.Packs) != 1 {
		return nil, fmt.Errorf("%s: expected exactly one (pack ...), got %d", declPath, len(f.Packs))
	}
	p := &Pack{Decl: f.Packs[0]}
	p.Decl.Dir = dir
	if len(f.Fragments) > 0 || len(f.Images) > 0 {
		return nil, fmt.Errorf("%s declares the pack; its fragments belong under %s",
			declPath, filepath.Join(dir, "fragments"))
	}
	// The declaration file may still carry capability and tree declarations,
	// which are vocabulary rather than content.
	p.Files = append(p.Files, f)

	fragDir := filepath.Join(dir, "fragments")
	err = filepath.Walk(fragDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".sx") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		ff, err := lang.ParseFile(string(data), path)
		if err != nil {
			return err
		}
		p.Files = append(p.Files, ff)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if p.Decl.External != "" {
		ext, err := filepath.Abs(filepath.Join(dir, p.Decl.External))
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(ext, "external.desc")); err != nil {
			return nil, fmt.Errorf("%s: (external %q) is not a br2-external tree: no external.desc",
				declPath, p.Decl.External)
		}
		p.External = ext
	}
	return p, nil
}

// Problem is something wrong with a pack itself, rather than with an image
// that uses it.
type Problem struct {
	Pack    string
	Message string
}

func (p Problem) Error() string { return fmt.Sprintf("pack %s: %s", p.Pack, p.Message) }

// Check verifies a pack against its own declaration: everything it promises
// exists, nothing else is exported, and the trees it was written against are
// the ones in use. versions maps a tree name to the release being checked
// against; a tree absent from it is not checked rather than assumed to match.
func (p *Pack) Check(versions map[string]string) []Problem {
	var out []Problem
	add := func(format string, args ...any) {
		out = append(out, Problem{Pack: p.Decl.Name, Message: fmt.Sprintf(format, args...)})
	}

	defined := map[lang.ID]string{}
	for _, f := range p.Files {
		for _, fr := range f.Fragments {
			defined[fr.ID] = fr.Pos.Short()
		}
	}
	promised := map[lang.ID]bool{}
	for _, id := range p.Decl.Provides {
		promised[id] = true
		if _, ok := defined[id]; !ok {
			add("promises %s, which none of its fragments define", id)
		}
	}
	// The other direction matters as much: a fragment nobody declared is one
	// a consumer can compose and the pack does not know it maintains.
	var extra []string
	for id, pos := range defined {
		if !promised[id] {
			extra = append(extra, fmt.Sprintf("%s (%s)", id, pos))
		}
	}
	sort.Strings(extra)
	for _, e := range extra {
		add("defines %s but does not declare it in (provides ...)", e)
	}

	for name, want := range p.Decl.Requires {
		have, ok := versions[name]
		if !ok {
			continue // not being checked against that tree in this run
		}
		okv, err := lang.Satisfies(have, want)
		if err != nil {
			add("%v", err)
			continue
		}
		if !okv {
			add("needs %s %s; checking against %s", name, want, have)
		}
	}
	return out
}
