package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// inputMode is shown in the footer. In browse mode letters are commands; in
// search mode letters type into the query.
type inputMode int

const (
	modeBrowse inputMode = iota
	modeSearch
)

// hotkey is one key binding. Key is bubbletea's msg.String() for the key;
// Hint is how it is shown. The first entry for an action carries its Label
// (the footer and README text); aliases leave Label empty. Rows lists the row
// kinds the binding applies to; nil means it needs no particular row.
type hotkey struct {
	Key    string
	Hint   string
	Mode   inputMode
	Action string
	Label  string // short, for the footer legend
	Desc   string // fuller text for the `?` list and README; defaults to Label
	Rows   []rowKind
}

var (
	anyRow  = []rowKind{rowIssue, rowSubtask, rowEpic}
	epicRow = []rowKind{rowEpic}
)

// hotkeys is the one table that drives both the key handlers and the footer
// legend (and the README key list), so they cannot disagree.
var hotkeys = []hotkey{
	{Key: "enter", Hint: "enter", Mode: modeBrowse, Action: "open", Label: "open", Desc: "open the detail screen (an epic opens its children)", Rows: anyRow},
	{Key: "/", Hint: "/", Mode: modeBrowse, Action: "search", Label: "search", Desc: "search: filters the lanes as you type, then searches Jira"},
	{Key: "t", Hint: "t", Mode: modeBrowse, Action: "transition", Label: "transition", Desc: "change status (a live list of valid transitions)", Rows: anyRow},
	{Key: "a", Hint: "a", Mode: modeBrowse, Action: "assign", Label: "assign", Desc: "assign to me", Rows: anyRow},
	{Key: "b", Hint: "b", Mode: modeBrowse, Action: "branch", Label: "branch", Desc: "new branch from the issue", Rows: anyRow},
	{Key: "s", Hint: "s", Mode: modeBrowse, Action: "star", Label: "star", Desc: "star or unstar the epic (epics only)", Rows: epicRow},
	{Key: "c", Hint: "c", Mode: modeBrowse, Action: "copy", Label: "copy", Desc: "copy the issue key", Rows: anyRow},
	{Key: "o", Hint: "o", Mode: modeBrowse, Action: "browser", Label: "browser", Desc: "open the issue in the browser", Rows: anyRow},
	{Key: "down", Hint: "↓", Mode: modeBrowse, Action: "down", Label: "move down"},
	{Key: "j", Hint: "j", Mode: modeBrowse, Action: "down"},
	{Key: "up", Hint: "↑", Mode: modeBrowse, Action: "up", Label: "move up"},
	{Key: "k", Hint: "k", Mode: modeBrowse, Action: "up"},
	{Key: "tab", Hint: "tab", Mode: modeBrowse, Action: "next_tab", Label: "next tab", Desc: "next tab"},
	{Key: "shift+tab", Hint: "shift+tab", Mode: modeBrowse, Action: "prev_tab", Label: "previous tab"},
	{Key: "1", Hint: "1", Mode: modeBrowse, Action: "tab_issues", Label: "Issues tab", Desc: "jump to Issues"},
	{Key: "2", Hint: "2", Mode: modeBrowse, Action: "tab_epics", Label: "Epics tab", Desc: "jump to Epics"},
	{Key: "r", Hint: "r", Mode: modeBrowse, Action: "refresh", Label: "refresh", Desc: "refresh the current tab"},
	{Key: "?", Hint: "?", Mode: modeBrowse, Action: "help", Label: "keys", Desc: "show all keys"},
	{Key: "esc", Hint: "esc", Mode: modeBrowse, Action: "back", Label: "close", Desc: "close the popup (or clear a search)"},
	{Key: "q", Hint: "q", Mode: modeBrowse, Action: "quit", Label: "close", Desc: "close the popup"},

	{Key: "enter", Hint: "enter", Mode: modeSearch, Action: "open", Label: "open", Rows: anyRow},
	{Key: "down", Hint: "↓", Mode: modeSearch, Action: "down", Label: "move down"},
	{Key: "up", Hint: "↑", Mode: modeSearch, Action: "up", Label: "move up"},
	{Key: "esc", Hint: "esc", Mode: modeSearch, Action: "clear_search", Label: "clear search"},
}

