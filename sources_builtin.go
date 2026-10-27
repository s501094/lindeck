package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

func builtinSources() []*Source {
	return []*Source{aptSource(), brewSource(), flatpakSource(), snapSource(),
		pipxSource(), cargoSource(), npmSource()}
}

// aptInstall is how a package manager that comes from apt gets installed.
func aptInstall(name, pkg string) func() *job {
	return func() *job {
		return &job{label: "install " + name, args: []string{"apt-get", "install", "-y", "--", pkg}, root: true, redetect: true}
	}
}

// ---------------------------------------------------------------------------
// apt
// ---------------------------------------------------------------------------

func aptSource() *Source {
	return &Source{
		ID: "apt", Name: "apt", bins: []string{"apt-get"}, Root: true, Catalog: true, Sections: true,
		catalog:   func(*Source) ([]*Pkg, error) { return aptCatalog() },
		installed: func(*Source) ([]Inst, error) { return loadInstalled(), nil },
		info:      func(_ *Source, p *Pkg) []string { return pkgDetails(p.Name) },
		install:   func(_ *Source, p *Pkg) []string { return aptArgs("install", p.Name) },
		remove:    func(_ *Source, p *Pkg) []string { return aptArgs("remove", p.Name) },
		upgrade: func(_ *Source, p *Pkg) []string {
			return []string{"apt-get", "install", "-y", "-o", "Dpkg::Progress-Fancy=0", "--only-upgrade", "--", p.Name}
		},
		upgradeAll: func(*Source) []string { return []string{"apt-get", "upgrade", "-y", "-o", "Dpkg::Progress-Fancy=0"} },

		RepoKind: "repository", RepoHint: "ppa:user/name, or a full \"deb …\" line",
		repos: func(*Source) ([]Repo, error) { return aptRepos() },
		addRepo: func(_ *Source, spec string) (*job, error) {
			if _, err := os.Stat("/usr/bin/add-apt-repository"); err != nil {
				return nil, errors.New("add-apt-repository is missing — install software-properties-common first")
			}
			return &job{label: "add repository " + spec, args: []string{"add-apt-repository", "-y", spec},
				root: true, reloadSource: "apt", reloadRepos: "apt"}, nil
		},
		delRepo: func(_ *Source, r Repo) *job {
			return chain(
				&job{label: "remove " + r.Name, args: []string{"rm", "-f", "--", r.Path}, root: true},
				&job{label: "apt update", args: []string{"apt-get", "update"}, root: true, reloadSource: "apt", reloadRepos: "apt"},
			)
		},
	}
}

func aptArgs(action, pkg string) []string {
	return []string{"apt-get", action, "-y", "-o", "Dpkg::Progress-Fancy=0", "--", pkg}
}

// aptRepos lists the files that define apt repositories, one Repo per file.
func aptRepos() ([]Repo, error) {
	var files []string
	if _, err := os.Stat("/etc/apt/sources.list"); err == nil {
		files = append(files, "/etc/apt/sources.list")
	}
	for _, pat := range []string{"/etc/apt/sources.list.d/*.list", "/etc/apt/sources.list.d/*.sources"} {
		m, _ := filepath.Glob(pat)
		files = append(files, m...)
	}
	var repos []Repo
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var uris []string
		for _, l := range strings.Split(string(data), "\n") {
			l = strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(l, "#") || l == "":
			case strings.HasPrefix(l, "URIs:"):
				uris = append(uris, strings.Fields(strings.TrimPrefix(l, "URIs:"))...)
			case strings.HasPrefix(l, "deb ") || strings.HasPrefix(l, "deb-src "):
				for _, f := range strings.Fields(l)[1:] {
					if strings.Contains(f, "://") {
						uris = append(uris, f)
						break
					}
				}
			}
		}
		if len(uris) == 0 {
			continue // empty or fully commented out
		}
		base := filepath.Base(f)
		protected := f == "/etc/apt/sources.list" || base == "ubuntu.sources" || base == "debian.sources"
		repos = append(repos, Repo{Name: base, Detail: strings.Join(dedupe(uris), "  "), Path: f, Protected: protected})
	}
	return repos, nil
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Homebrew
// ---------------------------------------------------------------------------

