package main

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// theme holds every style the picker draws with. Colour is chosen by Jira
// status category (to do, in progress, done), which every search result
// already carries.
type theme struct {
	plain bool

	todo, progress, done, unknown lipgloss.Style
	heading, dim, key             lipgloss.Style
	selected                      lipgloss.Style
	tabActive, tabInactive        lipgloss.Style
	star, errLine                 lipgloss.Style
}

// newTheme builds the styles. In plain mode every style is empty, so output
// is glyphs and text only.
func newTheme(plain bool) theme {
	t := theme{plain: plain}
	if plain {
		return t
	}
	fg := func(c string) lipgloss.Style { return lipgloss.NewStyle().Foreground(lipgloss.Color(c)) }
	t.todo = fg("245")
	t.progress = fg("39")
	t.done = fg("42")
	t.unknown = fg("245")
	t.heading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	t.dim = fg("241")
	t.key = fg("111")
	t.selected = lipgloss.NewStyle().Bold(true)
	t.tabActive = lipgloss.NewStyle().Bold(true).Reverse(true)
	t.tabInactive = fg("245")
	t.star = fg("220")
	t.errLine = fg("203")
	return t
}

// terminalIsPlain reports a terminal with no colour support, or one that
// asked for none (NO_COLOR).
func terminalIsPlain() bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	return lipgloss.ColorProfile() == termenv.Ascii
}

// statusStyle picks the style for a Jira status category key.
func (t theme) statusStyle(category string) lipgloss.Style {
	switch category {
	case "new":
		return t.todo
	case "indeterminate":
		return t.progress
	case "done":
		return t.done
	}
	return t.unknown
}

// genericGlyph is used for any issue type not in the table.
const genericGlyph = "•"

var issueGlyphs = map[string]string{
	"bug":         "◉",
	"story":       "▲",
	"task":        "■",
	"epic":        "◆",
	"sub-task":    "└",
	"subtask":     "└",
	"improvement": "△",
}

// issueGlyph maps an issue type name to a plain Unicode glyph. There is no
// Nerd Font requirement.
func issueGlyph(issueType string) string {
	if g, ok := issueGlyphs[strings.ToLower(strings.TrimSpace(issueType))]; ok {
		return g
	}
	return genericGlyph
}