// footerPriority orders the footer legend; entries with a Label that are not
// listed here come last. The full list is behind `?`.
var footerPriority = []string{"open", "search", "transition", "assign", "branch", "star", "refresh", "help", "back", "clear_search"}

// handlers maps each action name to its handler. A handler mutates the model
// and returns a command; r is the row under the cursor (zero if none).
var handlers = map[string]func(*pickerModel, row) tea.Cmd{}

func rowAllowed(h hotkey, r row, hasRow bool) bool {
	if h.Rows == nil {
		return true
	}
	if !hasRow {
		return false
	}
	for _, k := range h.Rows {
		if k == r.Kind {
			return true
		}
	}
	return false
}

// lookupHotkey finds the binding for a key in a mode that applies to the
// focused row.
func lookupHotkey(mode inputMode, key string, r row, hasRow bool) (hotkey, bool) {
	for _, h := range hotkeys {
		if h.Mode == mode && h.Key == key && rowAllowed(h, r, hasRow) {
			return h, true
		}
	}
	return hotkey{}, false
}

// footerLegend renders the one-line legend for a mode and focused row. At
// widths under 60 it keeps the first entries that fit, then "[?] more".
func footerLegend(mode inputMode, r row, hasRow bool, width int) string {
	var parts []string
	seen := map[string]bool{}
	for _, action := range footerPriority {
		for _, h := range hotkeys {
			if h.Mode != mode || h.Action != action || h.Label == "" || seen[action] {
				continue
			}
			if !rowAllowed(h, r, hasRow) {
				continue
			}
			seen[action] = true
			label := h.Label
			parts = append(parts, "["+h.Hint+"] "+label)
		}
	}
	line := strings.Join(parts, "  ")
	// The footer is drawn after a two-space margin; a legend that does not fit
	// (always the case under 60 columns) keeps the first entries that do.
	if width <= 0 || len([]rune(line)) <= width-2 {
		return line
	}
	more := "[?] more"
	out := ""
	for _, p := range parts {
		if strings.HasPrefix(p, "[?]") {
			continue
		}
		next := p
		if out != "" {
			next = out + "  " + p
		}
		if len([]rune(next))+len([]rune(more))+4 > width {
			break
		}
		out = next
	}
	if out == "" {
		return more
	}
	return out + "  " + more
}

// helpText renders the full key list for `?`.
func helpText() string {
	var b strings.Builder
	for _, mode := range []inputMode{modeBrowse, modeSearch} {
		if mode == modeBrowse {
			b.WriteString("  Browse mode\n\n")
		} else {
			b.WriteString("\n  Search mode (letters type)\n\n")
		}
		for _, action := range actionOrder(mode) {
			hints, label := actionKeys(mode, action)
			b.WriteString("    " + padRight(strings.Join(hints, " / "), 22) + label + "\n")
		}
	}
	b.WriteString("\n  press any key to go back\n")
	return b.String()
}

func padRight(s string, n int) string {
	for len([]rune(s)) < n {
		s += " "
	}
	return s
}

// actionOrder returns a mode's actions in table order, once each.
func actionOrder(mode inputMode) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hotkeys {
		if h.Mode == mode && !seen[h.Action] {
			seen[h.Action] = true
			out = append(out, h.Action)
		}
	}
	return out
}

// actionKeys returns every key hint bound to an action in a mode, and its label.
func actionKeys(mode inputMode, action string) (hints []string, label string) {
	for _, h := range hotkeys {
		if h.Mode == mode && h.Action == action {
			hints = append(hints, h.Hint)
			if h.Desc != "" {
				label = h.Desc
			} else if h.Label != "" && label == "" {
				label = h.Label
			}
		}
	}
	return hints, label
}

// hotkeyMarkdown dumps the browse-mode table for the README.
func hotkeyMarkdown() string {
	var b strings.Builder
	b.WriteString("| Key | Action |\n|---|---|\n")
	for _, action := range actionOrder(modeBrowse) {
		hints, label := actionKeys(modeBrowse, action)
		quoted := make([]string, len(hints))
		for i, h := range hints {
			quoted[i] = "`" + h + "`"
		}
		b.WriteString("| " + strings.Join(quoted, " / ") + " | " + label + " |\n")
	}
	return b.String()
}
