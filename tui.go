package main

// Floating-window UI with four tabs: Installed, Scan, apt (search/browse),
// and Theme.
// Layout and keys mirror plugdeck.nvim.

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	tabApt
	tabTheme
)

type (
	idxMsg struct {
		idx *Index
		err error
	}
	instMsg    []Inst
	scanMsg    []Check
	detailsMsg struct {
		title string
		lines []string
	}
	validatedMsg struct{ err error }
	lineMsg      string
	linesDoneMsg struct{}
	runDoneMsg   struct{ err error }
)

// job is a root command run in the background with its output streamed in.
type job struct {
	label  string
	args   []string
	reload bool // reload the apt index afterwards (apt update)
	lines  chan string
	done   chan error
}

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
}

type model struct {
	width, height int
	tab           tab

	inst    []Inst
	instSet map[string]bool
	showAll bool

	idx        *Index
	idxLoading bool
	idxErr     error
	aptView    string // "sections" | "packages"
	query      string
	results    []*Pkg
	pendingQ   *string // search requested before the index finished loading

	scan     []Check
	scanning bool

	cursor map[string]int // per-view cursor memory, keyed by context
	offset map[string]int

	input     textinput.Model
	inputKind string // "" | "search" | "add" | "password" | "color"
	editSlot  int    // slot being edited by the "color" prompt

	cfg Config // saved settings; the theme in use comes from here

	details      []string
	detailsTitle string

	pending *job // waiting on a password
	job     *job // running
	output  []string
	status  []seg
}

func newModel(query string) model {
	cfg, cfgErr := loadConfig()
	applyTheme(cfg.resolve())

	m := model{
		cfg:        cfg,
		tab:        tabInstalled,
		instSet:    map[string]bool{},
		idxLoading: true,
		aptView:    "sections",
		cursor:     map[string]int{},
		offset:     map[string]int{},
		input:      textinput.New(),
	}
	m.styleInput()
	if cfgErr != nil {
		m.setStatus(sErr, "%v — using the default theme", cfgErr)
	}
	if query != "" {
		m.tab = tabApt
		m.pendingQ = &query
	}
	return m
}

func (m model) Init() tea.Cmd {
	return tea.Batch(cmdIndex, cmdInstalled)
}

func cmdIndex() tea.Msg {
	idx, err := loadIndex()
	return idxMsg{idx, err}
}

func cmdInstalled() tea.Msg { return instMsg(loadInstalled()) }

func cmdScan(inst []Inst) tea.Cmd {
	return func() tea.Msg { return scanMsg(runScan(inst)) }
}

func cmdDetails(name string) tea.Cmd {
	return func() tea.Msg { return detailsMsg{name, pkgDetails(name)} }
}

