package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Theme is the set of colors the UI is drawn with. Every value is a
// "#rrggbb" hex string so themes can be written by hand in config.json.
type Theme struct {
	Border    string `json:"border"`    // window border and divider lines
	Title     string `json:"title"`     // "lindeck" in the top border
	Tabs      string `json:"tabs"`      // tab line, headings
	Text      string `json:"text"`      // normal text
	Dim       string `json:"dim"`       // key hints, descriptions, versions
	Accent    string `json:"accent"`    // sections, package icons
	Ok        string `json:"ok"`        // ✓ / ● / success messages
	Warn      string `json:"warn"`      // ▲ / warnings
	Error     string `json:"error"`     // ✗ / errors
	Selection string `json:"selection"` // background of the selected row
	Prompt    string `json:"prompt"`    // input prompt labels
}

// slots lists the theme fields in display order, with getters and setters.
var slots = []struct {
	name string
	get  func(*Theme) *string
}{
	{"border", func(t *Theme) *string { return &t.Border }},
	{"title", func(t *Theme) *string { return &t.Title }},
	{"tabs", func(t *Theme) *string { return &t.Tabs }},
	{"text", func(t *Theme) *string { return &t.Text }},
	{"dim", func(t *Theme) *string { return &t.Dim }},
	{"accent", func(t *Theme) *string { return &t.Accent }},
	{"ok", func(t *Theme) *string { return &t.Ok }},
	{"warn", func(t *Theme) *string { return &t.Warn }},
	{"error", func(t *Theme) *string { return &t.Error }},
	{"selection", func(t *Theme) *string { return &t.Selection }},
	{"prompt", func(t *Theme) *string { return &t.Prompt }},
}

type namedTheme struct {
	Name  string
	Theme Theme
}

// presets are the built-in themes, in menu order. The first is the default.
var presets = []namedTheme{
	{"solarized-osaka", Theme{"#93a1a1", "#268bd2", "#cb4b16", "#839496", "#586e75", "#2aa198", "#859900", "#b58900", "#dc322f", "#073642", "#cb4b16"}},
	{"catppuccin-mocha", Theme{"#b4befe", "#89b4fa", "#fab387", "#cdd6f4", "#7f849c", "#94e2d5", "#a6e3a1", "#f9e2af", "#f38ba8", "#45475a", "#cba6f7"}},
	{"tokyonight", Theme{"#7aa2f7", "#7dcfff", "#ff9e64", "#c0caf5", "#565f89", "#73daca", "#9ece6a", "#e0af68", "#f7768e", "#283457", "#bb9af7"}},
	{"gruvbox", Theme{"#a89984", "#83a598", "#fe8019", "#ebdbb2", "#928374", "#8ec07c", "#b8bb26", "#fabd2f", "#fb4934", "#3c3836", "#d3869b"}},
	{"nord", Theme{"#88c0d0", "#81a1c1", "#d08770", "#d8dee9", "#616e88", "#8fbcbb", "#a3be8c", "#ebcb8b", "#bf616a", "#3b4252", "#b48ead"}},
	{"dracula", Theme{"#bd93f9", "#8be9fd", "#ffb86c", "#f8f8f2", "#6272a4", "#8be9fd", "#50fa7b", "#f1fa8c", "#ff5555", "#44475a", "#ff79c6"}},
	{"rose-pine", Theme{"#c4a7e7", "#ebbcba", "#f6c177", "#e0def4", "#6e6a86", "#ebbcba", "#9ccfd8", "#f6c177", "#eb6f92", "#26233a", "#c4a7e7"}},
	{"everforest", Theme{"#a7c080", "#7fbbb3", "#e69875", "#d3c6aa", "#859289", "#83c092", "#a7c080", "#dbbc7f", "#e67e80", "#3d484d", "#d699b6"}},
	{"kanagawa", Theme{"#7e9cd8", "#7fb4ca", "#ffa066", "#dcd7ba", "#727169", "#7aa89f", "#98bb6c", "#e6c384", "#ff5d62", "#2d4f67", "#957fb8"}},
	{"one-dark", Theme{"#61afef", "#56b6c2", "#d19a66", "#abb2bf", "#5c6370", "#56b6c2", "#98c379", "#e5c07b", "#e06c75", "#3e4451", "#c678dd"}},
}

const customName = "custom"

func preset(name string) (Theme, bool) {
	for _, p := range presets {
		if p.Name == name {
			return p.Theme, true
		}
	}
	return Theme{}, false
}

// Config is what lindeck saves in ~/.config/lindeck/config.json.
type Config struct {
	Theme  string `json:"theme"`            // a preset name, or "custom"
	Custom *Theme `json:"custom,omitempty"` // colors used when Theme is "custom"
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "lindeck", "config.json"), nil
}

// loadConfig reads the config file. A missing or unreadable file gives the
// default theme; a bad file is reported but never stops lindeck starting.
func loadConfig() (Config, error) {
	cfg := Config{Theme: presets[0].Name}
	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{Theme: presets[0].Name}, errors.New("config.json: " + err.Error())
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// resolve returns the theme a config selects. Custom colors that are missing
// or invalid fall back to the default theme's value for that slot.
func (cfg Config) resolve() Theme {
	if cfg.Theme != customName {
		if t, ok := preset(cfg.Theme); ok {
			return t
		}
		return presets[0].Theme
	}
	t := presets[0].Theme
	if cfg.Custom != nil {
		for _, s := range slots {
			if v, ok := normalizeHex(*s.get(cfg.Custom)); ok {
				*s.get(&t) = v
			}
		}
	}
	return t
}

var hexRe = regexp.MustCompile(`^#?([0-9a-fA-F]{6}|[0-9a-fA-F]{3})$`)

// normalizeHex accepts "abc", "#abc", "aabbcc" or "#aabbcc" and returns "#aabbcc".
func normalizeHex(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !hexRe.MatchString(s) {
		return "", false
	}
	s = strings.ToLower(strings.TrimPrefix(s, "#"))
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	return "#" + s, true
}

// selBg is the selected-row background; the other styles live in tui.go.
var selBg lipgloss.Color

// applyTheme sets every style the UI draws with.
func applyTheme(t Theme) {
	c := func(s string) lipgloss.Color { return lipgloss.Color(s) }
	sText = lipgloss.NewStyle().Foreground(c(t.Text))
	sTitle = lipgloss.NewStyle().Foreground(c(t.Tabs)).Bold(true)
	sComment = lipgloss.NewStyle().Foreground(c(t.Dim))
	sOk = lipgloss.NewStyle().Foreground(c(t.Ok))
	sWarn = lipgloss.NewStyle().Foreground(c(t.Warn))
	sErr = lipgloss.NewStyle().Foreground(c(t.Error))
	sDir = lipgloss.NewStyle().Foreground(c(t.Accent))
	sBorder = lipgloss.NewStyle().Foreground(c(t.Border))
	sWinTtl = lipgloss.NewStyle().Foreground(c(t.Title)).Bold(true)
	sPrompt = lipgloss.NewStyle().Foreground(c(t.Prompt)).Bold(true)
	sHint = lipgloss.NewStyle().Foreground(c(t.Tabs))
	sHintKey = sHint.Bold(true)
	selBg = c(t.Selection)
}
