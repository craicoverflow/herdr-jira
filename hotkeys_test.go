package main

import (
	"os"
	"strings"
	"testing"
)

func TestNoKeyIsBoundTwiceInOneMode(t *testing.T) {
	type slot struct {
		mode inputMode
		key  string
	}
	seen := map[slot]string{}
	for _, h := range hotkeys {
		s := slot{h.Mode, h.Key}
		if prev, dup := seen[s]; dup {
			t.Errorf("key %q in mode %v is bound to both %q and %q", h.Key, h.Mode, prev, h.Action)
		}
		seen[s] = h.Action
	}
}

func TestEveryActionHasAHandler(t *testing.T) {
	for _, h := range hotkeys {
		if handlers[h.Action] == nil {
			t.Errorf("action %q (key %q) has no handler", h.Action, h.Key)
		}
	}
	for action := range handlers {
		found := false
		for _, h := range hotkeys {
			if h.Action == action {
				found = true
			}
		}
		if !found {
			t.Errorf("handler %q has no key", action)
		}
	}
}

func TestEveryActionHasALabelOnItsFirstEntry(t *testing.T) {
	for _, mode := range []inputMode{modeBrowse, modeSearch} {
		for _, action := range actionOrder(mode) {
			if _, label := actionKeys(mode, action); label == "" {
				t.Errorf("action %q has no label", action)
			}
		}
	}
}

func TestLookupHotkeyHonoursModeAndRowKind(t *testing.T) {
	epic, issue := row{Kind: rowEpic}, row{Kind: rowIssue}
	if _, ok := lookupHotkey(modeBrowse, "s", epic, true); !ok {
		t.Error("s must work on an epic row")
	}
	if _, ok := lookupHotkey(modeBrowse, "s", issue, true); ok {
		t.Error("s must not apply to an issue row")
	}
	if _, ok := lookupHotkey(modeBrowse, "s", row{}, false); ok {
		t.Error("s must not apply with no row")
	}
	if _, ok := lookupHotkey(modeSearch, "s", epic, true); ok {
		t.Error("s is a letter, not a command, in search mode")
	}
	if _, ok := lookupHotkey(modeBrowse, "tab", row{}, false); !ok {
		t.Error("tab needs no row")
	}
	if _, ok := lookupHotkey(modeBrowse, "e", epic, true); ok {
		t.Error("the old e key must be gone")
	}
	if _, ok := lookupHotkey(modeBrowse, "ctrl+f", epic, true); ok {
		t.Error("the old ctrl+f key must be gone")
	}
}

func TestFooterListsOnlyKeysValidForTheFocusedRow(t *testing.T) {
	onEpic := footerLegend(modeBrowse, row{Kind: rowEpic}, true, 200)
	onIssue := footerLegend(modeBrowse, row{Kind: rowIssue}, true, 200)
	if !strings.Contains(onEpic, "[s] star") {
		t.Errorf("epic footer %q lacks [s]", onEpic)
	}
	if strings.Contains(onIssue, "[s]") {
		t.Errorf("issue footer %q must not offer [s]", onIssue)
	}
	empty := footerLegend(modeBrowse, row{}, false, 200)
	if strings.Contains(empty, "[enter]") || !strings.Contains(empty, "[/] search") {
		t.Errorf("footer with no row = %q", empty)
	}
}

func TestFooterDiffersByMode(t *testing.T) {
	search := footerLegend(modeSearch, row{Kind: rowIssue}, true, 200)
	if !strings.Contains(search, "[esc] clear search") || strings.Contains(search, "[t]") {
		t.Errorf("search footer = %q", search)
	}
}

func TestNarrowFooterTruncatesWithMore(t *testing.T) {
	line := footerLegend(modeBrowse, row{Kind: rowEpic}, true, 40)
	if !strings.HasSuffix(line, "[?] more") {
		t.Errorf("narrow footer %q must end with [?] more", line)
	}
	if n := len([]rune(line)); n > 40-2 {
		t.Errorf("narrow footer is %d wide, want <= 38", n)
	}
	if !strings.HasPrefix(line, "[enter] open") {
		t.Errorf("narrow footer %q should keep the first entries", line)
	}
	if got := footerLegend(modeBrowse, row{Kind: rowEpic}, true, 12); got != "[?] more" {
		t.Errorf("tiny footer = %q, want just [?] more", got)
	}
}

func TestHelpTextListsEveryAction(t *testing.T) {
	text := helpText()
	for _, h := range hotkeys {
		if !strings.Contains(text, h.Hint) {
			t.Errorf("help lacks key %q", h.Hint)
		}
	}
}

func TestHotkeyMarkdownCoversBrowseModeOnly(t *testing.T) {
	md := hotkeyMarkdown()
	if !strings.Contains(md, "`enter`") || !strings.Contains(md, "`↓` / `j`") {
		t.Errorf("markdown lacks expected rows:\n%s", md)
	}
	if strings.Contains(md, "clear search") {
		t.Error("search-mode keys do not belong in the browse table")
	}
}

func TestREADMEKeyTableMatchesTheHotkeyTable(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "<!-- hotkeys:start -->")
	end := strings.Index(text, "<!-- hotkeys:end -->")
	if start < 0 || end < start {
		t.Fatal("README.md lacks the hotkeys:start / hotkeys:end markers")
	}
	got := strings.TrimSpace(text[start+len("<!-- hotkeys:start -->") : end])
	if want := strings.TrimSpace(hotkeyMarkdown()); got != want {
		t.Errorf("README key table is out of date with hotkeys.go.\n got:\n%s\nwant:\n%s", got, want)
	}
}
