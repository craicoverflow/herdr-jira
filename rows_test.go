package main

import (
	"reflect"
	"testing"
)

func st(key, summary, cat string) ticketStatus {
	return ticketStatus{Key: key, Summary: summary, StatusCategory: cat}
}

func sub(key, parent string) ticketStatus {
	return ticketStatus{Key: key, Summary: "sub " + key, ParentKey: parent, HierarchyLevel: -1}
}

// shape renders rows as short strings for comparison: "# Lane (n)" for a
// heading, "  - text" for a note, "KEY" / "  KEY" for rows by depth.
func shape(rows []row) []string {
	var out []string
	for _, r := range rows {
		switch r.Kind {
		case rowHeading:
			out = append(out, "# "+headingText(r))
		case rowNote:
			out = append(out, "- "+r.Text)
		default:
			s := r.Issue.Key
			if r.Depth > 0 {
				s = "  " + s
			}
			if r.Starred {
				s += "*"
			}
			out = append(out, s)
		}
	}
	return out
}

func expect(t *testing.T, got []row, want ...string) {
	t.Helper()
	if g := shape(got); !reflect.DeepEqual(g, want) {
		t.Errorf("rows\n got  %q\n want %q", g, want)
	}
}

func TestIssuesLaneGroupsSubtasksUnderParents(t *testing.T) {
	d := laneData{
		Issues:   []ticketStatus{st("A-1", "one", "new"), st("A-2", "two", "new")},
		Subtasks: []ticketStatus{sub("S-1", "A-2"), sub("S-2", "A-1"), sub("S-3", "A-2")},
	}
	expect(t, buildRows(tabIssues, d, "", nil),
		"# Assigned to me (5)", "A-1", "  S-2", "A-2", "  S-1", "  S-3")
}

func TestOrphanSubtaskGoesAtTopLevelAfterOtherIssues(t *testing.T) {
	d := laneData{
		Issues:   []ticketStatus{st("A-1", "one", "new"), st("A-2", "two", "new")},
		Subtasks: []ticketStatus{sub("S-1", "GONE-9"), sub("S-2", "A-1")},
	}
	expect(t, buildRows(tabIssues, d, "", nil),
		"# Assigned to me (4)", "A-1", "  S-2", "A-2", "S-1")
}

func TestMatchingSubtaskUnderNonMatchingParentShowsAtTopLevel(t *testing.T) {
	d := laneData{
		Issues:   []ticketStatus{st("A-1", "alpha", "new")},
		Subtasks: []ticketStatus{{Key: "S-1", Summary: "beta work", ParentKey: "A-1", HierarchyLevel: -1}},
	}
	rows := buildRows(tabIssues, d, "beta", nil)
	expect(t, rows, "# Assigned to me (1)", "S-1")
	if rows[1].Kind != rowSubtask || rows[1].Depth != 0 {
		t.Errorf("row = %+v, want a depth-0 sub-task", rows[1])
	}
}

func TestMatchingParentKeepsItsMatchingSubtasksIndented(t *testing.T) {
	d := laneData{
		Issues:   []ticketStatus{st("A-1", "widget", "new")},
		Subtasks: []ticketStatus{{Key: "S-1", Summary: "widget part", ParentKey: "A-1"}, {Key: "S-2", Summary: "other", ParentKey: "A-1"}},
	}
	expect(t, buildRows(tabIssues, d, "widget", nil), "# Assigned to me (2)", "A-1", "  S-1")
}

func TestEmptyIssuesLaneExplainsItselfWithoutAFilter(t *testing.T) {
	expect(t, buildRows(tabIssues, laneData{}, "", nil),
		"# Assigned to me (0)", "- "+noteNoIssues)
}

func TestEmptyLaneHasNoHeadingUnderAFilter(t *testing.T) {
	d := laneData{Issues: []ticketStatus{st("A-1", "one", "new")}}
	expect(t, buildRows(tabIssues, d, "zzz", nil))
}

func TestFilterMatchesKeyAndSummaryCaseInsensitively(t *testing.T) {
	d := laneData{Issues: []ticketStatus{st("PROJ-12", "Fix the Widget", "new"), st("PROJ-13", "other", "new")}}
	expect(t, buildRows(tabIssues, d, "widget", nil), "# Assigned to me (1)", "PROJ-12")
	expect(t, buildRows(tabIssues, d, "proj-13", nil), "# Assigned to me (1)", "PROJ-13")
}

func TestEpicsLaneOrderAndAssignedAndStarredShowsOnce(t *testing.T) {
	d := laneData{
		Epics:       []ticketStatus{st("E-1", "mine", "new"), st("E-2", "also starred", "new")},
		Starred:     []ticketStatus{st("E-2", "also starred", "new"), st("E-3", "team", "indeterminate")},
		StarredKeys: map[string]bool{"E-2": true, "E-3": true},
	}
	expect(t, buildRows(tabEpics, d, "", nil),
		"# Assigned to me (2)", "E-1", "E-2*",
		"# Starred (1)", "E-3*")
}

