package main

import (
	"regexp"
	"strings"
)

// CustomSource is a package manager the user describes in config.json (or
// with the form on the Sources tab). Commands run through sh; {query} and
// {pkg} are replaced with the shell-quoted search text and package name.
//
// search and list should print one package per line:
//
//	name [version] [description…]
type CustomSource struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Bin     string `json:"bin"`               // command that shows it's installed, e.g. "nix-env"
	Root    bool   `json:"root,omitempty"`    // install / remove / upgrade need sudo
	Search  string `json:"search,omitempty"`  // e.g. "nix search nixpkgs {query}"
	List    string `json:"list,omitempty"`    // lists installed packages
	Install string `json:"install"`           // e.g. "nix-env -iA nixpkgs.{pkg}"
	Remove  string `json:"remove,omitempty"`  // e.g. "nix-env -e {pkg}"
	Upgrade string `json:"upgrade,omitempty"` // e.g. "nix-env -u {pkg}"
	Setup   string `json:"setup,omitempty"`   // installs the package manager itself
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slug turns a display name into a source ID: "My Tool" → "my-tool".
func slug(name string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func expand(tmpl string, vars map[string]string) string {
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{"+k+"}", shellQuote(v))
	}
	return tmpl
}

// parsePkgLines reads "name [version] [description…]" lines.
func parsePkgLines(src string, ls []string) []*Pkg {
	var pkgs []*Pkg
	for _, l := range ls {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		p := &Pkg{Source: src, Name: f[0]}
		if len(f) > 1 {
			p.Version = f[1]
		}
		if len(f) > 2 {
			p.Desc = strings.Join(f[2:], " ")
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func (c CustomSource) source() *Source {
	id := c.ID
	if id == "" {
		id = slug(c.Name)
	}
	sh := func(tmpl string, vars map[string]string) []string {
		if tmpl == "" {
			return nil
		}
		return []string{"/bin/sh", "-c", expand(tmpl, vars)}
	}
	s := &Source{ID: id, Name: c.Name, bins: []string{c.Bin}, Root: c.Root, Custom: true,
		SearchHint: "custom source"}
	if c.Search != "" {
		s.search = func(_ *Source, q string) ([]*Pkg, error) {
			ls, err := outLines("/bin/sh", "-c", expand(c.Search, map[string]string{"query": q}))
			return parsePkgLines(id, ls), err
		}
	} else {
		s.SearchHint = "this custom source has no search command — install by exact name with a on the Installed tab"
	}
	if c.List != "" {
		s.installed = func(*Source) ([]Inst, error) {
			ls, err := outLines("/bin/sh", "-c", c.List)
			var res []Inst
			for _, p := range parsePkgLines(id, ls) {
				res = append(res, Inst{Source: id, Name: p.Name, Version: p.Version, Summary: p.Desc, Manual: true})
			}
			return res, err
		}
	}
	s.install = func(_ *Source, p *Pkg) []string { return sh(c.Install, map[string]string{"pkg": p.Name}) }
	if c.Remove != "" {
		s.remove = func(_ *Source, p *Pkg) []string { return sh(c.Remove, map[string]string{"pkg": p.Name}) }
	}
	if c.Upgrade != "" {
		s.upgrade = func(_ *Source, p *Pkg) []string { return sh(c.Upgrade, map[string]string{"pkg": p.Name}) }
	}
	if c.Setup != "" {
		s.SetupHint = "runs: " + c.Setup
		s.setup = func() *job {
			return &job{label: "install " + c.Name, args: []string{"/bin/sh", "-c", c.Setup}, root: c.Root, redetect: true}
		}
	}
	return s
}
