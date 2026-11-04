package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestEveryStatusCategoryHasAStyleAndUnknownFallsBack(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	th := newTheme(false)
	seen := map[string]bool{}
	for _, cat := range []string{"new", "indeterminate", "done"} {
		out := th.statusStyle(cat).Render("x")
		if !strings.Contains(out, "\x1b[") {
			t.Errorf("category %q renders without colour: %q", cat, out)
		}
		seen[out] = true
	}
	if len(seen) != 3 {
		t.Error("the three status categories must look different")
	}
	if out := th.statusStyle("mystery").Render("x"); out == "" {
		t.Error("an unknown category must still render")
	}
}

func TestUnknownIssueTypeGetsTheGenericGlyph(t *testing.T) {
	if got := issueGlyph("Initiative"); got != genericGlyph {
		t.Errorf("glyph = %q, want the generic one", got)
	}
	if issueGlyph("") != genericGlyph {
		t.Error("an empty type gets the generic glyph")
	}
	for _, name := range []string{"Bug", "story", " Task ", "Epic", "Sub-task", "subtask"} {
		if issueGlyph(name) == genericGlyph {
			t.Errorf("%q should have its own glyph", name)
		}
	}
}

func TestPlainModeProducesNoEscapeSequences(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	v := newListView(newTheme(true))
	v.height = 10
	v.setRows(twoLanes())
	if out := v.render(80); strings.Contains(out, "\x1b") {
		t.Errorf("plain mode leaked escape sequences: %q", out)
	}
	m := newPickerModel(&fakeJiraSource{})
	m.th = newTheme(true)
	if strings.Contains(m.tabBar(), "\x1b") {
		t.Error("plain tab bar leaked escape sequences")
	}
}

func TestNoColorEnvForcesPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if !terminalIsPlain() {
		t.Error("NO_COLOR must select plain mode")
	}
}
