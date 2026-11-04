package main

import (
	"strconv"
	"strings"
)

// tab is one of the popup's two horizontal tabs.
type tab int

const (
	tabIssues tab = iota
	tabEpics
)

func (t tab) String() string {
	if t == tabEpics {
		return "Epics"
	}
	return "Issues"
}

// searchScope returns the scope a remote search uses on this tab.
func (t tab) searchScope() searchScope {
	if t == tabEpics {
		return scopeEpics
	}
	return scopeIssues
}

type rowKind int

const (
	rowHeading rowKind = iota // lane name and count; never selectable
	rowIssue
	rowSubtask
	rowEpic
	rowNote // a dim explanatory line (empty lane, search error); never selectable
)

// selectable reports whether the cursor may rest on a row of this kind.
func (k rowKind) selectable() bool {
	return k == rowIssue || k == rowSubtask || k == rowEpic
}

// Lane names. Headings carry these; the count is added when rendering.
const (
	laneAssigned = "Assigned to me"
	laneStarred  = "Starred"
	laneResults  = "Search results"
)

// row is one line of the flattened list.
type row struct {
	Kind    rowKind
	Issue   ticketStatus // zero for headings and notes
	Depth   int          // 0, or 1 for a sub-task under its parent
	Lane    string       // headings: lane name without the count
	Count   int          // headings only
	More    bool         // headings only: the lane hit its page cap, render "50+"
	Starred bool         // epics only: the epic is starred
	Text    string       // notes only
}

// laneData is everything buildRows needs, as fetched.
type laneData struct {
	Issues, Subtasks, Epics []ticketStatus
	Starred                 []ticketStatus  // as Jira returned them; may include Done, which buildRows drops
	StarredKeys             map[string]bool // every starred key, including ones Jira did not return
	IssuesMore              bool
	SubtasksMore            bool
	EpicsMore               bool
}

// searchHits are remote search results. nil means no answer yet for the
// current query; an empty non-nil slice means the answer was "nothing".
type searchHits []ticketStatus

// Empty-lane lines.
const (
	noteNoIssues  = "No open issues assigned to you."
	noteNoEpics   = "No epics assigned to you."
	noteNoStarred = "No starred epics. Press s on an epic to star it."
	noteNoMatches = "No matches."
)

func isDone(t ticketStatus) bool { return t.StatusCategory == "done" }

func matchesFilter(t ticketStatus, filter string) bool {
	if filter == "" {
		return true
	}
	f := strings.ToLower(filter)
	return strings.Contains(strings.ToLower(t.Key), f) || strings.Contains(strings.ToLower(t.Summary), f)
}

func filterStatuses(in []ticketStatus, filter string) []ticketStatus {
	out := make([]ticketStatus, 0, len(in))
	for _, t := range in {
		if matchesFilter(t, filter) {
			out = append(out, t)
		}
	}
	return out
}

// buildRows turns fetched data into the ordered rows the list view shows. It
// does no I/O and no rendering, so lane order, de-duplication, counts and
// filtering are testable without a terminal.
func buildRows(t tab, d laneData, filter string, hits searchHits) []row {
	var rows []row
	shown := map[string]bool{}
	note := func(s string) { rows = append(rows, row{Kind: rowNote, Text: s}) }

	// addLane appends a heading and its body. With a filter active an empty
	// lane is dropped; otherwise it shows a count of 0 and a one-line reason.
	addLane := func(lane string, count int, more bool, body []row, empty string) {
		if len(body) == 0 && filter != "" {
			return
		}
		rows = append(rows, row{Kind: rowHeading, Lane: lane, Count: count, More: more && filter == ""})
		if len(body) == 0 {
			note(empty)
			return
		}
		rows = append(rows, body...)
		for _, r := range body {
			shown[r.Issue.Key] = true
		}
	}

	switch t {
	case tabIssues:
		body := issueRows(d.Issues, d.Subtasks, filter)
		addLane(laneAssigned, len(body), d.IssuesMore || d.SubtasksMore, body, noteNoIssues)
	case tabEpics:
		assigned := map[string]bool{}
		var mine []row
		for _, e := range filterStatuses(d.Epics, filter) {
			mine = append(mine, row{Kind: rowEpic, Issue: e, Starred: d.StarredKeys[e.Key]})
		}
		for _, e := range d.Epics {
			assigned[e.Key] = true
		}
		addLane(laneAssigned, len(mine), d.EpicsMore, mine, noteNoEpics)

		var starred []row
		for _, e := range filterStatuses(d.Starred, filter) {
			if isDone(e) || assigned[e.Key] {
				continue
			}
			starred = append(starred, row{Kind: rowEpic, Issue: e, Starred: true})
		}
		addLane(laneStarred, len(starred), false, starred, noteNoStarred)
	}

	if hits != nil {
		var body []row
		for _, h := range hits {
			if isDone(h) || shown[h.Key] {
				continue
			}
			kind := rowIssue
			switch {
			case t == tabEpics:
				kind = rowEpic
			case h.HierarchyLevel < 0:
				kind = rowSubtask
			}
			body = append(body, row{Kind: kind, Issue: h, Starred: kind == rowEpic && d.StarredKeys[h.Key]})
		}
		// The results lane is a remote answer, so it keeps its heading even
		// under a filter (the filter is the query that produced it).
		rows = append(rows, row{Kind: rowHeading, Lane: laneResults, Count: len(body)})
		if len(body) == 0 {
			note(noteNoMatches)
		}
		rows = append(rows, body...)
	}
	return rows
}

// issueRows orders the Issues lane: each top-level issue followed by its
// sub-tasks (depth 1), then sub-tasks whose parent is not displayed, at depth 0.
func issueRows(issues, subtasks []ticketStatus, filter string) []row {
	top := filterStatuses(issues, filter)
	subs := filterStatuses(subtasks, filter)
	inTop := map[string]bool{}
	for _, t := range top {
		inTop[t.Key] = true
	}
	children := map[string][]ticketStatus{}
	var orphans []ticketStatus
	for _, s := range subs {
		if s.ParentKey != "" && inTop[s.ParentKey] {
			children[s.ParentKey] = append(children[s.ParentKey], s)
		} else {
			orphans = append(orphans, s)
		}
	}
	var out []row
	for _, t := range top {
		out = append(out, row{Kind: rowIssue, Issue: t})
		for _, c := range children[t.Key] {
			out = append(out, row{Kind: rowSubtask, Issue: c, Depth: 1})
		}
	}
	for _, o := range orphans {
		out = append(out, row{Kind: rowSubtask, Issue: o})
	}
	return out
}

// headingText is a heading's rendered text: "Lane (3)", or "Lane (50+)" when
// the lane hit its cap.
func headingText(r row) string {
	n := strconv.Itoa(r.Count)
	if r.More {
		n += "+"
	}
	return r.Lane + " (" + n + ")"
}
