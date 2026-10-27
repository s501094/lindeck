package main

// Floating-window UI with five tabs: Installed, Scan, Browse, Sources and
// Theme. Layout and keys mirror plugdeck.nvim.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// Styles the UI draws with. applyTheme (theme.go) sets them from the
// selected theme before the first frame.
var (
	sText, sTitle, sComment, sOk, sWarn, sErr lipgloss.Style
	sDir, sBorder, sWinTtl, sPrompt           lipgloss.Style
	sHint, sHintKey                           lipgloss.Style
)

var icons = struct{ ok, warn, err, unknown, manual, auto string }{
	ok: "●", warn: "▲", err: "✗", unknown: "○", manual: "◉", auto: "◍",
}

type tab int

const (
	tabInstalled tab = iota
	tabScan
	tabBrowse
	tabSources
	tabTheme
)

type (
	catalogMsg struct {
		src string
		idx *Index
		err error
	}
	searchMsg struct {
		src, query string
		pkgs       []*Pkg
		err        error
	}
	instMsg struct {
		src  string
		list []Inst
		err  error
	}
	reposMsg struct {
		src   string
		repos []Repo
		err   error
	}
	scanMsg    []Check
	detailsMsg struct {
		title, src string
		lines      []string
	}
	validatedMsg struct{ err error }
	lineMsg      string
	linesDoneMsg struct{}
	runDoneMsg   struct{ err error }
)

// seg is a styled run of text; a row is a slice of them.
type seg struct {
	t string
	s lipgloss.Style
}

// item is one selectable entry, which may span several rows.
type item struct {
	name    string // package name the item acts on
	pkg     *Pkg
	inst    *Inst
	check   *Check
	section string
	theme   string // preset name, or customName, on the Theme tab
	isSlot  bool   // a custom color row on the Theme tab
	slot    int    // index into slots when isSlot
	src     *Source
	repo    *Repo
	addNew  bool // the "add a custom package manager" row
}

type model struct {
	width, height int
	tab           tab

	cfg     Config    // saved settings: theme, custom sources, disabled sources
	sources []*Source // every known source, built-in then custom

	inst       map[string][]Inst // installed packages per source
	instErr    map[string]error
	instSet    map[string]bool // "source/name" → installed
	instFilter string          // "" = all sources
	showAll    bool            // include dependencies (apt auto, snap bases)

	browse   string            // source shown on the Browse tab
	catalogs map[string]*Index // loaded catalogs, by source
	loading  map[string]bool   // catalog or search in flight
	loadErr  map[string]error
	view     string // "sections" | "packages"
	query    string
	results  []*Pkg
	pendingQ *string // search requested before the catalog finished loading

	scan     []Check
	scanning bool

	repos       map[string][]Repo
	repoErr     map[string]error
	reposLoaded bool

	cursor map[string]int // per-view cursor memory, keyed by context
	offset map[string]int

	input     textinput.Model
	inputKind string // "" | "search" | "add" | "password" | "color" | "repo" | "confirm" | "form"
	editSlot  int    // slot being edited by the "color" prompt
	repoSrc   string // source a "repo" prompt adds to
	onConfirm func(m *model) tea.Cmd
	form      *CustomSource
	formStep  int

	details      []string
	detailsTitle string
	detailsSrc   string

	pending *job // waiting on a password
	job     *job // running
	output  []string
	status  []seg
}

func newModel(query string) model {
	cfg, cfgErr := loadConfig()
	applyTheme(cfg.resolve())

	m := model{
		cfg:      cfg,
		sources:  allSources(cfg),
		tab:      tabInstalled,
		inst:     map[string][]Inst{},
		instErr:  map[string]error{},
		instSet:  map[string]bool{},
		browse:   "apt",
		catalogs: map[string]*Index{},
		loading:  map[string]bool{"apt": true},
		loadErr:  map[string]error{},
		view:     "sections",
		repos:    map[string][]Repo{},
		repoErr:  map[string]error{},
		cursor:   map[string]int{},
		offset:   map[string]int{},
		input:    textinput.New(),
	}
	m.styleInput()
	if cfgErr != nil {
		m.setStatus(sErr, "%v — using the default theme", cfgErr)
	}
	if query != "" {
		m.tab = tabBrowse
		m.pendingQ = &query
	}
	return m
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{cmdCatalog(m.src("apt"))}
	for _, s := range m.active() {
		cmds = append(cmds, cmdInstalled(s))
	}
	return tea.Batch(cmds...)
}

// ---------------------------------------------------------------------------
// Background commands
// ---------------------------------------------------------------------------

func cmdCatalog(s *Source) tea.Cmd {
	return func() tea.Msg {
		pkgs, err := s.catalog(s)
		return catalogMsg{s.ID, newIndex(pkgs, s.Sections), err}
	}
}

func cmdSearch(s *Source, q string) tea.Cmd {
	return func() tea.Msg {
		pkgs, err := s.search(s, q)
		return searchMsg{s.ID, q, pkgs, err}
	}
}

func cmdInstalled(s *Source) tea.Cmd {
	if s.installed == nil {
		return nil
	}
	return func() tea.Msg {
		list, err := s.installed(s)
		return instMsg{s.ID, list, err}
	}
}

func cmdRepos(s *Source) tea.Cmd {
	return func() tea.Msg {
		r, err := s.repos(s)
		return reposMsg{s.ID, r, err}
	}
}

func cmdDetails(s *Source, p *Pkg) tea.Cmd {
	return func() tea.Msg {
		lines := []string{"no details available"}
		if s.info != nil {
			lines = s.info(s, p)
		}
		return detailsMsg{p.Name, s.ID, lines}
	}
}

