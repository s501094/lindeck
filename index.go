package main

import (
	"bufio"
	"os/exec"
	"sort"
	"strings"
)

// Pkg is one available package from any source. Section is the apt
// section, the Flatpak remote, or the snap's confinement, depending on source.
type Pkg struct {
	Source                                 string
	Name, Version, Section, Desc, Homepage string
}

// Section is an apt section (category) and how many packages are in it.
type Section struct {
	Name  string
	Count int
}

// Index is a source's whole catalog held in memory, for fast search and browsing.
type Index struct {
	byName   map[string]*Pkg
	all      []*Pkg
	sections []Section
}

// loadIndex parses `apt-cache dumpavail` (~90k packages, under a second).
func loadIndex() (*Index, error) {
	pkgs, err := aptCatalog()
	if err != nil {
		return nil, err
	}
	return newIndex(pkgs, true), nil
}

func aptCatalog() ([]*Pkg, error) {
	cmd := exec.Command("apt-cache", "dumpavail")
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var pkgs []*Pkg
	cur := &Pkg{Source: "apt"}
	flush := func() {
		if cur.Name != "" {
			pkgs = append(pkgs, cur)
		}
		cur = &Pkg{Source: "apt"}
	}

	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if line[0] == ' ' {
			continue // long-description continuation
		}
		key, val, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch key {
		case "Package":
			cur.Name = val
		case "Version":
			cur.Version = val
		case "Section":
			// "universe/devel" and "devel" are the same category
			if _, s, ok := strings.Cut(val, "/"); ok {
				val = s
			}
			cur.Section = val
		case "Description":
			cur.Desc = val
		case "Homepage":
			cur.Homepage = val
		}
	}
	flush()
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return pkgs, nil
}

// newIndex builds an index from a catalog, keeping the first entry for each
// name. With sections, packages are also counted per Section for browsing.
func newIndex(pkgs []*Pkg, sections bool) *Index {
	idx := &Index{byName: map[string]*Pkg{}}
	counts := map[string]int{}
	for _, p := range pkgs {
		if p.Name == "" || idx.byName[p.Name] != nil {
			continue
		}
		idx.byName[p.Name] = p
		idx.all = append(idx.all, p)
		counts[p.Section]++
	}
	sort.Slice(idx.all, func(i, j int) bool { return idx.all[i].Name < idx.all[j].Name })
	if sections {
		for name, n := range counts {
			if name != "" {
				idx.sections = append(idx.sections, Section{name, n})
			}
		}
		sort.Slice(idx.sections, func(i, j int) bool { return idx.sections[i].Name < idx.sections[j].Name })
	}
	return idx
}

// Search matches names and descriptions, ranking exact, prefix, and name
// matches ahead of description-only matches.
func (idx *Index) Search(term string) []*Pkg {
	term = strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return idx.all
	}
	type hit struct {
		p    *Pkg
		rank int
	}
	var hits []hit
	for _, p := range idx.all {
		switch {
		case p.Name == term:
			hits = append(hits, hit{p, 0})
		case strings.HasPrefix(p.Name, term):
			hits = append(hits, hit{p, 1})
		case strings.Contains(p.Name, term):
			hits = append(hits, hit{p, 2})
		case strings.Contains(strings.ToLower(p.Name), term):
			hits = append(hits, hit{p, 2})
		case strings.Contains(strings.ToLower(p.Desc), term):
			hits = append(hits, hit{p, 3})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank < hits[j].rank })
	res := make([]*Pkg, len(hits))
	for i, h := range hits {
		res[i] = h.p
	}
	return res
}

// InSection returns every package in a section, sorted by name.
func (idx *Index) InSection(name string) []*Pkg {
	var res []*Pkg
	for _, p := range idx.all {
		if p.Section == name {
			res = append(res, p)
		}
	}
	return res
}
