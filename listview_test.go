package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// twoLanes: heading, A-1, A-2, heading, B-1, B-2 (indices 0..5).
func twoLanes() []row {
	return []row{
		{Kind: rowHeading, Lane: "One", Count: 2},
		{Kind: rowIssue, Issue: ticketStatus{Key: "A-1", Summary: "first", StatusName: "Open"}},
		{Kind: rowIssue, Issue: ticketStatus{Key: "A-2", Summary: "second", StatusName: "Open"}},
		{Kind: rowHeading, Lane: "Two", Count: 2},
		{Kind: rowEpic, Issue: ticketStatus{Key: "B-1", Summary: "third"}, Starred: true},
		{Kind: rowNote, Text: "a note"},
		{Kind: rowSubtask, Issue: ticketStatus{Key: "B-2", Summary: "fourth"}, Depth: 1},
	}
}

func newTestView(rows []row, height int) listView {
	v := newListView(newTheme(true))
	v.height = height
	v.setRows(rows)
	return v
}

func TestCursorStartsOnTheFirstSelectableRow(t *testing.T) {
	v := newTestView(twoLanes(), 10)
	if v.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (never on a heading)", v.cursor)
	}
}

func TestMoveCursorSkipsHeadingsAndNotesInBothDirections(t *testing.T) {
	v := newTestView(twoLanes(), 10)
	var visited []int
	for i := 0; i < 4; i++ {
		v.moveCursor(1)
		visited = append(visited, v.cursor)
	}
	if want := []int{2, 4, 6, 6}; !equalInts(visited, want) {
		t.Errorf("down: %v, want %v (stops at the end)", visited, want)
	}
	visited = nil
	for i := 0; i < 5; i++ {
		v.moveCursor(-1)
		visited = append(visited, v.cursor)
	}
	if want := []int{4, 2, 1, 1, 1}; !equalInts(visited, want) {
		t.Errorf("up: %v, want %v (stops at the start)", visited, want)
	}
	v.moveCursor(0)
	v.moveCursor(2)
	if v.cursor != 4 {
		t.Errorf("moving by 2 landed on %d, want 4", v.cursor)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestZeroSelectableRowsMeansNoCursor(t *testing.T) {
	v := newTestView([]row{{Kind: rowHeading, Lane: "Empty"}, {Kind: rowNote, Text: "nothing"}}, 5)
	if v.cursor != -1 {
		t.Fatalf("cursor = %d, want -1", v.cursor)
	}
	v.moveCursor(1)
	if _, ok := v.selected(); ok {
		t.Error("selected must report nothing")
	}
}

func TestScrollFollowsTheCursorAndKeepsTheHeadingInView(t *testing.T) {
	v := newTestView(twoLanes(), 3)
	v.moveCursor(1) // A-2, index 2, window [0,3)
	v.moveCursor(1) // B-1, index 4: window must scroll
	if v.cursor != 4 {
		t.Fatalf("cursor = %d", v.cursor)
	}
	if v.offset > 3 || v.cursor >= v.offset+v.height {
		t.Errorf("offset = %d: the 'Two' heading (row 3) and the cursor (row 4) must both be visible", v.offset)
	}
	// With a one-line window the cursor wins, but with room the heading is kept.
	w := newTestView(twoLanes(), 2)
	w.moveCursor(1)
	w.moveCursor(1)
	if w.offset != 3 {
		t.Errorf("height 2: offset = %d, want 3 (heading and first row)", w.offset)
	}
	v.moveCursor(-1)
	v.moveCursor(-1) // back to A-1
	if v.offset != 0 {
		t.Errorf("offset = %d, want 0 after returning to the first row", v.offset)
	}
}

func TestSetRowsKeepsTheCursorOnTheSameKey(t *testing.T) {
	v := newTestView(twoLanes(), 10)
	v.moveCursor(1)
	v.moveCursor(1) // B-1
	// A new lane appears above; B-1 moves down two indices.
	rows := append([]row{{Kind: rowHeading, Lane: "New"}, {Kind: rowIssue, Issue: ticketStatus{Key: "N-1"}}, {Kind: rowIssue, Issue: ticketStatus{Key: "N-2"}}}, twoLanes()...)
	v.setRows(rows)
	if r, _ := v.selected(); r.Issue.Key != "B-1" {
		t.Errorf("cursor on %q, want B-1 (same key, not same index)", r.Issue.Key)
	}
}

func TestSetRowsFallsBackToTheNearestSelectableRow(t *testing.T) {
	v := newTestView(twoLanes(), 10)
	v.moveCursor(1)
	v.moveCursor(1) // B-1 at 4
	v.setRows(twoLanes()[:3])
	if r, ok := v.selected(); !ok || r.Issue.Key != "A-2" {
		t.Errorf("selected = %+v, want the nearest row, A-2", r)
	}
}

func TestSelectKeyReportsMissing(t *testing.T) {
	v := newTestView(twoLanes(), 10)
	if v.selectKey("nope") {
		t.Error("selectKey must report false for a missing key")
	}
	if !v.selectKey("B-2") || v.cursor != 6 {
		t.Errorf("selectKey(B-2): cursor = %d", v.cursor)
	}
}

func TestHitTest(t *testing.T) {
	v := newTestView(twoLanes(), 4)
	cases := map[int]int{0: -1 /* heading */, 1: 1, 2: 2, 3: -1 /* heading */, 4: -1 /* outside the window */, -1: -1}
	for y, want := range cases {
		if got := v.hitTest(y); got != want {
			t.Errorf("hitTest(%d) = %d, want %d", y, got, want)
		}
	}
	v.offset = 3
	if got := v.hitTest(1); got != 4 {
		t.Errorf("hitTest with an offset = %d, want 4", got)
	}
	if got := v.hitTest(2); got != -1 {
		t.Errorf("hitTest on a note = %d, want -1", got)
	}
	v.offset = 5
	v.height = 5
	if got := v.hitTest(4); got != -1 {
		t.Errorf("hitTest below the last row = %d, want -1", got)
	}
}

func TestWheelScrollsByThreeAndKeepsTheCursorOnScreen(t *testing.T) {
	var rows []row
	for i := 0; i < 30; i++ {
		rows = append(rows, row{Kind: rowIssue, Issue: ticketStatus{Key: "K-" + string(rune('a'+i))}})
	}
	v := newTestView(rows, 5)
	v.scrollBy(wheelStep)
	if v.offset != 3 {
		t.Fatalf("offset = %d, want 3", v.offset)
	}
	if v.cursor < v.offset || v.cursor >= v.offset+v.height {
		t.Errorf("cursor %d left the window [%d,%d)", v.cursor, v.offset, v.offset+v.height)
	}
	v.scrollBy(1000)
	if v.offset != 25 {
		t.Errorf("offset = %d, want it clamped to 25", v.offset)
	}
	if v.cursor < v.offset {
		t.Errorf("cursor %d above the window", v.cursor)
	}
	v.scrollBy(-1000)
	if v.offset != 0 || v.cursor >= v.height {
		t.Errorf("after scrolling back: offset %d cursor %d", v.offset, v.cursor)
	}
}

func TestSetHeightNeverGoesBelowOne(t *testing.T) {
	v := newTestView(twoLanes(), 5)
	v.setHeight(-3)
	if v.height != 1 {
		t.Errorf("height = %d, want 1", v.height)
	}
}

func TestRenderFitsTheWindow(t *testing.T) {
	v := newTestView(twoLanes(), 4)
	for _, width := range []int{20, 40, 80} {
		out := v.render(width)
		if h := lipgloss.Height(out); h > 4 {
			t.Errorf("width %d: %d lines, want <= 4", width, h)
		}
		if w := lipgloss.Width(out); w > width {
			t.Errorf("width %d: rendered %d wide", width, w)
		}
	}
}

func TestRenderShowsPointerIndentGlyphStarAndStatus(t *testing.T) {
	v := newTestView(twoLanes(), 10)
	out := v.render(80)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[1], "▸ ") {
		t.Errorf("selected line %q lacks the pointer", lines[1])
	}
	if !strings.HasPrefix(lines[2], "  ") || strings.HasPrefix(lines[2], "▸") {
		t.Errorf("unselected line %q should be padded", lines[2])
	}
	if !strings.Contains(lines[0], "One (2)") {
		t.Errorf("heading line %q lacks the lane and count", lines[0])
	}
	if !strings.Contains(lines[1], "Open") {
		t.Errorf("line %q lacks the status", lines[1])
	}
	if !strings.Contains(lines[4], "★") || !strings.Contains(lines[4], "◆") {
		t.Errorf("epic line %q lacks the star and epic glyph", lines[4])
	}
	if !strings.Contains(lines[6], "    ") || !strings.Contains(lines[5], "a note") {
		t.Errorf("sub-task %q should be indented, note %q shown", lines[6], lines[5])
	}
}

func TestRenderDropsTheStatusWhenTheLineIsTooNarrow(t *testing.T) {
	v := newTestView(twoLanes(), 10)
	out := v.render(18)
	if strings.Contains(out, "Open") {
		t.Errorf("status should be dropped at width 18:\n%s", out)
	}
}

func TestScrollByWithNothingSelectableLeavesTheCursorAlone(t *testing.T) {
	v := newTestView(twoLanes(), 2)
	// Scroll so the window shows only the note-free tail, then back past the cursor.
	v.cursor = 1
	v.offset = 0
	rows := []row{{Kind: rowIssue, Issue: ticketStatus{Key: "A"}}, {Kind: rowHeading}, {Kind: rowHeading}, {Kind: rowHeading}, {Kind: rowHeading}}
	v.setRows(rows)
	v.setHeight(2)
	v.scrollBy(3) // the window now holds only headings
	if v.cursor != 0 {
		t.Errorf("cursor = %d, want it left on the only selectable row", v.cursor)
	}
}