func cmdValidate(pw string) tea.Cmd {
	return func() tea.Msg { return validatedMsg{sudoValidate(pw)} }
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

func aptJob(label string, args ...string) *job {
	full := append([]string{"apt-get"}, args[:1]...)
	full = append(full, "-y", "-o", "Dpkg::Progress-Fancy=0")
	return &job{label: label, args: append(full, args[1:]...)}
}

// ---------------------------------------------------------------------------
// State helpers
// ---------------------------------------------------------------------------

func (m model) ctx() string {
	switch m.tab {
	case tabInstalled:
		if m.showAll {
			return "installed:all"
		}
		return "installed"
	case tabScan:
		return "scan"
	case tabTheme:
		return "theme"
	}
	if m.aptView == "sections" {
		return "apt:sections"
	}
	return "apt:packages:" + m.query
}

func (m model) cur() int { return m.cursor[m.ctx()] }

func (m *model) setStatus(style lipgloss.Style, format string, a ...any) {
	m.status = []seg{{fmt.Sprintf(format, a...), style}}
}

func (m *model) search(q string) {
	m.query = q
	m.aptView = "packages"
	m.results = m.idx.Search(q)
}

func (m *model) openSection(name string) {
	m.query = "section:" + name
	m.aptView = "packages"
	m.results = m.idx.InSection(name)
}

func (m *model) switchTab(t tab) tea.Cmd {
	m.tab = t
	if t == tabScan && m.scan == nil && !m.scanning {
		m.scanning = true
		return cmdScan(m.inst)
	}
	return nil
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

// runRoot starts j, asking for the sudo password first unless it's cached.
func (m *model) runRoot(j *job) tea.Cmd {
	if m.job != nil {
		m.setStatus(sWarn, "wait for %s to finish", m.job.label)
		return nil
	}
	if sudoCached() {
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
	m.output = []string{"$ sudo " + strings.Join(j.args, " ")}
	m.setStatus(sWarn, "%s …", j.label)
	go sudoStream(password, j.args, j.lines, j.done)
	return waitLine(j)
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

	case idxMsg:
		m.idxLoading, m.idx, m.idxErr = false, msg.idx, msg.err
		if m.idx != nil && m.pendingQ != nil {
			m.search(*m.pendingQ)
			m.pendingQ = nil
		}

	case instMsg:
		m.inst = msg
		m.instSet = map[string]bool{}
		for _, in := range m.inst {
			m.instSet[in.Name] = true
		}
		if m.tab == tabScan && m.scan == nil && !m.scanning {
			m.scanning = true
			return m, cmdScan(m.inst)
		}

	case scanMsg:
		m.scan, m.scanning = msg, false

	case detailsMsg:
		m.detailsTitle, m.details = msg.title, msg.lines

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
		if msg.err != nil {
			m.setStatus(sErr, "%s %s failed: %v   (c: clear log)", icons.err, j.label, msg.err)
		} else {
			m.setStatus(sOk, "✓ %s done   (c: clear log)", j.label)
		}
		cmds := []tea.Cmd{cmdInstalled}
		if m.scan != nil || m.tab == tabScan {
			m.scan, m.scanning = nil, false // rescan once the installed list refreshes
		}
		if j.reload {
			m.idxLoading = true
			cmds = append(cmds, cmdIndex)
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

func (m model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	if m.details != nil {
		if key == "o" {
			m.openHomepage(m.detailsTitle)
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

	switch key {
	case "q", "esc":
		return m, tea.Quit
	case "1":
		return m, m.switchTab(tabInstalled)
	case "2":
		return m, m.switchTab(tabScan)
	case "3":
		m.tab = tabApt
	case "4":
		m.tab = tabTheme
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
	case "/":
		m.tab = tabApt
		return m, m.prompt("search", "Search apt: ")
	case "t", "-":
		m.tab, m.aptView = tabApt, "sections"
	case "backspace":
		if m.tab == tabApt {
			if m.aptView == "packages" {
				m.aptView = "sections"
			} else if m.results != nil {
				m.aptView = "packages"
			}
		}
	case "m":
		if m.tab == tabInstalled {
			m.showAll = !m.showAll
		}
	case "a":
		if m.tab == tabInstalled {
			return m, m.prompt("add", "Install package: ")
		}
	case "r":
		switch m.tab {
		case tabInstalled:
			return m, cmdInstalled
		case tabScan:
			m.scanning = true
			return m, cmdScan(m.inst)
		case tabApt:
			m.idxLoading = true
			return m, cmdIndex
		}
	case "s":
		if m.tab == tabScan {
			m.scanning = true
			return m, cmdScan(m.inst)
		}
	case "R":
		if m.tab == tabScan {
			j := &job{label: "apt update", args: []string{"apt-get", "update"}, reload: true}
			return m, m.runRoot(j)
		}
	case "U":
		if m.tab == tabScan {
			return m, m.runRoot(aptJob("upgrade all", "upgrade"))
		}
	case "A":
		if m.tab == tabScan {
			return m, m.runRoot(aptJob("autoremove", "autoremove"))
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
		return m, cmdDetails(sel.name)
	case "o":
		if sel != nil && sel.name != "" {
			m.openHomepage(sel.name)
		}
	case "i":
		if sel == nil || sel.name == "" || sel.section != "" {
			return m, nil
		}
		if m.instSet[sel.name] {
			m.setStatus(sWarn, "lindeck: %s is already installed", sel.name)
			return m, nil
		}
		return m, m.runRoot(aptJob("install "+sel.name, "install", "--", sel.name))
	case "u":
		if m.tab == tabScan && sel != nil {
			return m, m.runRoot(aptJob("upgrade "+sel.name, "install", "--only-upgrade", "--", sel.name))
		}
	case "d":
		if sel == nil || sel.name == "" || sel.section != "" {
			return m, nil
		}
		if !m.instSet[sel.name] {
			m.setStatus(sWarn, "lindeck: %s is not installed", sel.name)
			return m, nil
		}
		return m, m.runRoot(aptJob("remove "+sel.name, "remove", "--", sel.name))
	}
	return m, nil
}

func (m model) handleInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		if m.inputKind == "password" {
			m.pending = nil
			m.setStatus(sComment, "cancelled")
		}
		m.inputKind = ""
		m.input.Reset()
		m.input.Blur()
		return m, nil
	case "enter":
		kind, val := m.inputKind, strings.TrimSpace(m.input.Value())
		if kind == "password" {
			m.setStatus(sComment, "checking password …")
			return m, cmdValidate(m.input.Value())
		}
		m.inputKind = ""
		m.input.Blur()
		switch kind {
		case "search":
			if m.idx == nil {
				m.pendingQ = &val
			} else {
				m.search(val)
			}
		case "add":
			if val != "" {
				return m, m.runRoot(aptJob("install "+val, "install", "--", val))
			}
		case "color":
			m.setColor(m.editSlot, val)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *model) openHomepage(name string) {
	var p *Pkg
	if m.idx != nil {
		p = m.idx.byName[name]
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
	tabs := []string{"1:Installed", "2:Scan", "3:apt", "4:Theme"}
	var parts []string
	for i, t := range tabs {
		if tab(i) == m.tab {
			parts = append(parts, "["+t+"]")
		} else {
			parts = append(parts, " "+t+" ")
		}
	}
	return []seg{{"  " + strings.Join(parts, " ") + "   source: apt", sTitle}}
}

func (m model) footer() string {
	switch m.tab {
	case tabInstalled:
		return "a:add  d:remove  m:manual/all  r:refresh  <CR>:details  o:homepage  1-4:tabs  q:quit"
	case tabScan:
		return "s:rescan  u:upgrade  U:upgrade all  A:autoremove  R:apt update  <CR>:details  q:quit"
	case tabTheme:
		return "<CR>:apply theme / edit color  y:copy theme into custom  1-4:tabs  q:quit"
	}
	if m.aptView == "sections" {
		return "<CR>:browse section  /:search  <BS>:back to packages  r:refresh  1-4:tabs  q:quit"
	}
	return "t:sections  /:search  <CR>:details  i:install  d:remove  o:homepage  <BS>:sections  q:quit"
}

// header returns the non-selectable rows above the items for the current view.
func (m model) header() [][]seg {
	line := func(s string, st lipgloss.Style) [][]seg { return [][]seg{{{s, st}}} }
	switch m.tab {
	case tabInstalled:
		if m.inst == nil {
			return line("  reading dpkg database …", sComment)
		}
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
			return line("  scanning packages …", sComment)
		}
		c := map[string]int{}
		for _, r := range m.scan {
			c[r.Status]++
		}
		return [][]seg{{
			{"  ", sText}, {fmt.Sprintf("%d ok", c["ok"]), sOk},
			{"   ", sText}, {fmt.Sprintf("%d warnings", c["warn"]), sWarn},
			{"   ", sText}, {fmt.Sprintf("%d errors", c["err"]), sErr},
		}, {}}
	case tabApt:
		if m.idxLoading {
			return line("  loading apt index …", sComment)
		}
		if m.idxErr != nil {
			return append(line("  error: "+m.idxErr.Error(), sErr),
				line("  press r to retry", sComment)...)
		}
		if m.aptView == "sections" {
			return [][]seg{{{fmt.Sprintf("  apt sections — %d  (press <CR> to browse one)", len(m.idx.sections)), sText}}, {}}
		}
		title := "results for '" + m.query + "'"
		if s, ok := strings.CutPrefix(m.query, "section:"); ok {
			title = "section '" + s + "'"
		} else if m.query == "" {
			title = "all packages"
		}
		return [][]seg{{
			{fmt.Sprintf("  %s — %d packages   ", title, len(m.results)), sText},
			{"✓", sOk}, {" = already installed", sText},
		}, {}}
	}
	return nil
}

func (m model) items() []item {
	var items []item
	switch m.tab {
	case tabInstalled:
		for i := range m.inst {
			in := &m.inst[i]
			if m.showAll || in.Manual || in.State != "" {
				items = append(items, item{name: in.Name, inst: in})
			}
		}
	case tabScan:
		for i := range m.scan {
			items = append(items, item{name: m.scan[i].Name, check: &m.scan[i]})
		}
	case tabTheme:
		for _, p := range presets {
			items = append(items, item{theme: p.Name})
		}
		items = append(items, item{theme: customName})
		for i := range slots {
			items = append(items, item{isSlot: true, slot: i})
		}
	case tabApt:
		if m.idx == nil || m.idxLoading {
			break
		}
		if m.aptView == "sections" {
			for _, s := range m.idx.sections {
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
	case it.check != nil:
		return max(len(it.check.Notes), 1)
	case it.pkg != nil:
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
	case it.inst != nil:
		in := it.inst
		icon, status, nameSt := icons.auto, "auto", sText
		if in.Manual {
			icon, status = icons.manual, "manual"
		}
		nw, vw, showSummary := m.columns(13, 38, 24) // icon, status and spacing
		summary := ""
		if showSummary {
			summary = in.Summary
		}
		ist := sDir
		if in.State != "" {
			icon, ist, status, nameSt = icons.err, sErr, "ERROR", sErr
		}
		return [][]seg{{
			{"  ", sText}, {icon, ist},
			{fmt.Sprintf(" %-*s ", nw, trunc(in.Name, nw)), nameSt},
			{fmt.Sprintf("%-7s ", status), nameSt},
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
		nw := max(14, min(38, (m.innerWidth()-6)*45/100))
		rows := [][]seg{{{"  ", sText}, {icon, st}, {fmt.Sprintf(" %-*s ", nw, trunc(r.Name, nw)), sText}, {note, sText}}}
		for _, n := range r.Notes[min(1, len(r.Notes)):] {
			rows = append(rows, []seg{{"      · " + n, sComment}})
		}
		return rows

	case it.section != "":
		count := 0
		for _, s := range m.idx.sections {
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
		if m.instSet[p.Name] {
			mark, mst = "✓", sOk
		}
		return [][]seg{
			{{"  ", sText}, {mark, mst}, {fmt.Sprintf(" %-*s ", nw, trunc(p.Name, nw)), sText},
				{fmt.Sprintf("%-*s ", vw, trunc(p.Version, vw)), sComment}, {section, sDir}},
			{{"        " + p.Desc, sComment}},
		}
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
		if used == 0 {
			cur = append(cur, seg{"  ", sHint})
		} else {
			cur = append(cur, seg{"  ", sHint})
			used += 2
		}
		cur = append(cur, seg{key, sHintKey}, seg{":" + label, sHint})
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
		t := []rune(s.t)
		if len(t) > left {
			t = append(t[:max(left-1, 0)], '…')
		}
		b.WriteString(st.Render(string(t)))
		left -= len(t)
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
		body := [][]seg{{{"  " + m.detailsTitle, sTitle}}, {}}
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
				out, sel = append(out, r), append(sel, i == cur)
			}
		}
	}
	if len(items) == 0 && m.tab == tabInstalled && m.inst != nil && len(out) < bh {
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

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:max(n-1, 0)]) + "…"
}

// ---------------------------------------------------------------------------
// Theme tab
// ---------------------------------------------------------------------------

// swatches renders a theme's palette as a row of colored blocks.
func swatches(t Theme) []seg {
	var out []seg
	for _, s := range slots {
		out = append(out, seg{"██", lipgloss.NewStyle().Foreground(lipgloss.Color(*s.get(&t)))}, seg{" ", sText})
	}
	return out
}

func (m model) themeRows(name string) [][]seg {
	mark, mst := "  ", sText
	if m.cfg.Theme == name {
		mark, mst = "✓ ", sOk
	}
	if name == customName {
		t := m.customTheme()
		row := append([]seg{{"  ", sText}, {mark, mst}, {fmt.Sprintf("%-20s", name), sText}}, swatches(t)...)
		return [][]seg{{}, row, {{"      your own colors — <CR> on a color below to change it, or y on a theme to start from it", sComment}}}
	}
	t, _ := preset(name)
	return [][]seg{append([]seg{{"  ", sText}, {mark, mst}, {fmt.Sprintf("%-20s", name), sText}}, swatches(t)...)}
}

func (m model) slotRows(i int) [][]seg {
	t := m.customTheme()
	v := *slots[i].get(&t)
	return [][]seg{{
		{fmt.Sprintf("      %-12s", slots[i].name), sText},
		{"████ ", lipgloss.NewStyle().Foreground(lipgloss.Color(v))},
		{v, sComment},
	}}
}

// customTheme is the custom palette. Until one is saved it starts as a copy
// of the theme in use, so editing a color tweaks what you're looking at.
func (m model) customTheme() Theme {
	if m.cfg.Custom == nil {
		return m.cfg.resolve()
	}
	return Config{Theme: customName, Custom: m.cfg.Custom}.resolve()
}

func (m *model) save(msg string) {
	if err := saveConfig(m.cfg); err != nil {
		m.setStatus(sErr, "could not save theme: %v", err)
		return
	}
	m.setStatus(sOk, "✓ %s", msg)
}

func (m *model) themeEnter(it item) tea.Cmd {
	switch {
	case it.isSlot:
		m.editSlot = it.slot
		t := m.customTheme()
		cmd := m.prompt("color", slots[it.slot].name+" color (#rrggbb): ")
		m.input.SetValue(*slots[it.slot].get(&t))
		m.input.CursorEnd()
		return cmd
	case it.theme != "":
		m.cfg.Theme = it.theme
		m.save("theme set to " + it.theme)
	}
	return nil
}

// copyToCustom seeds the custom palette from a theme so it can be tweaked.
func (m *model) copyToCustom(name string) tea.Cmd {
	t, ok := preset(name)
	if !ok {
		t = m.customTheme()
	}
	m.cfg.Custom = &t
	m.cfg.Theme = customName
	m.save("copied " + name + " into custom — edit its colors below")
	return nil
}

func (m *model) setColor(i int, val string) {
	hex, ok := normalizeHex(val)
	if !ok {
		m.setStatus(sErr, "%q isn't a hex color — use #rrggbb, like #b4befe", val)
		return
	}
	t := m.customTheme()
	*slots[i].get(&t) = hex
	m.cfg.Custom = &t
	m.cfg.Theme = customName
	m.save(fmt.Sprintf("%s set to %s (theme: custom)", slots[i].name, hex))
}