func cmdValidate(pw string) tea.Cmd {
	return func() tea.Msg { return validatedMsg{sudoValidate(pw)} }
}

// cmdScan health-checks every active source: apt's full scan, plus pending
// upgrades for the others.
func (m model) cmdScan() tea.Cmd {
	srcs := m.active()
	inst := map[string][]Inst{}
	for k, v := range m.inst {
		inst[k] = v
	}
	return func() tea.Msg { return scanMsg(scanAll(srcs, inst)) }
}

func scanAll(srcs []*Source, inst map[string][]Inst) []Check {
	var checks []Check
	for _, s := range srcs {
		if s.ID == "apt" {
			checks = append(checks, runScan(inst["apt"])...)
			continue
		}
		byName := map[string]*Check{}
		for _, in := range inst[s.ID] {
			if in.Manual {
				byName[in.Name] = &Check{Source: s.ID, Name: in.Name, Status: "ok"}
			}
		}
		if s.outdated != nil {
			up, _ := s.outdated(s)
			for name, ver := range up {
				c := byName[name]
				if c == nil {
					c = &Check{Source: s.ID, Name: name}
					byName[name] = c
				}
				c.Status = "warn"
				c.Notes = append(c.Notes, "upgradable → "+ver)
			}
		}
		for _, c := range byName {
			checks = append(checks, *c)
		}
	}
	order := map[string]int{"err": 0, "warn": 1, "ok": 2}
	srcOrder := map[string]int{}
	for i, s := range srcs {
		srcOrder[s.ID] = i
	}
	sort.SliceStable(checks, func(i, j int) bool {
		a, b := checks[i], checks[j]
		if order[a.Status] != order[b.Status] {
			return order[a.Status] < order[b.Status]
		}
		if a.Source != b.Source {
			return srcOrder[a.Source] < srcOrder[b.Source]
		}
		return a.Name < b.Name
	})
	return checks
}

func waitLine(j *job) tea.Cmd {
	return func() tea.Msg {
		if l, ok := <-j.lines; ok {
			return lineMsg(l)
		}
		return linesDoneMsg{}
	}
}

func waitDone(j *job) tea.Cmd {
	return func() tea.Msg { return runDoneMsg{<-j.done} }
}

// ---------------------------------------------------------------------------
// Sources and state helpers
// ---------------------------------------------------------------------------