func TestStarredDoneEpicIsHiddenAndStarredKeyJiraDidNotReturnIsHidden(t *testing.T) {
	d := laneData{
		Epics:       []ticketStatus{st("E-1", "mine", "new")},
		Starred:     []ticketStatus{st("E-9", "finished", "done")},
		StarredKeys: map[string]bool{"E-9": true, "E-8": true}, // E-8: deleted or no access
	}
	expect(t, buildRows(tabEpics, d, "", nil),
		"# Assigned to me (1)", "E-1",
		"# Starred (0)", "- "+noteNoStarred)
}

func TestEmptyEpicLanesShowTheirReasons(t *testing.T) {
	expect(t, buildRows(tabEpics, laneData{}, "", nil),
		"# Assigned to me (0)", "- "+noteNoEpics, "# Starred (0)", "- "+noteNoStarred)
}

func TestCountsUnderAFilterAreTheFilteredNumber(t *testing.T) {
	d := laneData{Epics: []ticketStatus{st("E-1", "alpha", "new"), st("E-2", "beta", "new"), st("E-3", "alpha two", "new")}}
	rows := buildRows(tabEpics, d, "alpha", nil)
	if rows[0].Count != 2 {
		t.Errorf("count = %d, want 2", rows[0].Count)
	}
}

func TestCapShowsFiftyPlusOnlyWithoutAFilter(t *testing.T) {
	d := laneData{Issues: []ticketStatus{st("A-1", "x", "new")}, SubtasksMore: true}
	if got := buildRows(tabIssues, d, "", nil)[0]; headingText(got) != "Assigned to me (1+)" {
		t.Errorf("heading = %q, want a + when either query hit its cap", headingText(got))
	}
	if got := buildRows(tabIssues, d, "x", nil)[0]; headingText(got) != "Assigned to me (1)" {
		t.Errorf("heading under a filter = %q, want no +", headingText(got))
	}
	d = laneData{Epics: []ticketStatus{st("E-1", "x", "new")}, EpicsMore: true}
	if got := buildRows(tabEpics, d, "", nil)[0]; !got.More {
		t.Error("the Epics lane must carry the cap flag")
	}
}

func TestSearchHitsFormTheLastLaneWithoutRepeatingLaneRows(t *testing.T) {
	d := laneData{Issues: []ticketStatus{st("A-1", "widget", "new")}}
	hits := searchHits{st("A-1", "widget", "new"), st("B-1", "widget too", "new"), st("B-2", "done one", "done")}
	expect(t, buildRows(tabIssues, d, "widget", hits),
		"# Assigned to me (1)", "A-1", "# Search results (1)", "B-1")
}

func TestSearchHitsOnEpicsTabAreEpicRowsAndShowTheirStar(t *testing.T) {
	d := laneData{StarredKeys: map[string]bool{"E-5": true}}
	rows := buildRows(tabEpics, d, "e", searchHits{st("E-5", "e five", "new")})
	last := rows[len(rows)-1]
	if last.Kind != rowEpic || !last.Starred {
		t.Errorf("hit row = %+v, want a starred epic row", last)
	}
}

func TestSearchHitsSubtaskKind(t *testing.T) {
	rows := buildRows(tabIssues, laneData{}, "s", searchHits{sub("S-1", "A-1")})
	if last := rows[len(rows)-1]; last.Kind != rowSubtask {
		t.Errorf("hit = %+v, want a sub-task row", last)
	}
}

func TestResultsLaneWithNoMatchesSaysSo(t *testing.T) {
	expect(t, buildRows(tabIssues, laneData{}, "zz", searchHits{}),
		"# Search results (0)", "- "+noteNoMatches)
}

func TestNoResultsLaneUntilAnAnswerArrives(t *testing.T) {
	rows := buildRows(tabIssues, laneData{Issues: []ticketStatus{st("A-1", "zz", "new")}}, "zz", nil)
	for _, r := range rows {
		if r.Kind == rowHeading && r.Lane == laneResults {
			t.Error("the Results lane must not show before a response arrives")
		}
	}
}

func TestRowKindSelectable(t *testing.T) {
	for k, want := range map[rowKind]bool{rowHeading: false, rowNote: false, rowIssue: true, rowSubtask: true, rowEpic: true} {
		if k.selectable() != want {
			t.Errorf("kind %v selectable = %v, want %v", k, k.selectable(), want)
		}
	}
}

func TestTabHelpers(t *testing.T) {
	if tabIssues.String() != "Issues" || tabEpics.String() != "Epics" {
		t.Error("tab names")
	}
	if tabIssues.searchScope() != scopeIssues || tabEpics.searchScope() != scopeEpics {
		t.Error("tab search scopes")
	}
}
