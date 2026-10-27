package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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
		m.setStatus(sErr, "could not save settings: %v", err)
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