func brewSource() *Source {
	home, _ := os.UserHomeDir()
	return &Source{
		ID: "brew", Name: "Homebrew",
		bins:    []string{"brew", "/home/linuxbrew/.linuxbrew/bin/brew", filepath.Join(home, ".linuxbrew/bin/brew")},
		Catalog: true,
		catalog: func(s *Source) ([]*Pkg, error) {
			if pkgs := brewAPICache(); pkgs != nil {
				return pkgs, nil
			}
			names, err := outLines(s.Bin(), "formulae")
			if err != nil {
				return nil, err
			}
			pkgs := make([]*Pkg, 0, len(names))
			for _, n := range names {
				pkgs = append(pkgs, &Pkg{Source: "brew", Name: strings.TrimSpace(n), Section: "formula"})
			}
			return pkgs, nil
		},
		installed: func(s *Source) ([]Inst, error) {
			ls, err := outLines(s.Bin(), "list", "--formula", "--versions")
			if err != nil {
				return nil, err
			}
			var res []Inst
			for _, l := range ls {
				f := strings.Fields(l)
				in := Inst{Source: "brew", Name: f[0], Manual: true}
				if len(f) > 1 {
					in.Version = f[len(f)-1]
				}
				res = append(res, in)
			}
			return res, nil
		},
		outdated: func(s *Source) (map[string]string, error) {
			// "name (1.0) < 1.1"
			ls, err := outLines(s.Bin(), "outdated", "--formula", "--verbose")
			res := map[string]string{}
			for _, l := range ls {
				if f := strings.Fields(l); len(f) >= 2 {
					res[f[0]] = f[len(f)-1]
				}
			}
			return res, err
		},
		info:       func(s *Source, p *Pkg) []string { return infoLines(s.Bin(), "info", p.Name) },
		install:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "install", p.Name} },
		remove:     func(s *Source, p *Pkg) []string { return []string{s.Bin(), "uninstall", p.Name} },
		upgrade:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "upgrade", p.Name} },
		upgradeAll: func(s *Source) []string { return []string{s.Bin(), "upgrade"} },

		RepoKind: "tap", RepoHint: "user/repo, or user/repo followed by a git URL",
		repos: func(s *Source) ([]Repo, error) {
			ls, err := outLines(s.Bin(), "tap")
			var repos []Repo
			for _, l := range ls {
				repos = append(repos, Repo{Name: strings.TrimSpace(l)})
			}
			return repos, err
		},
		addRepo: func(s *Source, spec string) (*job, error) {
			f := strings.Fields(spec)
			if len(f) == 0 || len(f) > 2 || !strings.Contains(f[0], "/") {
				return nil, errors.New("a tap looks like user/repo, optionally followed by a git URL")
			}
			return &job{label: "tap " + f[0], args: append([]string{s.Bin(), "tap"}, f...),
				reloadSource: "brew", reloadRepos: "brew"}, nil
		},
		delRepo: func(s *Source, r Repo) *job {
			return &job{label: "untap " + r.Name, args: []string{s.Bin(), "untap", r.Name}, reloadSource: "brew", reloadRepos: "brew"}
		},

		SetupHint: "runs Homebrew's official installer; it uses sudo once to create /home/linuxbrew",
		setup: func() *job {
			return &job{
				label: "install Homebrew",
				args: []string{"/bin/bash", "-c",
					`/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"`},
				preauth: true, redetect: true,
			}
		},
	}
}

// brewAPICache reads the catalog Homebrew downloads on `brew update`, which
// has descriptions, versions and homepages that `brew formulae` lacks. It
// returns nil when the cache is missing or unreadable.
func brewAPICache() []*Pkg {
	dir := os.Getenv("HOMEBREW_CACHE")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache", "Homebrew")
	}
	data, err := os.ReadFile(filepath.Join(dir, "api", "formula.jws.json"))
	if err != nil {
		return nil
	}
	var jws struct{ Payload string }
	if json.Unmarshal(data, &jws) != nil || jws.Payload == "" {
		return nil
	}
	var formulae []struct {
		Name, Desc, Homepage string
		Versions             struct{ Stable string }
	}
	if json.Unmarshal([]byte(jws.Payload), &formulae) != nil {
		return nil
	}
	pkgs := make([]*Pkg, 0, len(formulae))
	for _, f := range formulae {
		pkgs = append(pkgs, &Pkg{Source: "brew", Name: f.Name, Version: f.Versions.Stable, Desc: f.Desc,
			Homepage: f.Homepage, Section: "formula"})
	}
	return pkgs
}

