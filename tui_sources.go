package main

// The Sources tab: every package manager lindeck knows, whether it's
// installed, its repositories, and a form to add your own.

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m model) sourcesHeader() [][]seg {
	n := len(m.active())
	return [][]seg{
		{{fmt.Sprintf("  package sources — %d of %d active", n, len(m.sources)), sText},
			{"   ● installed  ○ not installed  ◌ disabled", sComment}},
	}
}

func (m model) sourceItems() []item {
	var items []item
	for _, s := range m.sources {
		items = append(items, item{src: s})
		if s.repos == nil || !s.Available() || m.disabled(s.ID) {
			continue
		}
		for i := range m.repos[s.ID] {
			items = append(items, item{src: s, repo: &m.repos[s.ID][i]})
		}
	}
	return append(items, item{addNew: true})
}

func (m model) sourceRows(it item) [][]seg {
	if it.addNew {
		return [][]seg{{}, {{"  + ", sOk}, {"add a custom package manager", sText}, {"   n", sHintKey}}}
	}
	s := it.src
	if it.repo != nil {
		r := it.repo
		tag := ""
		if r.Protected {
			tag = "  (system)"
		}
		return [][]seg{{
			{"      ↳ ", sComment}, {fmt.Sprintf("%-28s", trunc(r.Name, 28)), sText},
			{" " + r.Detail, sComment}, {tag, sComment},
		}}
	}

	icon, ist, state := icons.ok, sOk, "installed"
	switch {
	case m.disabled(s.ID):
		icon, ist, state = "◌", sComment, "disabled — e to enable"
	case !s.Available():
		icon, ist, state = icons.unknown, sWarn, "not installed — i to install"
		if s.setup == nil {
			state = "not installed"
		}
	}
	var extra []string
	if s.Available() && !m.disabled(s.ID) {
		if s.Root {
			extra = append(extra, "needs sudo")
		} else {
			extra = append(extra, "no sudo")
		}
		if s.repos != nil {
			if err := m.repoErr[s.ID]; err != nil {
				extra = append(extra, "repos: "+err.Error())
			} else if m.reposLoaded {
				extra = append(extra, fmt.Sprintf("%d %s", len(m.repos[s.ID]), plural(s.RepoKind, len(m.repos[s.ID]))))
			}
		}
	}
	if s.Custom {
		extra = append(extra, "custom")
	}
	return [][]seg{{}, {
		{"  ", sText}, {icon, ist}, {fmt.Sprintf(" %-10s ", s.Name), sTitle},
		{fmt.Sprintf("%-30s", state), sText}, {strings.Join(extra, " · "), sComment},
	}}
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	if strings.HasSuffix(word, "y") {
		return strings.TrimSuffix(word, "y") + "ies"
	}
	return word + "s"
}

// sourcesKey handles the Sources tab's own keys. ok is false for keys it
// leaves to the global handler.
func (m *model) sourcesKey(key string, sel *item) (tea.Cmd, bool) {
	var s *Source
	if sel != nil {
		s = sel.src
	}
	switch key {
	case "n":
		return m.startForm(), true
	case "r":
		m.reposLoaded = false
		return m.loadRepos(), true
	}
	if s == nil {
		return nil, false
	}
	switch key {
	case "enter":
		if !s.Available() || m.disabled(s.ID) {
			m.setStatus(sWarn, "%s isn't active — i installs it, e enables it", s.Name)
			return nil, true
		}
		m.tab = tabBrowse
		return m.setBrowse(s.ID), true

	case "i":
		if s.Available() {
			m.setStatus(sComment, "%s is already installed", s.Name)
			return nil, true
		}
		if s.setup == nil {
			m.setStatus(sWarn, "lindeck doesn't know how to install %s", s.Name)
			return nil, true
		}
		cmd := m.confirm(fmt.Sprintf("Install %s?", s.Name), func(m *model) tea.Cmd {
			return m.runJob(s.setup())
		})
		m.setStatus(sComment, "this %s", s.SetupHint)
		return cmd, true

	case "a":
		if s.repos == nil {
			m.setStatus(sWarn, "%s doesn't have repositories to add", s.Name)
			return nil, true
		}
		if !s.Available() {
			m.setStatus(sWarn, "install %s first (i)", s.Name)
			return nil, true
		}
		m.repoSrc = s.ID
		return m.prompt("repo", fmt.Sprintf("Add %s %s (%s): ", s.Name, s.RepoKind, s.RepoHint)), true

	case "d":
		if r := sel.repo; r != nil {
			if r.Protected {
				m.setStatus(sWarn, "%s is your distro's own source — lindeck won't remove it", r.Name)
				return nil, true
			}
			return m.confirm(fmt.Sprintf("Remove %s %s %s?", s.Name, s.RepoKind, r.Name), func(m *model) tea.Cmd {
				return m.runJob(s.delRepo(s, *r))
			}), true
		}
		if s.Custom {
			return m.confirm(fmt.Sprintf("Remove the custom source %s from lindeck? (it stays installed)", s.Name), func(m *model) tea.Cmd {
				m.removeCustom(s.ID)
				return nil
			}), true
		}
		m.setStatus(sWarn, "built-in sources can't be removed — e disables one instead")
		return nil, true

	case "e":
		if s.ID == "apt" {
			m.setStatus(sWarn, "apt is your system's package manager and is always on")
			return nil, true
		}
		m.toggleDisabled(s.ID)
		return nil, true
	}
	return nil, false
}

