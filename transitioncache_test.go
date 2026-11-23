package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

var (
	toDoMoves       = []transitionOption{{ID: "21", Name: "Start", ToStatusName: "In Progress", ToStatusCategory: "indeterminate"}}
	inProgressMoves = []transitionOption{{ID: "31", Name: "Done", ToStatusName: "Done", ToStatusCategory: "done"}}
)

// sourceAt is a fake whose issue reads back as type and status, offering moves.
func sourceAt(key, issueType, status string, moves []transitionOption) *fakeJiraSource {
	return &fakeJiraSource{
		getResult:       issueDetail{Key: key, IssueType: issueType, StatusName: status},
		transitionsList: moves,
	}
}

func mustTransitions(t *testing.T, src jiraSource, key, issueType, status string, refresh bool) []transitionOption {
	t.Helper()
	got, err := transitionsFor(context.Background(), src, key, issueType, status, refresh)
	if err != nil {
		t.Fatalf("transitionsFor %s: %v", key, err)
	}
	return got
}

func TestTransitionsForServesAStepFromTheCache(t *testing.T) {
	withAPIState(t)
	src := sourceAt("PROJ-1", "Task", "To Do", toDoMoves)
	mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	if src.transitionsCalls != 1 {
		t.Fatalf("a miss asks Jira once, got %d calls", src.transitionsCalls)
	}
	// Another issue at the same step: same project, type and status.
	got := mustTransitions(t, src, "PROJ-2", "task", "TO DO", false)
	if src.transitionsCalls != 1 {
		t.Fatalf("a hit must not ask Jira, got %d calls", src.transitionsCalls)
	}
	if len(got) != 1 || got[0].ID != "21" {
		t.Fatalf("cached list = %+v", got)
	}
	for _, other := range [][3]string{{"OTHER-1", "Task", "To Do"}, {"PROJ-3", "Bug", "To Do"}, {"PROJ-4", "Task", "In Progress"}} {
		before := src.transitionsCalls
		mustTransitions(t, src, other[0], other[1], other[2], false)
		if src.transitionsCalls != before+1 {
			t.Errorf("%v is another step and must ask Jira", other)
		}
	}
}

func TestTransitionsForFilesUnderTheStepJiraReports(t *testing.T) {
	withAPIState(t)
	// The caller's row says To Do, but the issue has moved on.
	src := sourceAt("PROJ-1", "Task", "In Progress", inProgressMoves)
	got := mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	if len(got) != 1 || got[0].ID != "31" {
		t.Fatalf("the reply is Jira's list: %+v", got)
	}
	src2 := sourceAt("PROJ-2", "Task", "To Do", toDoMoves)
	if got := mustTransitions(t, src2, "PROJ-2", "Task", "To Do", false); src2.transitionsCalls != 1 || got[0].ID != "21" {
		t.Fatalf("a list must not be filed under a stale status: calls %d, got %+v", src2.transitionsCalls, got)
	}
	src3 := sourceAt("PROJ-3", "Task", "In Progress", nil)
	if got := mustTransitions(t, src3, "PROJ-3", "Task", "In Progress", false); src3.transitionsCalls != 0 || got[0].ID != "31" {
		t.Fatalf("filed under the step Jira reported: calls %d, got %+v", src3.transitionsCalls, got)
	}
}

func TestTransitionsForWithoutHintsStillFillsTheCache(t *testing.T) {
	withAPIState(t)
	src := sourceAt("PROJ-1", "Task", "To Do", toDoMoves)
	mustTransitions(t, src, "PROJ-1", "", "", false)
	mustTransitions(t, src, "PROJ-1", "", "", false)
	if src.transitionsCalls != 2 {
		t.Fatalf("without a step there is nothing to look up: %d calls", src.transitionsCalls)
	}
	mustTransitions(t, src, "PROJ-2", "Task", "To Do", false)
	if src.transitionsCalls != 2 {
		t.Fatalf("an unhinted fetch is still kept for the next hinted one: %d calls", src.transitionsCalls)
	}
}

func TestTransitionsForRefreshAndExpiry(t *testing.T) {
	withAPIState(t)
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	origNow := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = origNow })

	src := sourceAt("PROJ-1", "Task", "To Do", toDoMoves)
	mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	mustTransitions(t, src, "PROJ-1", "Task", "To Do", true)
	if src.transitionsCalls != 2 {
		t.Fatalf("refresh must ask Jira: %d calls", src.transitionsCalls)
	}
	now = now.Add(transitionsTTL - time.Minute)
	mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	if src.transitionsCalls != 2 {
		t.Fatalf("a list inside the TTL is served: %d calls", src.transitionsCalls)
	}
	now = now.Add(2 * time.Minute)
	mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	if src.transitionsCalls != 3 {
		t.Fatalf("an expired list is refetched: %d calls", src.transitionsCalls)
	}
}