func (m model) src(id string) *Source {
	for _, s := range m.sources {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func (m model) disabled(id string) bool {
	for _, d := range m.cfg.Disabled {
		if d == id {
			return true
		}
	}
	return false
}

// active is every source that's installed and not disabled; apt is always on.
func (m model) active() []*Source {
	var res []*Source
	for _, s := range m.sources {
		if s.ID == "apt" || (!m.disabled(s.ID) && s.Available()) {
			res = append(res, s)
		}
	}
	return res
}

func (m model) browseSrc() *Source {
	if s := m.src(m.browse); s != nil {
		return s
	}
	return m.sources[0]
}

func (m model) isInstalled(src, name string) bool { return m.instSet[src+"/"+name] }

func (m model) ctx() string {
	switch m.tab {
	case tabInstalled:
		return fmt.Sprintf("installed:%s:%v", m.instFilter, m.showAll)
	case tabScan:
		return "scan"
	case tabSources:
		return "sources"
	case tabTheme:
		return "theme"
	}
	return "browse:" + m.browse + ":" + m.view + ":" + m.query
}

func (m model) cur() int { return m.cursor[m.ctx()] }

func (m *model) setStatus(style lipgloss.Style, format string, a ...any) {
	m.status = []seg{{fmt.Sprintf(format, a...), style}}
}

// setBrowse shows a source on the Browse tab, loading its catalog if needed.
func (m *model) setBrowse(id string) tea.Cmd {
	s := m.src(id)
	m.browse, m.query, m.results = id, "", nil
	m.view = "packages"
	if s.Sections {
		m.view = "sections"
	}
	if !s.Catalog {
		return nil
	}
	if idx := m.catalogs[id]; idx != nil {
		m.results = idx.all
		return nil
	}
	if !m.loading[id] {
		m.loading[id] = true
		return cmdCatalog(s)
	}
	return nil
}

// cycle returns the ID dir steps away from cur in ids, wrapping around.
func cycle(ids []string, cur string, dir int) string {
	for i, id := range ids {
		if id == cur {
			return ids[(i+dir+len(ids))%len(ids)]
		}
	}
	return ids[0]
}

func (m model) activeIDs() []string {
	var ids []string
	for _, s := range m.active() {
		ids = append(ids, s.ID)
	}
	return ids
}

func (m *model) search(q string) tea.Cmd {
	s := m.browseSrc()
	m.query, m.view = q, "packages"
	if s.Catalog {
		if idx := m.catalogs[s.ID]; idx != nil {
			m.results = idx.Search(q)
		} else {
			m.pendingQ = &q
		}
		return nil
	}
	if s.search == nil {
		m.setStatus(sWarn, "%s has no search command", s.Name)
		return nil
	}
	m.results = nil
	if strings.TrimSpace(q) == "" {
		return nil
	}
	m.loading[s.ID] = true
	return cmdSearch(s, q)
}

func (m *model) openSection(name string) {
	m.query = "section:" + name
	m.view = "packages"
	m.results = m.catalogs[m.browse].InSection(name)
}

func (m *model) switchTab(t tab) tea.Cmd {
	m.tab = t
	switch {
	case t == tabScan && m.scan == nil && !m.scanning:
		m.scanning = true
		return m.cmdScan()
	case t == tabSources && !m.reposLoaded:
		return m.loadRepos()
	}
	return nil
}

func (m *model) loadRepos() tea.Cmd {
	m.reposLoaded = true
	var cmds []tea.Cmd
	for _, s := range m.active() {
		if s.repos != nil {
			cmds = append(cmds, cmdRepos(s))
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) prompt(kind, label string) tea.Cmd {
	m.inputKind = kind
	m.input.Prompt = label
	m.input.Reset()
	m.input.EchoMode = textinput.EchoNormal
	if kind == "password" {
		m.input.EchoMode = textinput.EchoPassword
		m.input.EchoCharacter = '•'
	}
	return m.input.Focus()
}

// confirm asks a yes/no question and runs fn on yes.
func (m *model) confirm(question string, fn func(m *model) tea.Cmd) tea.Cmd {
	m.onConfirm = fn
	return m.prompt("confirm", question+" (y/N): ")
}

// runJob starts j, asking for the sudo password first when the chain needs
// root and sudo hasn't cached your credentials.
func (m *model) runJob(j *job) tea.Cmd {
	if j == nil {
		return nil
	}
	if m.job != nil {
		m.setStatus(sWarn, "wait for %s to finish", m.job.label)
		return nil
	}
	if !j.needsPassword() || sudoCached() {
		return m.start(j, "")
	}
	m.pending = j
	m.setStatus(sText, "%s needs root", j.label)
	return m.prompt("password", "sudo password: ")
}

// start launches j in the background. The password goes straight to sudo
// and is not kept on the model.
func (m *model) start(j *job, password string) tea.Cmd {
	j.lines, j.done = make(chan string, 64), make(chan error, 1)
	m.job, m.pending = j, nil
	prefix := "$ "
	if j.root {
		prefix = "$ sudo "
	}
	if m.output == nil {
		m.output = []string{}
	}
	m.output = append(m.output, prefix+strings.Join(j.args, " "))
	m.setStatus(sWarn, "%s …", j.label)
	go runStream(j, password, j.lines, j.done)
	return waitLine(j)
}

// afterJob refreshes whatever a finished job changed.
func (m *model) afterJob(j *job) []tea.Cmd {
	var cmds []tea.Cmd
	if j.reloadSource != "" {
		if s := m.src(j.reloadSource); s != nil {
			cmds = append(cmds, cmdInstalled(s))
		}
	}
	if j.reloadRepos != "" {
		if s := m.src(j.reloadRepos); s != nil {
			if s.repos != nil {
				cmds = append(cmds, cmdRepos(s))
			}
			if s.Catalog {
				m.loading[s.ID] = true
				cmds = append(cmds, cmdCatalog(s))
			}
		}
	}
	if j.redetect {
		m.sources = allSources(m.cfg) // pick up the newly installed manager
		for _, s := range m.active() {
			cmds = append(cmds, cmdInstalled(s))
		}
		m.reposLoaded = false
		if m.tab == tabSources {
			cmds = append(cmds, m.loadRepos())
		}
	}
	m.scan, m.scanning = nil, false // rescan once the installed lists refresh
	return cmds
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

// Update handles a message, then applies the theme to draw with: the one
// under the cursor while browsing the Theme tab, otherwise the saved one.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	if nm, ok := next.(model); ok {
		applyTheme(nm.previewTheme())
		nm.styleInput()
		next = nm
	}
	return next, cmd
}

func (m model) previewTheme() Theme {
	if m.tab == tabTheme && m.inputKind == "" {
		items := m.items()
		if c := m.cur(); c < len(items) {
			if items[c].theme == customName || items[c].isSlot {
				return m.customTheme()
			}
			if t, ok := preset(items[c].theme); ok {
				return t
			}
		}
	}
	return m.cfg.resolve()
}

func (m *model) styleInput() {
	m.input.Cursor.Style = sWinTtl
	m.input.TextStyle = sText
	m.input.PromptStyle = sPrompt
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case catalogMsg:
		m.loading[msg.src], m.loadErr[msg.src] = false, msg.err
		if msg.err == nil {
			m.catalogs[msg.src] = msg.idx
		}
		if msg.src == m.browse && msg.err == nil {
			switch {
			case m.pendingQ != nil:
				q := *m.pendingQ
				m.pendingQ = nil
				return m, m.search(q)
			case m.query == "":
				m.results = msg.idx.all
			case strings.HasPrefix(m.query, "section:"):
				m.openSection(strings.TrimPrefix(m.query, "section:"))
			default:
				m.results = msg.idx.Search(m.query)
			}
		}

	case searchMsg:
		if msg.src == m.browse && msg.query == m.query {
			m.loading[msg.src], m.loadErr[msg.src] = false, msg.err
			m.results = msg.pkgs
		}

	case instMsg:
		m.instErr[msg.src] = msg.err
		if msg.err == nil || msg.list != nil {
			m.inst[msg.src] = msg.list
		}
		m.instSet = map[string]bool{}
		for src, list := range m.inst {
			for _, in := range list {
				m.instSet[src+"/"+in.Name] = true
			}
		}
		if m.tab == tabScan && m.scan == nil && !m.scanning {
			m.scanning = true
			return m, m.cmdScan()
		}

	case reposMsg:
		m.repos[msg.src], m.repoErr[msg.src] = msg.repos, msg.err

	case scanMsg:
		m.scan, m.scanning = msg, false

	case detailsMsg:
		m.detailsTitle, m.detailsSrc, m.details = msg.title, msg.src, msg.lines

	case validatedMsg:
		pw := m.input.Value()
		m.input.Reset()
		if msg.err != nil {
			m.setStatus(sErr, "%s — try again, <Esc> to cancel", msg.err)
			return m, nil
		}
		m.inputKind = ""
		m.input.Blur()
		return m, m.start(m.pending, pw)

	case lineMsg:
		m.output = append(m.output, string(msg))
		return m, waitLine(m.job)

	case linesDoneMsg:
		return m, waitDone(m.job)

	case runDoneMsg:
		j := m.job
		m.job = nil
		cmds := m.afterJob(j)
		switch {
		case msg.err != nil:
			m.setStatus(sErr, "%s %s failed: %v   (c: clear log)", icons.err, j.label, msg.err)
		case j.next != nil:
			// sudo cached the password on the first root step, so later
			// steps in the chain run without asking again
			cmds = append(cmds, m.start(j.next, ""))
		case j.redetect:
			m.setStatus(sOk, "✓ %s done — it's now on the Sources and Browse tabs   (c: clear log)", j.label)
		default:
			m.setStatus(sOk, "✓ %s done   (c: clear log)", j.label)
		}
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		// Fast typing or key repeat can deliver several runes in one KeyMsg
		// ("jj"); outside a prompt, treat each as its own keypress.
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste && m.inputKind == "" {
			var cmds []tea.Cmd
			var mm tea.Model = m
			for _, r := range msg.Runes {
				var cmd tea.Cmd
				mm, cmd = mm.(model).handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				cmds = append(cmds, cmd)
			}
			return mm, tea.Batch(cmds...)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

// pkgOf returns the package an item acts on, and its source.
func (m model) pkgOf(it *item) (*Source, *Pkg) {
	switch {
	case it == nil:
		return nil, nil
	case it.pkg != nil:
		return m.src(it.pkg.Source), it.pkg
	case it.inst != nil:
		return m.src(it.inst.Source), &Pkg{Source: it.inst.Source, Name: it.inst.Name, Version: it.inst.Version,
			Desc: it.inst.Summary, Section: it.inst.Origin}
	case it.check != nil:
		return m.src(it.check.Source), &Pkg{Source: it.check.Source, Name: it.check.Name}
	}
	return nil, nil
}

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	if m.details != nil {
		if key == "o" {
			m.openHomepage(m.detailsSrc, m.detailsTitle)
		}
		m.details = nil
		return m, nil
	}

	if m.inputKind != "" {
		return m.handleInput(k)
	}

	items := m.items()
	n := len(items)
	var sel *item
	if c := m.cur(); c < n {
		sel = &items[c]
	}
	move := func(to int) {
		m.cursor[m.ctx()] = max(0, min(to, n-1))
	}
	src, pkg := m.pkgOf(sel)

	// Sources-tab keys first; anything it doesn't handle falls through
	if m.tab == tabSources {
		if cmd, ok := m.sourcesKey(key, sel); ok {
			return m, cmd
		}
	}

	switch key {
	case "q", "esc":
		return m, tea.Quit
	case "1":
		return m, m.switchTab(tabInstalled)
	case "2":
		return m, m.switchTab(tabScan)
	case "3":
		return m, m.switchTab(tabBrowse)
	case "4":
		return m, m.switchTab(tabSources)
	case "5":
		return m, m.switchTab(tabTheme)
	case "y":
		if m.tab == tabTheme && sel != nil && sel.theme != "" {
			return m, m.copyToCustom(sel.theme)
		}
	case "j", "down":
		move(m.cur() + 1)
	case "k", "up":
		move(m.cur() - 1)
	case "ctrl+d":
		move(m.cur() + m.bodyHeight()/2)
	case "ctrl+u":
		move(m.cur() - m.bodyHeight()/2)
	case "g", "home":
		move(0)
	case "G", "end":
		move(n - 1)
	case "c":
		m.output, m.status = nil, nil
	case "[", "]":
		dir := 1
		if key == "[" {
			dir = -1
		}
		switch m.tab {
		case tabBrowse:
			return m, m.setBrowse(cycle(m.activeIDs(), m.browse, dir))
		case tabInstalled:
			m.instFilter = cycle(append([]string{""}, m.activeIDs()...), m.instFilter, dir)
		}
	case "/":
		m.tab = tabBrowse
		return m, m.prompt("search", "Search "+m.browseSrc().Name+": ")
	case "t", "-":
		if m.browseSrc().Sections {
			m.tab, m.view = tabBrowse, "sections"
		}
	case "backspace":
		if m.tab == tabBrowse && m.browseSrc().Sections {
			if m.view == "packages" {
				m.view = "sections"
			} else if m.results != nil {
				m.view = "packages"
			}
		}
	case "m":
		if m.tab == tabInstalled {
			m.showAll = !m.showAll
		}
	case "a":
		if m.tab == tabInstalled {
			return m, m.prompt("add", "Install from "+m.browseSrc().Name+" (exact name): ")
		}
	case "r":
		switch m.tab {
		case tabInstalled:
			var cmds []tea.Cmd
			for _, s := range m.active() {
				cmds = append(cmds, cmdInstalled(s))
			}
			return m, tea.Batch(cmds...)
		case tabScan:
			m.scanning = true
			return m, m.cmdScan()
		case tabBrowse:
			s := m.browseSrc()
			if s.Catalog {
				m.loading[s.ID] = true
				return m, cmdCatalog(s)
			}
			return m, m.search(m.query)
		}
	case "s":
		if m.tab == tabScan {
			m.scanning = true
			return m, m.cmdScan()
		}
	case "R":
		if m.tab == tabScan {
			return m, m.runJob(&job{label: "apt update", args: []string{"apt-get", "update"}, root: true, reloadRepos: "apt"})
		}
	case "U":
		if m.tab == tabScan {
			return m, m.runJob(m.upgradeAllJob())
		}
	case "A":
		if m.tab == tabScan {
			return m, m.runJob(&job{label: "autoremove", args: []string{"apt-get", "autoremove", "-y", "-o", "Dpkg::Progress-Fancy=0"},
				root: true, reloadSource: "apt"})
		}
	case "enter":
		if sel == nil {
			return m, nil
		}
		if m.tab == tabTheme {
			return m, m.themeEnter(*sel)
		}
		if sel.section != "" {
			m.openSection(sel.section)
			return m, nil
		}
		if src != nil {
			return m, cmdDetails(src, pkg)
		}
	case "o":
		if pkg != nil {
			m.openHomepage(pkg.Source, pkg.Name)
		}
	case "i":
		if src == nil || m.tab == tabScan {
			return m, nil
		}
		if m.isInstalled(src.ID, pkg.Name) {
			m.setStatus(sWarn, "lindeck: %s is already installed from %s", pkg.Name, src.Name)
			return m, nil
		}
		return m, m.runJob(src.pkgJob("install", src.install(src, pkg), pkg))
	case "u":
		if m.tab == tabScan && src != nil {
			if src.upgrade == nil {
				m.setStatus(sWarn, "%s can't upgrade single packages", src.Name)
				return m, nil
			}
			return m, m.runJob(src.pkgJob("upgrade", src.upgrade(src, pkg), pkg))
		}
	case "d":
		if src == nil || m.tab == tabScan {
			return m, nil
		}
		if !m.isInstalled(src.ID, pkg.Name) {
			m.setStatus(sWarn, "lindeck: %s is not installed from %s", pkg.Name, src.Name)
			return m, nil
		}
		if src.remove == nil {
			m.setStatus(sWarn, "%s has no remove command", src.Name)
			return m, nil
		}
		return m, m.runJob(src.pkgJob("remove", src.remove(src, pkg), pkg))
	}
	return m, nil
}

// upgradeAllJob upgrades every active source, root steps first so the one
// password prompt covers all of them.
func (m model) upgradeAllJob() *job {
	var roots, users []*job
	for _, s := range m.active() {
		if s.upgradeAll == nil {
			continue
		}
		j := &job{label: "upgrade " + s.Name, args: s.upgradeAll(s), root: s.Root, reloadSource: s.ID}
		if s.Root {
			roots = append(roots, j)
		} else {
			users = append(users, j)
		}
	}
	return chain(append(roots, users...)...)
}

func (m model) handleInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		if m.inputKind == "password" {
			m.pending = nil
			m.setStatus(sComment, "cancelled")
		}
		if m.inputKind == "form" {
			m.form = nil
			m.setStatus(sComment, "cancelled — no source added")
		}
		m.inputKind = ""
		m.input.Reset()
		m.input.Blur()
		return m, nil
	case "enter":
		kind, val := m.inputKind, strings.TrimSpace(m.input.Value())
		switch kind {
		case "password":
			m.setStatus(sComment, "checking password …")
			return m, cmdValidate(m.input.Value())
		case "form":
			return m, m.formNext(val)
		}
		m.inputKind = ""
		m.input.Blur()
		switch kind {
		case "search":
			return m, m.search(val)
		case "add":
			if val != "" {
				s := m.browseSrc()
				p := &Pkg{Source: s.ID, Name: val}
				return m, m.runJob(s.pkgJob("install", s.install(s, p), p))
			}
		case "color":
			m.setColor(m.editSlot, val)
		case "repo":
			return m, m.addRepo(m.repoSrc, val)
		case "confirm":
			fn := m.onConfirm
			m.onConfirm = nil
			if v := strings.ToLower(val); (v == "y" || v == "yes") && fn != nil {
				return m, fn(&m)
			}
			m.setStatus(sComment, "cancelled")
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *model) openHomepage(srcID, name string) {
	var p *Pkg
	if idx := m.catalogs[srcID]; idx != nil {
		p = idx.byName[name]
	}
	if p == nil {
		for _, r := range m.results {
			if r.Source == srcID && r.Name == name {
				p = r
			}
		}
	}
	if p == nil || p.Homepage == "" {
		m.setStatus(sWarn, "lindeck: no homepage listed for %s", name)
		return
	}
	if err := openURL(p.Homepage); err != nil {
		m.setStatus(sErr, "could not open %s: %v", p.Homepage, err)
		return
	}
	m.setStatus(sComment, "opened %s", p.Homepage)
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func (m model) tabLine() []seg {
	tabs := []string{"1:Installed", "2:Scan", "3:Browse", "4:Sources", "5:Theme"}
	var parts []string
	for i, t := range tabs {
		if tab(i) == m.tab {
			parts = append(parts, "["+t+"]")
		} else {
			parts = append(parts, " "+t+" ")
		}
	}
	return []seg{{"  " + strings.Join(parts, " "), sTitle}}
}

func (m model) footer() string {
	switch m.tab {
	case tabInstalled:
		return "[ ]:filter source  a:add  d:remove  m:dependencies  r:refresh  <CR>:details  o:homepage  1-5:tabs  q:quit"
	case tabScan:
		return "s:rescan  u:upgrade  U:upgrade all  A:autoremove  R:apt update  <CR>:details  q:quit"
	case tabSources:
		return "i:install manager  a:add repo  d:remove  e:enable/disable  n:new source  <CR>:browse  r:refresh  q:quit"
	case tabTheme:
		return "<CR>:apply theme / edit color  y:copy theme into custom  1-5:tabs  q:quit"
	}
	if m.view == "sections" {
		return "[ ]:switch source  <CR>:browse section  /:search  <BS>:back to packages  r:refresh  q:quit"
	}
	if m.browseSrc().Sections {
		return "[ ]:switch source  t:sections  /:search  <CR>:details  i:install  d:remove  o:homepage  <BS>:sections  q:quit"
	}
	return "[ ]:switch source  /:search  <CR>:details  i:install  d:remove  o:homepage  r:refresh  1-5:tabs  q:quit"
}

// sourceBar shows the active sources with the current one highlighted.
func (m model) sourceBar(label string, ids []string, cur string) []seg {
	segs := []seg{{"  " + label + " ", sComment}}
	for _, id := range ids {
		name := id
		if name == "" {
			name = "all"
		}
		if id == cur {
			segs = append(segs, seg{"[" + name + "]", sTitle}, seg{" ", sText})
		} else {
			segs = append(segs, seg{" " + name + " ", sText}, seg{" ", sText})
		}
	}
	return append(segs, seg{"  [ ] switch", sComment})
}

// header returns the non-selectable rows above the items for the current view.
func (m model) header() [][]seg {
	line := func(s string, st lipgloss.Style) []seg { return []seg{{s, st}} }
	switch m.tab {
	case tabInstalled:
		rows := [][]seg{m.sourceBar("source:", append([]string{""}, m.activeIDs()...), m.instFilter)}
		if len(m.inst) == 0 {
			rows = append(rows, line("  reading installed packages …", sComment))
		}
		for _, s := range m.active() {
			if err := m.instErr[s.ID]; err != nil && (m.instFilter == "" || m.instFilter == s.ID) {
				rows = append(rows, line(fmt.Sprintf("  %s: %v", s.Name, err), sErr))
			}
		}
		return append(rows, []seg{})
	case tabSources:
		return m.sourcesHeader()
	case tabTheme:
		path, _ := configPath()
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+"/") {
			path = "~" + path[len(home):]
		}
		return [][]seg{
			{{"  themes — current: ", sText}, {m.cfg.Theme, sTitle}, {"   (move to preview, <CR> to apply)", sComment}},
			{{"  saved to " + path, sComment}},
			{},
		}
	case tabScan:
		if m.scan == nil {
			return [][]seg{line("  scanning packages …", sComment)}
		}
		c := map[string]int{}
		for _, r := range m.scan {
			c[r.Status]++
		}
		return [][]seg{{
			{"  ", sText}, {fmt.Sprintf("%d ok", c["ok"]), sOk},
			{"   ", sText}, {fmt.Sprintf("%d warnings", c["warn"]), sWarn},
			{"   ", sText}, {fmt.Sprintf("%d errors", c["err"]), sErr},
			{"   across " + strings.Join(m.activeIDs(), ", "), sComment},
		}, {}}
	case tabBrowse:
		s := m.browseSrc()
		rows := [][]seg{m.sourceBar("source:", m.activeIDs(), m.browse)}
		switch {
		case m.loading[s.ID] && s.Catalog:
			return append(rows, line("  loading "+s.Name+" catalog …", sComment))
		case m.loading[s.ID]:
			return append(rows, line("  searching "+s.Name+" for '"+m.query+"' …", sComment))
		case m.loadErr[s.ID] != nil:
			return append(rows, line("  error: "+m.loadErr[s.ID].Error(), sErr), line("  press r to retry", sComment))
		case m.view == "sections" && m.catalogs[s.ID] != nil:
			return append(rows, line(fmt.Sprintf("  %s sections — %d  (press <CR> to browse one)", s.Name, len(m.catalogs[s.ID].sections)), sText), nil)
		case !s.Catalog && m.query == "":
			return append(rows, line("  press / to search "+s.Name, sText), line("  "+s.SearchHint, sComment))
		}
		title := "results for '" + m.query + "'"
		if sec, ok := strings.CutPrefix(m.query, "section:"); ok {
			title = "section '" + sec + "'"
		} else if m.query == "" {
			title = "all " + s.Name + " packages"
		}
		return append(rows, []seg{
			{fmt.Sprintf("  %s — %d packages   ", title, len(m.results)), sText},
			{"✓", sOk}, {" = already installed", sText},
		}, nil)
	}
	return nil
}

func (m model) items() []item {
	var items []item
	switch m.tab {
	case tabInstalled:
		for _, s := range m.active() {
			if m.instFilter != "" && m.instFilter != s.ID {
				continue
			}
			list := m.inst[s.ID]
			for i := range list {
				in := &list[i]
				if m.showAll || in.Manual || in.State != "" {
					items = append(items, item{name: in.Name, inst: in})
				}
			}
		}
	case tabScan:
		for i := range m.scan {
			items = append(items, item{name: m.scan[i].Name, check: &m.scan[i]})
		}
	case tabSources:
		items = m.sourceItems()
	case tabTheme:
		for _, p := range presets {
			items = append(items, item{theme: p.Name})
		}
		items = append(items, item{theme: customName})
		for i := range slots {
			items = append(items, item{isSlot: true, slot: i})
		}
	case tabBrowse:
		idx := m.catalogs[m.browse]
		if m.loading[m.browse] && m.browseSrc().Catalog {
			break
		}
		if m.view == "sections" && idx != nil {
			for _, s := range idx.sections {
				items = append(items, item{section: s.Name})
			}
			break
		}
		for _, p := range m.results {
			items = append(items, item{name: p.Name, pkg: p})
		}
	}
	return items
}

// rowCount is how many rows rows(it) will return, without building them.
func rowCount(it item) int {
	switch {
	case it.theme == customName:
		return 3 // blank line, the custom row, its explanation
	case it.addNew:
		return 2
	case it.src != nil && it.repo == nil:
		return 2 // blank line, then the source
	case it.check != nil:
		return max(len(it.check.Notes), 1)
	case it.pkg != nil && it.pkg.Desc != "":
		return 2
	}
	return 1
}

func (m model) rows(it item) [][]seg {
	switch {
	case it.theme != "":
		return m.themeRows(it.theme)
	case it.isSlot:
		return m.slotRows(it.slot)
	case it.src != nil || it.addNew:
		return m.sourceRows(it)
	case it.inst != nil:
		in := it.inst
		icon, nameSt := icons.auto, sText
		if in.Manual {
			icon = icons.manual
		}
		nw, vw, showSummary := m.columns(14, 38, 24) // icon, source column and spacing
		summary := ""
		if showSummary {
			summary = in.Summary
		}
		ist, srcLabel := sDir, in.Source
		if in.State != "" {
			icon, ist, nameSt, srcLabel = icons.err, sErr, sErr, "ERROR"
		}
		return [][]seg{{
			{"  ", sText}, {icon, ist},
			{fmt.Sprintf(" %-*s ", nw, trunc(in.Name, nw)), nameSt},
			{fmt.Sprintf("%-8s", trunc(srcLabel, 7)), sDir},
			{fmt.Sprintf("%-*s ", vw, trunc(in.Version, vw)), sComment},
			{summary, sComment},
		}}

	case it.check != nil:
		r := it.check
		icon, st := icons.ok, sOk
		switch r.Status {
		case "warn":
			icon, st = icons.warn, sWarn
		case "err":
			icon, st = icons.err, sErr
		}
		note := ""
		if len(r.Notes) > 0 {
			note = r.Notes[0]
		}
		nw := max(14, min(38, (m.innerWidth()-14)*45/100))
		rows := [][]seg{{{"  ", sText}, {icon, st}, {fmt.Sprintf(" %-*s ", nw, trunc(r.Name, nw)), sText},
			{fmt.Sprintf("%-8s", trunc(r.Source, 7)), sDir}, {note, sText}}}
		for _, n := range r.Notes[min(1, len(r.Notes)):] {
			rows = append(rows, []seg{{"      · " + n, sComment}})
		}
		return rows

	case it.section != "":
		count := 0
		for _, s := range m.catalogs[m.browse].sections {
			if s.Name == it.section {
				count = s.Count
			}
		}
		return [][]seg{{
			{fmt.Sprintf("   ▸ %-32s", it.section), sDir},
			{fmt.Sprintf("  (%d packages)", count), sComment},
		}}

	case it.pkg != nil:
		p := it.pkg
		nw, vw, showSection := m.columns(5, 45, 24)
		section := ""
		if showSection {
			section = p.Section
		}
		mark, mst := " ", sText
		if m.isInstalled(p.Source, p.Name) {
			mark, mst = "✓", sOk
		}
		rows := [][]seg{
			{{"  ", sText}, {mark, mst}, {fmt.Sprintf(" %-*s ", nw, trunc(p.Name, nw)), sText},
				{fmt.Sprintf("%-*s ", vw, trunc(p.Version, vw)), sComment}, {section, sDir}},
		}
		if p.Desc != "" {
			rows = append(rows, []seg{{"        " + p.Desc, sComment}})
		}
		return rows
	}
	return nil
}

func (m model) winSize() (w, h int) {
	w, h = int(float64(m.width)*0.85), int(float64(m.height)*0.8)
	if m.width < 120 { // narrow terminal: use nearly all of it
		w = m.width - 2
	}
	return max(w, min(m.width, 60)), max(h, min(m.height, 16))
}

// innerWidth is the usable width inside the window border.
func (m model) innerWidth() int {
	w, _ := m.winSize()
	return w - 2
}

// hintLines wraps the key hints to the window width, breaking only between
// hints so none is cut in half. Keys are bold; both share the tab-line color
// so the hints stay readable over transparent or image backgrounds.
func (m model) hintLines() [][]seg {
	width := m.innerWidth() - 2
	var lines [][]seg
	var cur []seg
	used := 0
	for _, h := range strings.Split(m.footer(), "  ") {
		key, label, _ := strings.Cut(h, ":")
		n := len([]rune(h))
		if used > 0 && used+2+n > width {
			lines, cur, used = append(lines, cur), nil, 0
		}
		if used > 0 {
			used += 2
		}
		cur = append(cur, seg{"  ", sHint}, seg{key, sHintKey}, seg{":" + label, sHint})
		used += n
	}
	return append(lines, cur)
}

// columns splits the space left after fixed parts between a name and a
// version column, dropping the trailing description when it would be too
// narrow to read. fixed is the width of everything except those columns.
func (m model) columns(fixed, maxName, maxVer int) (name, ver int, showRest bool) {
	avail := m.innerWidth() - fixed
	name, ver = maxName, maxVer
	if rest := avail - name - ver - 2; rest >= 16 {
		return name, ver, true
	}
	// shrink to fit, keeping names readable first
	name = max(14, min(maxName, avail*55/100))
	ver = max(8, min(maxVer, avail-name-2))
	return name, ver, avail-name-ver-2 >= 16
}

const outputRows = 8

// bodyHeight is how many rows the scrolling list gets inside the window.
func (m model) bodyHeight() int {
	_, h := m.winSize()
	used := 2 + 2 + len(m.hintLines()) + 1 // border, tab line + rule, hints, status line
	if m.inputKind != "" {
		used++
	}
	if m.output != nil {
		used += outputRows + 1
	}
	return max(h-used, 1)
}

// renderRow lays segments out in exactly w cells, truncating or padding.
func renderRow(segs []seg, w int, selected bool) string {
	var b strings.Builder
	left := w
	last := sText
	for _, s := range segs {
		if left <= 0 {
			break
		}
		st := s.s
		if selected {
			st = st.Background(selBg)
		}
		last = st
		t := s.t
		if runewidth.StringWidth(t) > left {
			t = runewidth.Truncate(t, left, "…")
		}
		b.WriteString(st.Render(t))
		left -= runewidth.StringWidth(t)
	}
	if left > 0 {
		pad := lipgloss.NewStyle()
		if selected {
			pad = last.UnsetForeground()
		}
		b.WriteString(pad.Render(strings.Repeat(" ", left)))
	}
	return b.String()
}

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	w, _ := m.winSize()
	iw := w - 2
	var rows []string
	add := func(segs []seg, sel bool) { rows = append(rows, renderRow(segs, iw, sel)) }

	add(m.tabLine(), false)
	for _, l := range m.hintLines() {
		add(l, false)
	}
	add([]seg{{strings.Repeat("─", iw), sBorder}}, false)

	bh := m.bodyHeight()
	if m.details != nil {
		body := [][]seg{{{"  " + m.detailsTitle, sTitle}, {"  (" + m.detailsSrc + ")", sComment}}, {}}
		for _, l := range m.details {
			body = append(body, []seg{{"  " + l, sText}})
		}
		body = append(body, []seg{}, []seg{{"  o:open homepage   any key: close", sComment}})
		for i := 0; i < bh; i++ {
			if i < len(body) {
				add(body[i], false)
			} else {
				add(nil, false)
			}
		}
	} else {
		m.renderBody(add, bh)
	}

	if m.output != nil {
		add([]seg{{"─ output " + strings.Repeat("─", iw), sBorder}}, false)
		tail := m.output[max(len(m.output)-outputRows, 0):]
		for i := 0; i < outputRows; i++ {
			if i < len(tail) {
				add([]seg{{"  " + tail[i], sComment}}, false)
			} else {
				add(nil, false)
			}
		}
	}
	if m.inputKind != "" {
		rows = append(rows, " "+lipgloss.NewStyle().Width(iw-1).MaxWidth(iw-1).Render(m.input.View()))
	}
	add(append([]seg{{"  ", sText}}, m.status...), false)

	// border with a centered title, like nvim_open_win{ title_pos = "center" }
	title := " lindeck "
	fill := iw - len(title)
	top := sBorder.Render("╭"+strings.Repeat("─", fill/2)) + sWinTtl.Render(title) +
		sBorder.Render(strings.Repeat("─", fill-fill/2)+"╮")
	var b strings.Builder
	b.WriteString(top + "\n")
	side := sBorder.Render("│")
	for _, r := range rows {
		b.WriteString(side + r + side + "\n")
	}
	b.WriteString(sBorder.Render("╰" + strings.Repeat("─", iw) + "╯"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, b.String())
}

// renderBody draws the header and the visible slice of items, scrolling so
// the selected item stays on screen. Only visible items are rendered.
func (m model) renderBody(add func([]seg, bool), bh int) {
	header := m.header()
	items := m.items()
	cur := m.cur()

	// starts[i] is the first row of item i, counting header rows
	starts := make([]int, len(items)+1)
	starts[0] = len(header)
	for i, it := range items {
		starts[i+1] = starts[i] + rowCount(it)
	}
	total := starts[len(items)]
	selStart, selEnd := 0, 0
	if cur < len(items) {
		selStart, selEnd = starts[cur], starts[cur+1]
	}

	// minimal scrolling; offset is remembered per view (maps are shared state)
	ctx := m.ctx()
	off := m.offset[ctx]
	if selEnd > off+bh {
		off = selEnd - bh
	}
	if selStart < off {
		off = selStart
	}
	if cur == 0 {
		off = 0
	}
	off = max(0, min(off, max(total-bh, 0)))
	m.offset[ctx] = off

	var out [][]seg
	var sel []bool
	for r := off; r < len(header) && len(out) < bh; r++ {
		out, sel = append(out, header[r]), append(sel, false)
	}
	// first item intersecting the window
	i := 0
	for i < len(items) && starts[i+1] <= off {
		i++
	}
	for ; i < len(items) && len(out) < bh; i++ {
		for j, r := range m.rows(items[i]) {
			if starts[i]+j >= off && len(out) < bh {
				// the selection highlight skips spacer rows
				out, sel = append(out, r), append(sel, i == cur && len(r) > 0)
			}
		}
	}
	if len(items) == 0 && m.tab == tabInstalled && len(m.inst) > 0 && len(out) < bh {
		out, sel = append(out, []seg{{"  (no packages to show — press m to include dependencies)", sComment}}), append(sel, false)
	}
	for r := 0; r < bh; r++ {
		if r < len(out) {
			add(out[r], sel[r])
		} else {
			add(nil, false)
		}
	}
}

// trunc shortens s to at most n terminal cells, counting wide characters
// such as emoji as two.
func trunc(s string, n int) string {
	return runewidth.Truncate(s, n, "…")
}