// infoLines runs an info command and returns its output for the details view.
func infoLines(bin string, args ...string) []string {
	out, err := output(bin, args...)
	if err != nil {
		return []string{"no details available: " + err.Error()}
	}
	return splitLines(out)
}

// ---------------------------------------------------------------------------
// Flatpak
// ---------------------------------------------------------------------------

const flathubURL = "https://dl.flathub.org/repo/flathub.flatpakrepo"

func flatpakSource() *Source {
	return &Source{
		ID: "flatpak", Name: "Flatpak", bins: []string{"flatpak"}, Catalog: true,
		catalog: func(s *Source) ([]*Pkg, error) {
			ls, err := outLines(s.Bin(), "remote-ls", "--app", "--columns=application,name,version,description,origin")
			if err != nil {
				return nil, err
			}
			var pkgs []*Pkg
			for _, l := range ls {
				f := tabFields(l, 5)
				desc := f[1]
				if f[3] != "" {
					desc += " — " + f[3]
				}
				pkgs = append(pkgs, &Pkg{Source: "flatpak", Name: f[0], Version: f[2], Desc: desc, Section: f[4]})
			}
			return pkgs, nil
		},
		installed: func(s *Source) ([]Inst, error) {
			ls, err := outLines(s.Bin(), "list", "--app", "--columns=application,version,origin,name")
			var res []Inst
			for _, l := range ls {
				f := tabFields(l, 4)
				res = append(res, Inst{Source: "flatpak", Name: f[0], Version: f[1], Origin: f[2], Summary: f[3], Manual: true})
			}
			return res, err
		},
		outdated: func(s *Source) (map[string]string, error) {
			ls, err := outLines(s.Bin(), "remote-ls", "--updates", "--app", "--columns=application,version")
			res := map[string]string{}
			for _, l := range ls {
				f := tabFields(l, 2)
				res[f[0]] = f[1]
			}
			return res, err
		},
		info: func(s *Source, p *Pkg) []string {
			if out, err := output(s.Bin(), "info", p.Name); err == nil {
				return splitLines(out)
			}
			origin := p.Section
			if origin == "" {
				origin = "flathub"
			}
			return infoLines(s.Bin(), "remote-info", origin, p.Name)
		},
		install: func(s *Source, p *Pkg) []string {
			origin := p.Section
			if origin == "" {
				origin = "flathub"
			}
			return []string{s.Bin(), "install", "--user", "-y", "--noninteractive", origin, p.Name}
		},
		remove: func(s *Source, p *Pkg) []string {
			return []string{s.Bin(), "uninstall", "-y", "--noninteractive", p.Name}
		},
		upgrade:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "update", "-y", "--noninteractive", p.Name} },
		upgradeAll: func(s *Source) []string { return []string{s.Bin(), "update", "-y", "--noninteractive"} },

		RepoKind: "remote", RepoHint: "name followed by URL, or a .flatpakrepo URL",
		repos: func(s *Source) ([]Repo, error) {
			ls, err := outLines(s.Bin(), "remotes", "--columns=name,url,options")
			var repos []Repo
			for _, l := range ls {
				f := tabFields(l, 3)
				repos = append(repos, Repo{Name: f[0], Detail: strings.TrimSpace(f[1] + "  " + f[2]), System: strings.Contains(f[2], "system")})
			}
			return repos, err
		},
		addRepo: func(s *Source, spec string) (*job, error) {
			f := strings.Fields(spec)
			var name, u string
			switch {
			case len(f) == 2:
				name, u = f[0], f[1]
			case len(f) == 1 && strings.Contains(f[0], "://"):
				u = f[0]
				name = strings.TrimSuffix(filepath.Base(u), ".flatpakrepo")
			default:
				return nil, errors.New("give a name and a URL, like: fedora oci+https://registry.fedoraproject.org")
			}
			return &job{label: "add remote " + name, args: []string{s.Bin(), "remote-add", "--user", "--if-not-exists", name, u},
				reloadSource: "flatpak", reloadRepos: "flatpak"}, nil
		},
		delRepo: func(s *Source, r Repo) *job {
			if r.System {
				return &job{label: "remove remote " + r.Name, args: []string{s.Bin(), "remote-delete", "--system", r.Name},
					root: true, reloadSource: "flatpak", reloadRepos: "flatpak"}
			}
			return &job{label: "remove remote " + r.Name, args: []string{s.Bin(), "remote-delete", "--user", r.Name},
				reloadSource: "flatpak", reloadRepos: "flatpak"}
		},

		SetupHint: "installs flatpak with apt, then adds Flathub for your user",
		setup: func() *job {
			return chain(aptInstall("Flatpak", "flatpak")(),
				&job{label: "add Flathub", args: []string{"flatpak", "remote-add", "--user", "--if-not-exists", "flathub", flathubURL}, redetect: true})
		},
	}
}