func TestTransitionsForSkipsEmptyListsFailuresAndOtherSites(t *testing.T) {
	withAPIState(t)
	empty := sourceAt("PROJ-1", "Task", "To Do", []transitionOption{})
	mustTransitions(t, empty, "PROJ-1", "Task", "To Do", false)
	mustTransitions(t, empty, "PROJ-1", "Task", "To Do", false)
	if empty.transitionsCalls != 2 {
		t.Fatalf("an empty list (likely a permission on this issue) must not be kept: %d calls", empty.transitionsCalls)
	}

	failing := sourceAt("PROJ-1", "Task", "To Do", nil)
	failing.transitionsErr = errors.New("boom")
	if _, err := transitionsFor(context.Background(), failing, "PROJ-1", "Task", "To Do", false); err == nil {
		t.Fatal("a failed fetch is an error")
	}
	unread := sourceAt("PROJ-1", "", "", toDoMoves)
	unread.getErr = errors.New("get failed")
	if got := mustTransitions(t, unread, "PROJ-1", "Task", "To Do", false); len(got) != 1 {
		t.Fatalf("the list is still answered when the issue can't be read: %+v", got)
	}
	if _, err := os.Stat(transitionsCachePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("nothing should have been kept yet: %v", err)
	}

	src := sourceAt("PROJ-1", "Task", "To Do", toDoMoves)
	mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	cloudIDFn = func() string { return "cloud-2" }
	mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	if src.transitionsCalls != 2 {
		t.Fatalf("another site's lists must not be served: %d calls", src.transitionsCalls)
	}
}

func TestRefusedTransitionForgetsTheProjectsLists(t *testing.T) {
	withAPIState(t)
	mustTransitions(t, sourceAt("PROJ-1", "Task", "To Do", toDoMoves), "PROJ-1", "Task", "To Do", false)
	mustTransitions(t, sourceAt("PROJ-2", "Bug", "In Progress", inProgressMoves), "PROJ-2", "Bug", "In Progress", false)
	mustTransitions(t, sourceAt("OTHER-1", "Task", "To Do", toDoMoves), "OTHER-1", "Task", "To Do", false)

	ok := &fakeJiraSource{}
	if err := transitionIssue(context.Background(), ok, "PROJ-1", "21"); err != nil {
		t.Fatal(err)
	}
	if n := len(readTransitionsCache("cloud-1").Steps); n != 3 {
		t.Fatalf("a transition that worked keeps the lists: %d", n)
	}
	refused := &fakeJiraSource{transitionErr: errors.New("transition 21 is not valid for this issue")}
	if err := transitionIssue(context.Background(), refused, "PROJ-1", "21"); err == nil {
		t.Fatal("the refusal is returned")
	}
	steps := readTransitionsCache("cloud-1").Steps
	if len(steps) != 1 {
		t.Fatalf("PROJ's lists are dropped, OTHER's kept: %v", steps)
	}
	if _, ok := steps[workflowStep("OTHER-1", "Task", "To Do")]; !ok {
		t.Fatalf("OTHER's list is kept: %v", steps)
	}
}

func TestWorkflowStep(t *testing.T) {
	if workflowStep("proj-12", "Task", "In Progress") != workflowStep("PROJ-9", "task", "in progress") {
		t.Error("issues of one project, type and status share a step")
	}
	for _, c := range [][3]string{{"PROJ-1", "", "To Do"}, {"PROJ-1", "Task", ""}, {"nokey", "Task", "To Do"}} {
		if workflowStep(c[0], c[1], c[2]) != "" {
			t.Errorf("%v has no step", c)
		}
	}
}

func TestAPITransitionsUsesTheCache(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.getResult = issueDetail{Key: "PROJ-1", IssueType: "Task", StatusName: "To Do"}
	src.transitionsList = toDoMoves
	var got struct {
		Key         string           `json:"key"`
		Transitions []map[string]any `json:"transitions"`
	}
	callAPI(t, src, "transitions", "PROJ-1", "--type=Task", "--status=To Do").into(t, &got)
	callAPI(t, src, "transitions", "proj-2", "--type", "Task", "--status", "To Do").into(t, &got)
	if src.transitionsCalls != 1 {
		t.Fatalf("the second issue at that step is answered from the cache: %d calls", src.transitionsCalls)
	}
	if got.Key != "PROJ-2" || len(got.Transitions) != 1 || got.Transitions[0]["id"] != "21" {
		t.Fatalf("reply: %+v", got)
	}
	callAPI(t, src, "transitions", "PROJ-2", "--type=Task", "--status=To Do", "--refresh").into(t, &got)
	if src.transitionsCalls != 2 {
		t.Fatalf("--refresh asks Jira: %d calls", src.transitionsCalls)
	}

	src.transitionErr = errors.New("not valid")
	if r := callAPI(t, src, "transition", "PROJ-2", "21"); r.OK {
		t.Fatal("a refused transition is an error")
	}
	if n := len(readTransitionsCache("cloud-1").Steps); n != 0 {
		t.Fatalf("a refused transition forgets the project's lists: %d left", n)
	}

	mustTransitions(t, src, "PROJ-1", "Task", "To Do", false)
	if err := saveCredentials(credentials{ClientID: "c", RefreshToken: "r", CloudID: "cloud-1"}); err != nil {
		t.Fatal(err)
	}
	callAPI(t, src, "logout")
	if _, err := os.Stat(transitionsCachePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("logout must remove the transitions cache: %v", err)
	}
}