func (m *model) addRepo(srcID, spec string) tea.Cmd {
	s := m.src(srcID)
	if s == nil || spec == "" {
		return nil
	}
	j, err := s.addRepo(s, spec)
	if err != nil {
		m.setStatus(sErr, "%v", err)
		return nil
	}
	return m.runJob(j)
}

func (m *model) toggleDisabled(id string) {
	var out []string
	found := false
	for _, d := range m.cfg.Disabled {
		if d == id {
			found = true
		} else {
			out = append(out, d)
		}
	}
	if !found {
		out = append(out, id)
	}
	m.cfg.Disabled = out
	if found {
		m.save(id + " enabled")
	} else {
		m.save(id + " disabled — hidden from Installed, Browse and Scan")
		if m.browse == id {
			m.setBrowse("apt")
		}
		if m.instFilter == id {
			m.instFilter = ""
		}
	}
	m.scan = nil
}

func (m *model) removeCustom(id string) {
	var out []CustomSource
	for _, c := range m.cfg.Sources {
		if c.ID != id && slug(c.Name) != id {
			out = append(out, c)
		}
	}
	m.cfg.Sources = out
	m.sources = allSources(m.cfg)
	delete(m.inst, id)
	if m.browse == id {
		m.setBrowse("apt")
	}
	m.save("removed custom source " + id)
}

// ---------------------------------------------------------------------------
// Custom source form
// ---------------------------------------------------------------------------

type formStep struct {
	label    string
	required bool
	set      func(c *CustomSource, v string) error
}

var formSteps = []formStep{
	{"Name (e.g. Nix): ", true, func(c *CustomSource, v string) error {
		c.Name, c.ID = v, slug(v)
		if c.ID == "" {
			return errors.New("the name needs at least one letter or number")
		}
		return nil
	}},
	{"Command that shows it's installed (e.g. nix-env): ", true, func(c *CustomSource, v string) error { c.Bin = v; return nil }},
	{"Search command, {query} = search text (optional, e.g. nix search nixpkgs {query}): ", false,
		func(c *CustomSource, v string) error { c.Search = v; return nil }},
	{"List-installed command (optional, one package per line): ", false, func(c *CustomSource, v string) error { c.List = v; return nil }},
	{"Install command, {pkg} = package name (e.g. nix-env -iA nixpkgs.{pkg}): ", true, func(c *CustomSource, v string) error {
		if !strings.Contains(v, "{pkg}") {
			return errors.New("the install command needs {pkg} where the package name goes")
		}
		c.Install = v
		return nil
	}},
	{"Remove command, {pkg} = package name (optional): ", false, func(c *CustomSource, v string) error { c.Remove = v; return nil }},
	{"Upgrade command, {pkg} = package name (optional): ", false, func(c *CustomSource, v string) error { c.Upgrade = v; return nil }},
	{"Do installs need sudo? (y/N): ", false, func(c *CustomSource, v string) error {
		c.Root = strings.HasPrefix(strings.ToLower(v), "y")
		return nil
	}},
}

func (m *model) startForm() tea.Cmd {
	m.form, m.formStep = &CustomSource{}, 0
	m.setStatus(sComment, "adding a custom package manager — step 1 of %d, <Esc> to cancel", len(formSteps))
	return m.prompt("form", formSteps[0].label)
}

// formNext stores one answer and moves to the next question, saving the
// source after the last one.
func (m *model) formNext(val string) tea.Cmd {
	st := formSteps[m.formStep]
	if val == "" && st.required {
		m.setStatus(sErr, "this one is required — <Esc> to cancel")
		return nil
	}
	if val != "" || st.required {
		if err := st.set(m.form, val); err != nil {
			m.setStatus(sErr, "%v", err)
			return nil
		}
	}
	if m.formStep == 0 && m.src(m.form.ID) != nil {
		m.setStatus(sErr, "a source called %q already exists — pick another name", m.form.ID)
		return nil
	}
	m.formStep++
	if m.formStep < len(formSteps) {
		m.setStatus(sComment, "adding %s — step %d of %d, <Esc> to cancel", m.form.Name, m.formStep+1, len(formSteps))
		return m.prompt("form", formSteps[m.formStep].label)
	}

	m.inputKind = ""
	m.input.Blur()
	c := *m.form
	m.form = nil
	m.cfg.Sources = append(m.cfg.Sources, c)
	m.sources = allSources(m.cfg)
	m.save(fmt.Sprintf("added %s — edit it any time in config.json", c.Name))
	if s := m.src(c.ID); s != nil && s.Available() {
		return cmdInstalled(s)
	}
	return nil
}