// tabFields splits flatpak's tab-separated columns, padding to n fields.
func tabFields(l string, n int) []string {
	f := strings.Split(l, "\t")
	if len(f) == 1 { // older versions align with spaces instead
		f = strings.Fields(l)
	}
	for len(f) < n {
		f = append(f, "")
	}
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	return f
}

// ---------------------------------------------------------------------------
// Snap
// ---------------------------------------------------------------------------

func snapSource() *Source {
	return &Source{
		ID: "snap", Name: "Snap", bins: []string{"snap"}, Root: true,
		SearchHint: "searches the Snap Store",
		search: func(s *Source, q string) ([]*Pkg, error) {
			rows, err := columns(s.Bin(), "find", q)
			var pkgs []*Pkg
			for _, r := range rows {
				pkgs = append(pkgs, &Pkg{Source: "snap", Name: r["Name"], Version: r["Version"], Desc: r["Summary"], Section: r["Notes"]})
			}
			return pkgs, err
		},
		installed: func(s *Source) ([]Inst, error) {
			rows, err := columns(s.Bin(), "list")
			var res []Inst
			for _, r := range rows {
				notes := r["Notes"]
				// bases and snapd itself are dependencies, like apt's auto packages
				manual := !strings.Contains(notes, "base") && r["Name"] != "snapd"
				res = append(res, Inst{Source: "snap", Name: r["Name"], Version: r["Version"], Summary: r["Publisher"], Manual: manual})
			}
			return res, err
		},
		outdated: func(s *Source) (map[string]string, error) {
			rows, err := columns(s.Bin(), "refresh", "--list")
			res := map[string]string{}
			for _, r := range rows {
				res[r["Name"]] = r["Version"]
			}
			return res, err
		},
		info: func(s *Source, p *Pkg) []string { return infoLines(s.Bin(), "info", p.Name) },
		install: func(s *Source, p *Pkg) []string {
			args := []string{"snap", "install", p.Name}
			if strings.Contains(p.Section, "classic") {
				args = append(args, "--classic")
			}
			return args
		},
		remove:     func(_ *Source, p *Pkg) []string { return []string{"snap", "remove", p.Name} },
		upgrade:    func(_ *Source, p *Pkg) []string { return []string{"snap", "refresh", p.Name} },
		upgradeAll: func(*Source) []string { return []string{"snap", "refresh"} },
		SetupHint:  "installs snapd with apt",
		setup:      aptInstall("Snap", "snapd"),
	}
}

