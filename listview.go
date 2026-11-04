package main

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// wheelStep is how many lines one mouse-wheel tick scrolls.
const wheelStep = 3

// listView is the one scrolling list a tab shows: a flat slice of rows, a
// cursor that skips headings, and a scroll offset. One row is one line, so a
// terminal line maps straight back to a row for mouse hit-testing.
type listView struct {
	rows   []row
	cursor int // index into rows, or -1 when nothing is selectable
	offset int // index of the first visible row
	height int // visible lines
	th     theme
}

func newListView(th theme) listView {
	return listView{cursor: -1, height: 20, th: th}
}

// selected returns the row under the cursor.
func (v *listView) selected() (row, bool) {
	if v.cursor < 0 || v.cursor >= len(v.rows) {
		return row{}, false
	}
	return v.rows[v.cursor], true
}

// setRows swaps in new rows. The cursor stays on the same issue key when it
// is still present (not the same index), otherwise it lands on the nearest
// selectable row.
func (v *listView) setRows(rows []row) {
	prevKey := ""
	if r, ok := v.selected(); ok {
		prevKey = r.Issue.Key
	}
	prevIdx := v.cursor
	v.rows = rows
	v.cursor = -1
	if prevKey != "" && v.selectKey(prevKey) {
		return
	}
	v.cursor = v.nearestSelectable(prevIdx)
	v.scrollToCursor()
}

// selectKey moves the cursor to the row with this issue key.
func (v *listView) selectKey(key string) bool {
	for i, r := range v.rows {
		if r.Kind.selectable() && r.Issue.Key == key {
			v.cursor = i
			v.scrollToCursor()
			return true
		}
	}
	return false
}

// nearestSelectable returns the selectable row closest to idx (preferring
// forward), or -1 when there is none.
func (v *listView) nearestSelectable(idx int) int {
	if idx < 0 {
		idx = 0
	}
	for d := 0; d < len(v.rows); d++ {
		for _, i := range []int{idx + d, idx - d} {
			if i >= 0 && i < len(v.rows) && v.rows[i].Kind.selectable() {
				return i
			}
		}
	}
	return -1
}

// moveCursor moves by delta selectable rows, skipping headings and notes, and
// stops at the ends.
func (v *listView) moveCursor(delta int) {
	if v.cursor < 0 || delta == 0 {
		return
	}
	step := 1
	if delta < 0 {
		step, delta = -1, -delta
	}
	for ; delta > 0; delta-- {
		next := v.cursor + step
		for next >= 0 && next < len(v.rows) && !v.rows[next].Kind.selectable() {
			next += step
		}
		if next < 0 || next >= len(v.rows) {
			break
		}
		v.cursor = next
	}
	v.scrollToCursor()
}

// scrollToCursor keeps the cursor on screen. When the cursor is on the first
// row after a heading, the heading is kept in view too.
func (v *listView) scrollToCursor() {
	if v.cursor < 0 || v.height <= 0 {
		v.clampOffset()
		return
	}
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	if v.cursor >= v.offset+v.height {
		v.offset = v.cursor - v.height + 1
	}
	if v.cursor > 0 && v.rows[v.cursor-1].Kind == rowHeading && v.offset > v.cursor-1 {
		v.offset = v.cursor - 1
	}
	v.clampOffset()
}

func (v *listView) clampOffset() {
	maxOff := len(v.rows) - v.height
	if maxOff < 0 {
		maxOff = 0
	}
	if v.offset > maxOff {
		v.offset = maxOff
	}
	if v.offset < 0 {
		v.offset = 0
	}
}

// setHeight resizes the visible window.
func (v *listView) setHeight(h int) {
	if h < 1 {
		h = 1
	}
	v.height = h
	v.scrollToCursor()
}

// scrollBy scrolls the window by n lines (the mouse wheel) and, if the cursor
// is pushed off screen, moves it to the nearest visible selectable row.
func (v *listView) scrollBy(n int) {
	v.offset += n
	v.clampOffset()
	if v.cursor < 0 {
		return
	}
	if v.cursor < v.offset {
		v.cursor = v.firstSelectableFrom(v.offset, 1, v.cursor)
	} else if v.cursor >= v.offset+v.height {
		v.cursor = v.firstSelectableFrom(v.offset+v.height-1, -1, v.cursor)
	}
}

func (v *listView) firstSelectableFrom(i, step, fallback int) int {
	for ; i >= 0 && i < len(v.rows); i += step {
		if v.rows[i].Kind.selectable() {
			return i
		}
	}
	return fallback
}

// hitTest returns the selectable row index under a line relative to the
// list's first line, or -1 for a heading, note, blank line or out of range.
func (v *listView) hitTest(y int) int {
	if y < 0 || y >= v.height {
		return -1
	}
	i := v.offset + y
	if i < 0 || i >= len(v.rows) || !v.rows[i].Kind.selectable() {
		return -1
	}
	return i
}

// render draws the visible rows, one line each, no wider than width. It
// returns at most height lines; the caller pads.
func (v *listView) render(width int) string {
	end := v.offset + v.height
	if end > len(v.rows) {
		end = len(v.rows)
	}
	lines := make([]string, 0, v.height)
	for i := v.offset; i < end; i++ {
		lines = append(lines, v.renderRow(v.rows[i], i == v.cursor, width))
	}
	return strings.Join(lines, "\n")
}

func (v *listView) renderRow(r row, selected bool, width int) string {
	th := v.th
	switch r.Kind {
	case rowHeading:
		return ansi.Truncate(th.heading.Render(" "+headingText(r)), width, "…")
	case rowNote:
		return ansi.Truncate(th.dim.Render("   "+r.Text), width, "…")
	}

	pointer := "  "
	if selected {
		pointer = "▸ "
	}
	indent := strings.Repeat("  ", r.Depth)
	glyph := issueGlyph(r.Issue.IssueType)
	if r.Kind == rowEpic && r.Issue.IssueType == "" {
		glyph = issueGlyph("Epic")
	}
	key := r.Issue.Key
	star := ""
	if r.Starred {
		star = " ★"
	}
	status := r.Issue.StatusName

	prefixW := ansi.StringWidth(pointer + indent + glyph + " " + key + star + "  ")
	statusW := ansi.StringWidth(status)
	summaryW := width - prefixW - statusW - 2
	showStatus := true
	if summaryW < 10 {
		showStatus = false
		summaryW = width - prefixW
	}
	if summaryW < 0 {
		summaryW = 0
	}
	summary := ansi.Truncate(r.Issue.Summary, summaryW, "…")

	line := pointer + indent + glyph + " " + th.key.Render(key)
	if r.Starred {
		line += th.star.Render(star)
	}
	line += "  " + summary
	if showStatus && status != "" {
		pad := width - ansi.StringWidth(line) - statusW
		if pad < 1 {
			pad = 1
		}
		line += strings.Repeat(" ", pad) + th.statusStyle(r.Issue.StatusCategory).Render(status)
	}
	if selected {
		line = th.selected.Render(line)
	}
	return ansi.Truncate(line, width, "")
}
