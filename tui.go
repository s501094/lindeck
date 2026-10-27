package main

// Floating-window UI with three tabs: Installed, Scan, apt (search/browse).
// Layout and keys mirror plugdeck.nvim.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// solarized palette, to sit alongside solarized-osaka in nvim
var (
	cBase01 = lipgloss.Color("#586e75")
	cBase02 = lipgloss.Color("#073642")
	cBase0  = lipgloss.Color("#839496")
	cYellow = lipgloss.Color("#b58900")
	cOrange = lipgloss.Color("#cb4b16")
	cRed    = lipgloss.Color("#dc322f")
	cBlue   = lipgloss.Color("#268bd2")
	cCyan   = lipgloss.Color("#2aa198")
	cGreen  = lipgloss.Color("#859900")

	sText    = lipgloss.NewStyle().Foreground(cBase0)
	sTitle   = lipgloss.NewStyle().Foreground(cOrange).Bold(true)
	sComment = lipgloss.NewStyle().Foreground(cBase01)
	sOk      = lipgloss.NewStyle().Foreground(cGreen)
	sWarn    = lipgloss.NewStyle().Foreground(cYellow)
	sErr     = lipgloss.NewStyle().Foreground(cRed)
	sDir     = lipgloss.NewStyle().Foreground(cCyan)
	sBorder  = lipgloss.NewStyle().Foreground(cBase01)
	sWinTtl  = lipgloss.NewStyle().Foreground(cBlue).Bold(true)
)

var icons = struct{ ok, warn, err, unknown, manual, auto string }{
	ok: "●", warn: "▲", err: "✗", unknown: "○", manual: "◉", auto: "◍",
}

type tab int

const (
	tabInstalled tab = iota
	tabScan
	tabApt
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
	inputKind string // "" | "search" | "add" | "password"

	details      []string
	detailsTitle string

	pending *job // waiting on a password
	job     *job // running
	output  []string
	status  []seg
}

func newModel(query string) model {
	in := textinput.New()
	in.Cursor.Style = lipgloss.NewStyle().Foreground(cBlue)
	in.TextStyle = sText
	in.PromptStyle = sTitle

	m := model{
		tab:        tabInstalled,
		instSet:    map[string]bool{},
		idxLoading: true,
		aptView:    "sections",
		cursor:     map[string]int{},
		offset:     map[string]int{},
		input:      in,
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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	tabs := []string{"1:Installed", "2:Scan", "3:apt"}
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
		return "a:add  d:remove  m:manual/all  r:refresh  <CR>:details  o:homepage  1/2/3:tabs  q:quit"
	case tabScan:
		return "s:rescan  u:upgrade  U:upgrade all  A:autoremove  R:apt update  <CR>:details  q:quit"
	}
	if m.aptView == "sections" {
		return "<CR>:browse section  /:search  <BS>:back to packages  r:refresh  1/2/3:tabs  q:quit"
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
	case it.check != nil:
		return max(len(it.check.Notes), 1)
	case it.pkg != nil:
		return 2
	}
	return 1
}

func (m model) rows(it item) [][]seg {
	switch {
	case it.inst != nil:
		in := it.inst
		icon, status, nameSt := icons.auto, "auto", sText
		if in.Manual {
			icon, status = icons.manual, "manual"
		}
		ist := sDir
		if in.State != "" {
			icon, ist, status, nameSt = icons.err, sErr, "ERROR", sErr
		}
		return [][]seg{{
			{"  ", sText}, {icon, ist},
			{fmt.Sprintf(" %-38s ", trunc(in.Name, 38)), nameSt},
			{fmt.Sprintf("%-10s ", status), nameSt},
			{fmt.Sprintf("%-24s ", trunc(in.Version, 24)), sComment},
			{in.Summary, sComment},
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
		rows := [][]seg{{{"  ", sText}, {icon, st}, {fmt.Sprintf(" %-38s ", trunc(r.Name, 38)), sText}, {note, sText}}}
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
		mark, mst := " ", sText
		if m.instSet[p.Name] {
			mark, mst = "✓", sOk
		}
		return [][]seg{
			{{"  ", sText}, {mark, mst}, {fmt.Sprintf(" %-45s ", trunc(p.Name, 45)), sText},
				{fmt.Sprintf("%-24s ", trunc(p.Version, 24)), sComment}, {p.Section, sDir}},
			{{"        " + p.Desc, sComment}},
		}
	}
	return nil
}

func (m model) winSize() (w, h int) {
	w, h = int(float64(m.width)*0.85), int(float64(m.height)*0.8)
	return max(w, min(m.width, 60)), max(h, min(m.height, 16))
}

const outputRows = 8

// bodyHeight is how many rows the scrolling list gets inside the window.
func (m model) bodyHeight() int {
	_, h := m.winSize()
	used := 2 + 3 + 1 // border, tabs/footer/rule, status line
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
			st = st.Background(cBase02)
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
	add([]seg{{"  " + m.footer(), sComment}}, false)
	add([]seg{{strings.Repeat("─", iw), sComment}}, false)

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
		add([]seg{{"─ output " + strings.Repeat("─", iw), sComment}}, false)
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