// columns parses the aligned tables snap prints, keyed by header name.
func columns(bin string, args ...string) ([]map[string]string, error) {
	ls, err := outLines(bin, args...)
	if err != nil || len(ls) < 2 {
		return nil, err
	}
	header := ls[0]
	type col struct {
		name  string
		start int
	}
	var cols []col
	for i := 0; i < len(header); {
		if header[i] == ' ' {
			i++
			continue
		}
		j := i
		for j < len(header) && header[j] != ' ' {
			j++
		}
		cols = append(cols, col{header[i:j], i})
		i = j
	}
	var rows []map[string]string
	for _, l := range ls[1:] {
		r := map[string]string{}
		rl := []rune(l)
		for k, c := range cols {
			start, end := c.start, len(rl)
			if k+1 < len(cols) {
				end = cols[k+1].start
			}
			if start >= len(rl) {
				continue
			}
			r[c.name] = strings.TrimSpace(string(rl[start:min(end, len(rl))]))
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// pipx
// ---------------------------------------------------------------------------

func pipxSource() *Source {
	return &Source{
		ID: "pipx", Name: "pipx", bins: []string{"pipx"},
		SearchHint: "PyPI has no search — type an exact package name, like httpie",
		search: func(_ *Source, q string) ([]*Pkg, error) {
			p, err := pypiLookup(strings.TrimSpace(q))
			if err != nil || p == nil {
				return nil, err
			}
			return []*Pkg{p}, nil
		},
		installed: func(s *Source) ([]Inst, error) {
			ls, err := outLines(s.Bin(), "list", "--short")
			var res []Inst
			for _, l := range ls {
				if f := strings.Fields(l); len(f) >= 2 {
					res = append(res, Inst{Source: "pipx", Name: f[0], Version: f[1], Manual: true})
				}
			}
			return res, err
		},
		info: func(_ *Source, p *Pkg) []string {
			if q, err := pypiLookup(p.Name); err == nil && q != nil {
				return []string{"version:         " + q.Version, "homepage:        " + q.Homepage, "", q.Desc}
			}
			return []string{"no details available"}
		},
		install:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "install", p.Name} },
		remove:     func(s *Source, p *Pkg) []string { return []string{s.Bin(), "uninstall", p.Name} },
		upgrade:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "upgrade", p.Name} },
		upgradeAll: func(s *Source) []string { return []string{s.Bin(), "upgrade-all"} },
		SetupHint:  "installs pipx with apt",
		setup:      aptInstall("pipx", "pipx"),
	}
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

// pypiLookup fetches one package from PyPI's JSON API; nil if it doesn't exist.
func pypiLookup(name string) (*Pkg, error) {
	if name == "" {
		return nil, nil
	}
	resp, err := httpClient.Get("https://pypi.org/pypi/" + url.PathEscape(name) + "/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PyPI: %s", resp.Status)
	}
	var raw struct {
		Info struct {
			Name        string            `json:"name"`
			Version     string            `json:"version"`
			Summary     string            `json:"summary"`
			HomePage    string            `json:"home_page"`
			ProjectURLs map[string]string `json:"project_urls"`
		} `json:"info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	home := raw.Info.HomePage
	for _, k := range []string{"Homepage", "homepage", "Source", "Repository"} {
		if home == "" {
			home = raw.Info.ProjectURLs[k]
		}
	}
	return &Pkg{Source: "pipx", Name: raw.Info.Name, Version: raw.Info.Version, Desc: raw.Info.Summary, Homepage: home}, nil
}

// ---------------------------------------------------------------------------
// cargo
// ---------------------------------------------------------------------------

func cargoSource() *Source {
	home, _ := os.UserHomeDir()
	return &Source{
		ID: "cargo", Name: "cargo", bins: []string{"cargo", filepath.Join(home, ".cargo/bin/cargo")},
		SearchHint: "searches crates.io",
		search: func(s *Source, q string) ([]*Pkg, error) {
			// ripgrep = "15.2.0"       # description
			ls, err := outLines(s.Bin(), "search", q, "--limit", "50")
			var pkgs []*Pkg
			for _, l := range ls {
				name, rest, ok := strings.Cut(l, " = ")
				if !ok || strings.HasPrefix(l, "...") {
					continue
				}
				ver, desc, _ := strings.Cut(rest, "#")
				pkgs = append(pkgs, &Pkg{Source: "cargo", Name: strings.TrimSpace(name),
					Version: strings.Trim(strings.TrimSpace(ver), `"`), Desc: strings.TrimSpace(desc),
					Homepage: "https://crates.io/crates/" + strings.TrimSpace(name)})
			}
			return pkgs, err
		},
		installed: func(s *Source) ([]Inst, error) {
			// "lofi-tui v0.1.0 (/path):" then indented binary names
			ls, err := outLines(s.Bin(), "install", "--list")
			var res []Inst
			for _, l := range ls {
				if strings.HasPrefix(l, " ") {
					continue
				}
				f := strings.Fields(strings.TrimSuffix(l, ":"))
				if len(f) >= 2 {
					res = append(res, Inst{Source: "cargo", Name: f[0], Version: strings.TrimSuffix(strings.TrimPrefix(f[1], "v"), ":"), Manual: true})
				}
			}
			return res, err
		},
		info: func(s *Source, p *Pkg) []string {
			return []string{"version:         " + p.Version, "crates.io:       https://crates.io/crates/" + p.Name, "", p.Desc}
		},
		install:   func(s *Source, p *Pkg) []string { return []string{s.Bin(), "install", p.Name} },
		remove:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "uninstall", p.Name} },
		upgrade:   func(s *Source, p *Pkg) []string { return []string{s.Bin(), "install", p.Name} }, // reinstalls if newer
		SetupHint: "runs rustup's official installer for your user",
		setup: func() *job {
			return &job{label: "install Rust (rustup)", args: []string{"/bin/sh", "-c",
				"curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y"}, redetect: true}
		},
	}
}

// ---------------------------------------------------------------------------
// npm
// ---------------------------------------------------------------------------

func npmSource() *Source {
	s := &Source{
		ID: "npm", Name: "npm", bins: []string{"npm"},
		SearchHint: "searches the npm registry; installs globally (-g)",
		search: func(s *Source, q string) ([]*Pkg, error) {
			out, err := output(s.Bin(), "search", "--json", "--searchlimit", "40", q)
			if err != nil {
				return nil, err
			}
			var res []struct {
				Name, Version, Description string
				Links                      struct{ Homepage, Npm string }
			}
			if err := json.Unmarshal(out, &res); err != nil {
				return nil, err
			}
			var pkgs []*Pkg
			for _, r := range res {
				home := r.Links.Homepage
				if home == "" {
					home = r.Links.Npm
				}
				pkgs = append(pkgs, &Pkg{Source: "npm", Name: r.Name, Version: r.Version, Desc: r.Description, Homepage: home})
			}
			return pkgs, nil
		},
		installed: func(s *Source) ([]Inst, error) {
			out, err := output(s.Bin(), "ls", "-g", "--depth=0", "--json")
			if err != nil {
				return nil, err
			}
			var data struct {
				Dependencies map[string]struct{ Version string }
			}
			if err := json.Unmarshal(out, &data); err != nil {
				return nil, err
			}
			var res []Inst
			for name, d := range data.Dependencies {
				res = append(res, Inst{Source: "npm", Name: name, Version: d.Version, Manual: name != "npm" && name != "corepack"})
			}
			sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
			return res, nil
		},
		outdated: func(s *Source) (map[string]string, error) {
			out, err := output(s.Bin(), "outdated", "-g", "--json")
			res := map[string]string{}
			if err != nil {
				return res, err
			}
			var data map[string]struct{ Latest string }
			_ = json.Unmarshal(out, &data)
			for name, d := range data {
				res[name] = d.Latest
			}
			return res, nil
		},
		info:       func(s *Source, p *Pkg) []string { return infoLines(s.Bin(), "view", p.Name) },
		install:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "install", "-g", p.Name} },
		remove:     func(s *Source, p *Pkg) []string { return []string{s.Bin(), "uninstall", "-g", p.Name} },
		upgrade:    func(s *Source, p *Pkg) []string { return []string{s.Bin(), "install", "-g", p.Name + "@latest"} },
		upgradeAll: func(s *Source) []string { return []string{s.Bin(), "update", "-g"} },
		SetupHint:  "installs npm with apt",
		setup:      aptInstall("npm", "npm"),
	}
	// Global installs need root only when the global prefix isn't yours
	// (a distro npm under /usr); with nvm, mise or a user prefix they don't.
	if bin := s.Bin(); bin != "" {
		if out, err := output(bin, "prefix", "-g"); err == nil {
			s.Root = syscall.Access(strings.TrimSpace(string(out)), 2 /* W_OK */) != nil
		}
	}
	return s
}
