package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newTestPickerModel(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	withTempStateDir(t)
	withTempConfigDir(t)
	return newPickerModel(src)
}

// keyMsg builds the tea.KeyMsg for a key name as the hotkey table spells it.
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+f":
		return tea.KeyMsg{Type: tea.KeyCtrlF}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

// press sends one key and returns the updated model and the command.
func press(m pickerModel, key string) (pickerModel, tea.Cmd) {
	next, cmd := m.Update(keyMsg(key))
	return next.(pickerModel), cmd
}

func send(m pickerModel, msg tea.Msg) (pickerModel, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(pickerModel), cmd
}

func sized(m pickerModel) pickerModel {
	m, _ = send(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

func TestPickerModelLoadsIssuesOnSuccess(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	issues := []ticketStatus{{Key: "PROJ-1", StatusName: "Open", Summary: "Widget"}}
	pm, _ := send(m, issuesLoadedMsg{issues: issues})
	if pm.phase != phaseReady {
		t.Fatalf("phase = %v, want phaseReady", pm.phase)
	}
	r, ok := pm.view.selected()
	if !ok || r.Issue.Key != "PROJ-1" {
		t.Fatalf("selected = %+v, %v, want PROJ-1", r, ok)
	}
}

func TestPickerModelShowsErrorOnLoadFailure(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	pm, _ := send(m, issuesLoadedMsg{err: errSignedOut})
	if pm.phase != phaseError {
		t.Fatalf("phase = %v, want phaseError", pm.phase)
	}
	if pm.err != errSignedOut {
		t.Errorf("err = %v, want errSignedOut", pm.err)
	}
}

func TestPickerModelRetryFromErrorReloads(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	pm, _ := send(m, issuesLoadedMsg{err: errSignedOut})
	pm, cmd := press(pm, "r")
	if pm.phase != phaseLoading {
		t.Fatalf("phase after retry = %v, want phaseLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("retry must issue a command to reload")
	}
}

// readyWithOneIssue builds a model on the Issues tab with one row selected.
func readyWithOneIssue(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	m := sized(newTestPickerModel(t, src))
	pm, _ := send(m, issuesLoadedMsg{issues: []ticketStatus{{Key: "PROJ-1", StatusName: "Open", Summary: "Widget", IssueType: "Task"}}})
	return pm
}

func TestPickerModelEnterLoadsDetail(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})

	pm, cmd := press(pm, "enter")
	if pm.phase != phaseDetailLoading {
		t.Fatalf("phase after enter = %v, want phaseDetailLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("enter must issue a command to load the detail")
	}
	if pm.detailReturnPhase != phaseReady {
		t.Errorf("detailReturnPhase = %v, want phaseReady", pm.detailReturnPhase)
	}

	pm, _ = send(pm, detailLoadedMsg{detail: issueDetail{Key: "PROJ-1", StatusName: "Open", Summary: "Widget"}})
	if pm.phase != phaseDetail {
		t.Fatalf("phase after detail loaded = %v, want phaseDetail", pm.phase)
	}
	if pm.detail == nil || pm.detail.Key != "PROJ-1" {
		t.Errorf("detail = %+v, want PROJ-1", pm.detail)
	}
}

func TestPickerModelEscFromDetailReturnsToList(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, _ = press(pm, "enter")
	pm, _ = send(pm, detailLoadedMsg{detail: issueDetail{Key: "PROJ-1"}})

	pm, _ = press(pm, "esc")
	if pm.phase != phaseReady {
		t.Fatalf("phase after esc from detail = %v, want phaseReady", pm.phase)
	}
	if pm.detail != nil {
		t.Error("detail should be cleared after returning to the list")
	}
}

func TestPickerModelBDoesNotGoBackFromDetail(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, _ = press(pm, "enter")
	pm, _ = send(pm, detailLoadedMsg{detail: issueDetail{Key: "PROJ-1"}})

	pm, _ = press(pm, "b")
	if pm.phase != phaseDetail {
		t.Fatalf("phase after b on the detail screen = %v, want phaseDetail (esc is the only back key)", pm.phase)
	}
}

func TestPickerModelEscFromListQuits(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, cmd := press(pm, "esc")
	if !pm.quitting {
		t.Error("expected quitting=true after esc on the list")
	}
	if cmd == nil {
		t.Fatal("esc must issue tea.Quit")
	}
}

func TestPickerModelQQuits(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, cmd := press(pm, "q")
	if !pm.quitting || cmd == nil {
		t.Error("q must close the popup")
	}
}

func TestPickerModelSignInFailureShowsError(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	pm, _ := send(m, signedInMsg{err: errSignedOut})
	if pm.phase != phaseError {
		t.Fatalf("phase = %v, want phaseError", pm.phase)
	}
}

// detailReady returns a model already showing PROJ-1's detail screen.
func detailReady(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	pm := readyWithOneIssue(t, src)
	pm, _ = press(pm, "enter")
	pm, _ = send(pm, detailLoadedMsg{detail: issueDetail{Key: "PROJ-1", StatusName: "Open"}})
	return pm
}

func TestPickerModelTKeyOnDetailLoadsTransitionsLive(t *testing.T) {
	src := &fakeJiraSource{transitionsList: []transitionOption{
		{ID: "11", Name: "Start Progress", ToStatusName: "In Progress"},
		{ID: "21", Name: "Close", ToStatusName: "Closed"},
	}}
	pm := detailReady(t, src)

	pm, cmd := press(pm, "t")
	if pm.phase != phaseTransitionsLoading {
		t.Fatalf("phase after t = %v, want phaseTransitionsLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("t must issue a command to fetch transitions live")
	}
	pm, _ = send(pm, transitionsLoadedMsg{transitions: src.transitionsList})
	if pm.phase != phaseTransitions {
		t.Fatalf("phase after transitions loaded = %v, want phaseTransitions", pm.phase)
	}
	if len(pm.transitionList.Items()) != 2 {
		t.Fatalf("transition list has %d items, want 2", len(pm.transitionList.Items()))
	}
}

func TestPickerModelTKeyOnListLoadsDetailThenTransitions(t *testing.T) {
	src := &fakeJiraSource{}
	pm := readyWithOneIssue(t, src)

	pm, cmd := press(pm, "t")
	if pm.phase != phaseDetailLoading || cmd == nil {
		t.Fatalf("phase after t on a row = %v (cmd nil: %v), want phaseDetailLoading with a command", pm.phase, cmd == nil)
	}
	pm, cmd = send(pm, detailLoadedMsg{detail: issueDetail{Key: "PROJ-1"}})
	if pm.phase != phaseTransitionsLoading || cmd == nil {
		t.Fatalf("phase after the detail loaded = %v, want phaseTransitionsLoading", pm.phase)
	}
	pm, _ = send(pm, transitionsLoadedMsg{transitions: []transitionOption{{ID: "21", Name: "Close"}}})
	pm, _ = press(pm, "esc")
	if pm.phase != phaseDetail {
		t.Fatalf("esc from the transition list = %v, want phaseDetail", pm.phase)
	}
	pm, _ = press(pm, "esc")
	if pm.phase != phaseReady {
		t.Fatalf("esc from the detail = %v, want phaseReady", pm.phase)
	}
}

func TestPickerModelSelectingTransitionAppliesIt(t *testing.T) {
	pm := detailReady(t, &fakeJiraSource{})
	pm, _ = press(pm, "t")
	pm, _ = send(pm, transitionsLoadedMsg{transitions: []transitionOption{{ID: "21", Name: "Close", ToStatusName: "Closed"}}})

	pm, cmd := press(pm, "enter")
	if pm.phase != phaseApplyingTransition {
		t.Fatalf("phase after selecting a transition = %v, want phaseApplyingTransition", pm.phase)
	}
	if cmd == nil {
		t.Fatal("selecting a transition must issue a command to apply it")
	}
	pm, _ = send(pm, transitionAppliedMsg{detail: issueDetail{Key: "PROJ-1", StatusName: "Closed"}})
	if pm.phase != phaseDetail || pm.detail.StatusName != "Closed" {
		t.Fatalf("after apply: phase %v detail %+v, want phaseDetail Closed", pm.phase, pm.detail)
	}
}

func TestPickerModelFailedTransitionShowsErrorAndKeepsIssueUnchanged(t *testing.T) {
	pm := detailReady(t, &fakeJiraSource{})
	originalDetail := pm.detail
	pm, _ = press(pm, "t")
	pm, _ = send(pm, transitionsLoadedMsg{transitions: []transitionOption{{ID: "21", Name: "Close"}}})
	pm, _ = press(pm, "enter")

	pm, _ = send(pm, transitionAppliedMsg{err: errSignedOut})
	if pm.phase != phaseError || pm.errReturnPhase != phaseDetail {
		t.Fatalf("phase %v returnPhase %v, want phaseError returning to phaseDetail", pm.phase, pm.errReturnPhase)
	}
	pm, _ = press(pm, "r")
	if pm.phase != phaseDetail {
		t.Fatalf("phase after retrying from a failed transition = %v, want phaseDetail", pm.phase)
	}
	if pm.detail != originalDetail {
		t.Error("the issue's detail must be unchanged after a failed transition")
	}
}

func TestPickerModelChangingAnIssueRefreshesTheTabOnReturn(t *testing.T) {
	pm := detailReady(t, &fakeJiraSource{})
	pm, _ = press(pm, "a")
	pm, _ = send(pm, assignAppliedMsg{detail: issueDetail{Key: "PROJ-1", AssigneeName: "Me"}})
	pm, cmd := press(pm, "esc")
	if pm.phase != phaseReady {
		t.Fatalf("phase = %v, want phaseReady", pm.phase)
	}
	if cmd == nil {
		t.Error("returning to the list after a change must reload the tab")
	}
}

// ---- tabs -----------------------------------------------------------------

func TestTabSwitchingByKeyLoadsEpicsOnce(t *testing.T) {
	src := &fakeJiraSource{}
	pm := readyWithOneIssue(t, src)
	if pm.tab != tabIssues {
		t.Fatal("precondition: starts on the Issues tab")
	}

	pm, cmd := press(pm, "2")
	if pm.tab != tabEpics || cmd == nil {
		t.Fatalf("after 2: tab %v, cmd nil %v; want Epics with a load command", pm.tab, cmd == nil)
	}
	pm, _ = send(pm, epicsLoadedMsg{epics: []ticketStatus{{Key: "PROJ-9", Summary: "Epic"}}})

	pm, _ = press(pm, "tab")
	if pm.tab != tabIssues {
		t.Errorf("tab key from Epics: tab = %v, want Issues", pm.tab)
	}
	pm, cmd = press(pm, "shift+tab")
	if pm.tab != tabEpics {
		t.Errorf("shift+tab from Issues: tab = %v, want Epics", pm.tab)
	}
	if cmd != nil {
		t.Error("returning to an already-loaded Epics tab must not reload it")
	}
	pm, _ = press(pm, "1")
	if pm.tab != tabIssues {
		t.Errorf("1: tab = %v, want Issues", pm.tab)
	}
}

func TestTabSwitchingByClick(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	x := tabSpans()[1][0] + 1
	pm, _ = send(pm, tea.MouseMsg{X: x, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if pm.tab != tabEpics {
		t.Fatalf("click on the Epics label: tab = %v, want Epics", pm.tab)
	}
	x = tabSpans()[0][0]
	pm, _ = send(pm, tea.MouseMsg{X: x, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if pm.tab != tabIssues {
		t.Fatalf("click on the Issues label: tab = %v, want Issues", pm.tab)
	}
	pm, _ = send(pm, tea.MouseMsg{X: 90, Y: 0, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if pm.tab != tabIssues {
		t.Error("a click beyond the labels must not switch tabs")
	}
}

func TestTabBarHitTestMatchesDrawing(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	bar := pm.tabBar()
	for i, name := range []string{"Issues", "Epics"} {
		span := tabSpans()[i]
		got := strings.TrimSpace(lipgloss.NewStyle().Render(bar))
		if !strings.Contains(got, name) {
			t.Fatalf("tab bar %q lacks %q", got, name)
		}
		idx := strings.Index(bar, name)
		if idx < span[0] || idx >= span[1] {
			t.Errorf("%s drawn at column %d, hit span %v", name, idx, span)
		}
	}
}

func TestOldKeysDoNothing(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	for _, key := range []string{"e", "ctrl+f"} {
		next, cmd := press(pm, key)
		if next.phase != phaseReady || next.mode != modeBrowse || next.tab != tabIssues || cmd != nil {
			t.Errorf("%q changed state: phase %v mode %v tab %v cmd nil %v", key, next.phase, next.mode, next.tab, cmd == nil)
		}
	}
}

// ---- epics and stars ------------------------------------------------------

// epicsReady returns a model on the Epics tab with one assigned epic.
func epicsReady(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	pm := readyWithOneIssue(t, src)
	pm, _ = press(pm, "2")
	pm, _ = send(pm, epicsLoadedMsg{
		epics:   []ticketStatus{{Key: "PROJ-3008", StatusName: "Backlog", Summary: "Tenant Access Vending", IssueType: "Epic"}},
		cloudID: "cloud-a",
	})
	return pm
}

func rowKeys(m pickerModel) []string {
	var out []string
	for _, r := range m.view.rows {
		if r.Kind.selectable() {
			out = append(out, r.Issue.Key)
		}
	}
	return out
}

func TestStarOnEpicStarsAndPersists(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	pm, _ = press(pm, "s")
	if got := keysOf(readStars("cloud-a")); len(got) != 1 || got[0] != "PROJ-3008" {
		t.Fatalf("stars on disk = %v, want [PROJ-3008]", got)
	}
	r, _ := pm.view.selected()
	if !r.Starred {
		t.Error("the row should show as starred")
	}
	if !strings.Contains(pm.status, "Starred") {
		t.Errorf("status = %q, want a Starred confirmation", pm.status)
	}

	pm, _ = press(pm, "s")
	if got := readStars("cloud-a"); len(got) != 0 {
		t.Errorf("second s should unstar, got %v", got)
	}
}

func TestStarOnIssueRowDoesNothing(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, cmd := press(pm, "s")
	if cmd != nil || pm.status != "" {
		t.Errorf("s on an issue row had an effect: status %q", pm.status)
	}
	if _, err := os.Stat(starStatePath()); err == nil {
		t.Error("s on an issue row must not write the star file")
	}
}

func TestStarredLaneShowsOnlyNotDoneAndOnceUnderAssigned(t *testing.T) {
	m := sized(newTestPickerModel(t, &fakeJiraSource{}))
	m, _ = send(m, issuesLoadedMsg{})
	m, _ = press(m, "2")
	m, _ = send(m, epicsLoadedMsg{
		epics: []ticketStatus{{Key: "E-1", Summary: "Mine", StatusCategory: "new"}},
		starred: []ticketStatus{
			{Key: "E-1", Summary: "Mine", StatusCategory: "new"},
			{Key: "E-2", Summary: "Team", StatusCategory: "indeterminate"},
			{Key: "E-3", Summary: "Finished", StatusCategory: "done"},
		},
		starredKeys: []string{"E-1", "E-2", "E-3", "E-4"},
	})
	got := rowKeys(m)
	if len(got) != 2 || got[0] != "E-1" || got[1] != "E-2" {
		t.Fatalf("rows = %v, want [E-1 E-2]", got)
	}
}

func TestEnterOnEpicOpensChildrenAndIOpensTheEpic(t *testing.T) {
	src := &fakeJiraSource{
		epicChildrenResult: []ticketStatus{{Key: "PROJ-3011", StatusName: "In Progress", Summary: "Roll out OIDC"}},
		accountIDResult:    "account-123",
	}
	pm := epicsReady(t, src)

	pm, cmd := press(pm, "enter")
	if pm.phase != phaseEpicChildrenLoading || cmd == nil {
		t.Fatalf("enter on an epic: phase %v, want phaseEpicChildrenLoading with a command", pm.phase)
	}
	cmd()
	if len(src.epicChildrenCalls) != 1 || src.epicChildrenCalls[0] != "PROJ-3008" {
		t.Fatalf("epicChildren calls = %v, want [PROJ-3008]", src.epicChildrenCalls)
	}
	pm, _ = send(pm, epicChildrenLoadedMsg{epicKey: "PROJ-3008", issues: src.epicChildrenResult})
	if pm.phase != phaseEpicChildren {
		t.Fatalf("phase = %v, want phaseEpicChildren", pm.phase)
	}

	// i opens the epic's own detail, and esc comes back to the children.
	pm, _ = press(pm, "i")
	if pm.phase != phaseDetailLoading || pm.detailReturnPhase != phaseEpicChildren {
		t.Fatalf("i: phase %v return %v, want phaseDetailLoading returning to the children", pm.phase, pm.detailReturnPhase)
	}
	pm, _ = send(pm, detailLoadedMsg{detail: issueDetail{Key: "PROJ-3008"}})
	pm, _ = press(pm, "esc")
	if pm.phase != phaseEpicChildren {
		t.Fatalf("esc from the epic's detail = %v, want phaseEpicChildren", pm.phase)
	}

	// Enter on a child opens its detail; a assigns it; esc returns to the children.
	pm, _ = press(pm, "enter")
	if pm.phase != phaseDetailLoading {
		t.Fatalf("enter on a child = %v, want phaseDetailLoading", pm.phase)
	}
	pm, _ = send(pm, detailLoadedMsg{detail: issueDetail{Key: "PROJ-3011"}})
	pm, cmd = press(pm, "a")
	if pm.phase != phaseAssigning || cmd == nil {
		t.Fatalf("a on a child detail: phase %v", pm.phase)
	}
	pm, _ = send(pm, assignAppliedMsg{detail: issueDetail{Key: "PROJ-3011", AssigneeName: "Me"}})
	pm, _ = press(pm, "esc")
	if pm.phase != phaseEpicChildren {
		t.Fatalf("esc = %v, want phaseEpicChildren", pm.phase)
	}
}

func TestChildrenTransitionAndAssignActOnTheHighlightedChild(t *testing.T) {
	src := &fakeJiraSource{}
	pm := epicsReady(t, src)
	pm, _ = press(pm, "enter")
	pm, _ = send(pm, epicChildrenLoadedMsg{epicKey: "PROJ-3008", issues: []ticketStatus{{Key: "PROJ-3011"}}})

	pm, cmd := press(pm, "t")
	if pm.phase != phaseDetailLoading || !pm.pendingTransition || pm.detailReturnPhase != phaseEpicChildren || cmd == nil {
		t.Fatalf("t on a child: phase %v pending %v return %v", pm.phase, pm.pendingTransition, pm.detailReturnPhase)
	}
}

func TestEscFromChildrenReturnsToEpicsWithCursorOnThatEpic(t *testing.T) {
	m := sized(newTestPickerModel(t, &fakeJiraSource{}))
	m, _ = send(m, issuesLoadedMsg{})
	m, _ = press(m, "2")
	m, _ = send(m, epicsLoadedMsg{epics: []ticketStatus{{Key: "E-1"}, {Key: "E-2"}, {Key: "E-3"}}})
	m, _ = press(m, "down")
	m, _ = press(m, "down")
	m, _ = press(m, "enter")
	m, _ = send(m, epicChildrenLoadedMsg{epicKey: "E-3", issues: []ticketStatus{{Key: "C-1"}}})
	m, _ = press(m, "esc")
	if m.phase != phaseReady || m.tab != tabEpics {
		t.Fatalf("phase %v tab %v, want the Epics tab", m.phase, m.tab)
	}
	if r, _ := m.view.selected(); r.Issue.Key != "E-3" {
		t.Errorf("cursor on %q, want E-3", r.Issue.Key)
	}
}

func TestReloadingEpicChildrenResetsStaleFilter(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	pm, _ = press(pm, "enter")
	pm, _ = send(pm, epicChildrenLoadedMsg{epicKey: "PROJ-3008", issues: []ticketStatus{{Key: "PROJ-3011", Summary: "Roll out OIDC"}}})
	pm.epicChildrenList.SetFilterText("Roll")

	pm, _ = send(pm, epicChildrenLoadedMsg{epicKey: "PROJ-9000", issues: []ticketStatus{{Key: "PROJ-9999", Summary: "Something else"}}})
	if n := len(pm.epicChildrenList.VisibleItems()); n != 1 {
		t.Fatalf("visible items after reloading children = %d, want 1 (stale filter must be reset)", n)
	}
}

func TestEpicsTabLoadError(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, _ = press(pm, "2")
	pm, _ = send(pm, epicsLoadedMsg{err: errSignedOut})
	if pm.phase != phaseReady {
		t.Fatalf("a tab load error must not leave the tabbed view, phase = %v", pm.phase)
	}
	if out := pm.View(); !strings.Contains(out, "[r] retry") {
		t.Errorf("view lacks the retry hint:\n%s", out)
	}
	pm, cmd := press(pm, "r")
	if cmd == nil || pm.tabErr[tabEpics] != nil {
		t.Error("r must clear the error and reload")
	}
}

// ---- search ---------------------------------------------------------------

func TestSlashEntersSearchAndEscRestoresTheCursor(t *testing.T) {
	m := sized(newTestPickerModel(t, &fakeJiraSource{}))
	m, _ = send(m, issuesLoadedMsg{issues: []ticketStatus{
		{Key: "PROJ-1", Summary: "alpha"}, {Key: "PROJ-2", Summary: "beta"}, {Key: "PROJ-3", Summary: "gamma"},
	}})
	m, _ = press(m, "down")
	m, _ = press(m, "down") // on PROJ-3

	m, cmd := press(m, "/")
	if m.mode != modeSearch || cmd == nil {
		t.Fatalf("/ must enter search mode, mode = %v", m.mode)
	}
	for _, r := range "beta" {
		m, _ = press(m, string(r))
	}
	if got := rowKeys(m); len(got) != 1 || got[0] != "PROJ-2" {
		t.Fatalf("local filter rows = %v, want [PROJ-2]", got)
	}

	m, _ = press(m, "esc")
	if m.mode != modeBrowse {
		t.Fatalf("esc must return to browse mode, mode = %v", m.mode)
	}
	if got := rowKeys(m); len(got) != 3 {
		t.Fatalf("lanes must be restored, rows = %v", got)
	}
	if r, _ := m.view.selected(); r.Issue.Key != "PROJ-3" {
		t.Errorf("cursor on %q after esc, want back on PROJ-3", r.Issue.Key)
	}
}

func TestSearchModeTypesLettersIncludingS(t *testing.T) {
	m := epicsReady(t, &fakeJiraSource{})
	m, _ = press(m, "/")
	m, _ = press(m, "s")
	if m.searchInput.Value() != "s" {
		t.Errorf("query = %q, want %q (s types in search mode)", m.searchInput.Value(), "s")
	}
	if len(readStars("cloud-a")) != 0 {
		t.Error("s in search mode must not star anything")
	}
	for _, key := range []string{"q", "t", "b"} {
		m, _ = press(m, key)
	}
	if m.quitting || m.phase != phaseReady {
		t.Error("command letters must type, not run, in search mode")
	}
}

func TestSearchDebouncesToOneRemoteQueryScopedToTheTab(t *testing.T) {
	src := &fakeJiraSource{}
	m := readyWithOneIssue(t, src)
	m, _ = press(m, "/")
	var cmd tea.Cmd
	for _, r := range "abc" {
		m, cmd = press(m, string(r))
	}
	if cmd == nil {
		t.Fatal("typing must schedule a debounced search")
	}
	if m.searchSeq != 4 { // 1 for entering search, then one per keystroke
		t.Fatalf("searchSeq = %d, want 4", m.searchSeq)
	}

	m, c := send(m, debounceFireMsg{seq: 1, query: "a"})
	if c != nil || m.searching {
		t.Error("a stale debounce must not run a search")
	}
	m, c = send(m, debounceFireMsg{seq: m.searchSeq, query: "abc"})
	if c == nil || !m.searching {
		t.Fatal("the current debounce must dispatch the search")
	}
	if len(src.searchScopedCalls) != 0 {
		t.Fatal("dispatch alone must not call the source")
	}
	c()
	if len(src.searchScopedCalls) != 1 || src.searchScopedCalls[0] != (scopedCall{"abc", scopeIssues}) {
		t.Fatalf("searchScoped calls = %v, want one for abc on the Issues scope", src.searchScopedCalls)
	}
}

func TestSearchOnEpicsTabUsesEpicScope(t *testing.T) {
	src := &fakeJiraSource{}
	m := epicsReady(t, src)
	m, _ = press(m, "/")
	m, _ = press(m, "x")
	_, c := send(m, debounceFireMsg{seq: m.searchSeq, query: "x"})
	c()
	if len(src.searchScopedCalls) != 1 || src.searchScopedCalls[0].scope != scopeEpics {
		t.Fatalf("calls = %v, want one on the Epics scope", src.searchScopedCalls)
	}
}

func TestSearchResultsLaneAppearsAfterTheDebounceAndStaleResultsAreDropped(t *testing.T) {
	m := readyWithOneIssue(t, &fakeJiraSource{})
	m, _ = press(m, "/")
	m, _ = press(m, "w")
	stale := m.searchSeq
	m, _ = press(m, "i")

	m, _ = send(m, searchResultsMsg{seq: stale, scope: scopeIssues, issues: []ticketStatus{{Key: "OLD-1"}}})
	if m.hits != nil {
		t.Error("a stale searchResultsMsg must be dropped")
	}

	m, _ = send(m, searchResultsMsg{seq: m.searchSeq, scope: scopeIssues, issues: []ticketStatus{
		{Key: "OTHER-1", Summary: "other"}, {Key: "PROJ-1", Summary: "Widget"},
	}})
	if m.searching {
		t.Error("the current result must clear searching")
	}
	var lanes []string
	for _, r := range m.view.rows {
		if r.Kind == rowHeading {
			lanes = append(lanes, r.Lane)
		}
	}
	if len(lanes) != 2 || lanes[1] != laneResults {
		t.Fatalf("lanes = %v, want the Results lane below the filtered lane", lanes)
	}
	keys := rowKeys(m)
	if len(keys) == 0 || keys[len(keys)-1] != "OTHER-1" {
		t.Errorf("rows = %v, want OTHER-1 last; PROJ-1 is not repeated", keys)
	}
}

func TestSearchResultsFromAnotherTabAreDropped(t *testing.T) {
	m := readyWithOneIssue(t, &fakeJiraSource{})
	m, _ = press(m, "/")
	m, _ = press(m, "x")
	m, _ = send(m, searchResultsMsg{seq: m.searchSeq, scope: scopeEpics, issues: []ticketStatus{{Key: "E-1"}}})
	if m.hits != nil {
		t.Error("an Epics-scoped answer must not land on the Issues tab")
	}
}

func TestSearchErrorKeepsTheLanes(t *testing.T) {
	m := readyWithOneIssue(t, &fakeJiraSource{})
	m, _ = press(m, "/")
	m, _ = press(m, "w")
	m, _ = send(m, searchResultsMsg{seq: m.searchSeq, scope: scopeIssues, err: errSignedOut})
	if got := rowKeys(m); len(got) != 1 || got[0] != "PROJ-1" {
		t.Fatalf("lanes must stay visible on a search error, rows = %v", got)
	}
	last := m.view.rows[len(m.view.rows)-1]
	if last.Kind != rowNote || !strings.Contains(last.Text, "Search failed") {
		t.Errorf("last row = %+v, want a one-line error under the Results heading", last)
	}
}

func TestClearingTheQueryDropsHitsWithoutSearching(t *testing.T) {
	m := readyWithOneIssue(t, &fakeJiraSource{})
	m, _ = press(m, "/")
	m, _ = press(m, "x")
	m, _ = send(m, searchResultsMsg{seq: m.searchSeq, scope: scopeIssues, issues: []ticketStatus{{Key: "X-1"}}})
	seq := m.searchSeq
	m, cmd := press(m, "backspace")
	if m.hits != nil || m.searchSeq == seq {
		t.Error("clearing the query must drop the hits and bump searchSeq")
	}
	if cmd != nil {
		if _, ok := cmd().(debounceFireMsg); ok {
			t.Error("an empty query must not schedule a remote search")
		}
	}
}

func TestSearchHitsAreNeverWrittenToThePickerCache(t *testing.T) {
	m := readyWithOneIssue(t, &fakeJiraSource{})
	m, _ = press(m, "/")
	m, _ = press(m, "x")
	m, _ = send(m, searchResultsMsg{seq: m.searchSeq, scope: scopeIssues, issues: []ticketStatus{{Key: "HIT-1"}}})
	m, _ = send(m, issuesLoadedMsg{issues: []ticketStatus{{Key: "PROJ-1"}}, subtasks: []ticketStatus{{Key: "PROJ-2", ParentKey: "PROJ-1", HierarchyLevel: -1}}})

	data, err := os.ReadFile(pickerCachePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "HIT-1") {
		t.Errorf("search hits leaked into the cache: %s", data)
	}
	var cached []ticketStatus
	if err := json.Unmarshal(data, &cached); err != nil || len(cached) != 2 {
		t.Fatalf("cache = %s (%v), want a bare array of the 2 lane rows", data, err)
	}
	if cached[0].Key != "PROJ-1" || cached[1].Key != "PROJ-2" {
		t.Errorf("cache order = %v, want issues then sub-tasks", cached)
	}
	_ = m
}

func TestOldShapeCacheDecodesAndRenders(t *testing.T) {
	withTempStateDir(t)
	withTempConfigDir(t)
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `[{"Key":"PROJ-1","Summary":"Widget","StatusName":"Open","StatusCategory":"new"}]`
	if err := os.WriteFile(pickerCachePath(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	m := sized(newPickerModel(&fakeJiraSource{}))
	if m.phase != phaseReady {
		t.Fatalf("a cache must paint the list at once, phase = %v", m.phase)
	}
	if !strings.Contains(m.View(), "PROJ-1") {
		t.Errorf("old-shape cache did not render:\n%s", m.View())
	}
}

func TestSubtasksSplitBackOutOfTheCache(t *testing.T) {
	issues, subs := splitCached([]ticketStatus{{Key: "A"}, {Key: "B", ParentKey: "A", HierarchyLevel: -1}})
	if len(issues) != 1 || len(subs) != 1 || subs[0].Key != "B" {
		t.Errorf("issues %v subtasks %v", issues, subs)
	}
}

// ---- mouse and view -------------------------------------------------------

func TestClickSelectsARowAndSecondClickOpensIt(t *testing.T) {
	src := &fakeJiraSource{}
	m := sized(newTestPickerModel(t, src))
	m, _ = send(m, issuesLoadedMsg{issues: []ticketStatus{{Key: "A-1"}, {Key: "A-2"}, {Key: "A-3"}}})
	// Line 0 is the tab bar, line 1 the lane heading, line 2 the first row.
	click := func(y int) tea.MouseMsg {
		return tea.MouseMsg{X: 10, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	}

	m, _ = send(m, click(1))
	if r, _ := m.view.selected(); r.Issue.Key != "A-1" {
		t.Fatalf("a click on a heading must do nothing, cursor on %q", r.Issue.Key)
	}
	if m.phase != phaseReady {
		t.Fatal("a click on a heading must not open anything")
	}

	m, _ = send(m, click(4))
	if r, _ := m.view.selected(); r.Issue.Key != "A-3" {
		t.Fatalf("click on line 4: cursor on %q, want A-3", r.Issue.Key)
	}
	if m.phase != phaseReady {
		t.Fatal("the first click only selects")
	}
	m, cmd := send(m, click(4))
	if m.phase != phaseDetailLoading || cmd == nil {
		t.Fatalf("the second click on the selected row must open it, phase = %v", m.phase)
	}
}

func TestClickOnAChildRowUsesTheSearchHeaderOffset(t *testing.T) {
	m := sized(newTestPickerModel(t, &fakeJiraSource{}))
	m, _ = send(m, issuesLoadedMsg{issues: []ticketStatus{{Key: "A-1"}, {Key: "A-2"}}})
	m, _ = press(m, "/") // the search box adds a header line
	m, _ = send(m, tea.MouseMsg{X: 10, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if r, _ := m.view.selected(); r.Issue.Key != "A-2" {
		t.Errorf("cursor on %q, want A-2 (rows start on line 3 in search mode)", r.Issue.Key)
	}
}

func TestWheelScrollsTheList(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	m, _ = send(m, tea.WindowSizeMsg{Width: 100, Height: 10})
	var issues []ticketStatus
	for i := 0; i < 40; i++ {
		issues = append(issues, ticketStatus{Key: "A-" + string(rune('A'+i%26)) + string(rune('a'+i/26))})
	}
	m, _ = send(m, issuesLoadedMsg{issues: issues})
	m, _ = send(m, tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.view.offset != wheelStep {
		t.Fatalf("offset after one wheel tick = %d, want %d", m.view.offset, wheelStep)
	}
	m, _ = send(m, tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.view.offset != 0 {
		t.Fatalf("offset after wheel up = %d, want 0", m.view.offset)
	}
}

func TestViewFitsTheWindowOnBothTabs(t *testing.T) {
	m := epicsReady(t, &fakeJiraSource{})
	for _, size := range [][2]int{{100, 30}, {50, 12}, {120, 8}} {
		m, _ = send(m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, tb := range []string{"1", "2"} {
			m, _ = press(m, tb)
			out := m.View()
			if h := lipgloss.Height(out); h > size[1] {
				t.Errorf("%dx%d tab %s: height %d > %d", size[0], size[1], tb, h, size[1])
			}
			if w := lipgloss.Width(out); w > size[0] {
				t.Errorf("%dx%d tab %s: width %d > %d", size[0], size[1], tb, w, size[0])
			}
		}
	}
}

func TestViewShowsTabsLanesAndFooter(t *testing.T) {
	m := epicsReady(t, &fakeJiraSource{})
	out := m.View()
	for _, want := range []string{"Issues", "[Epics]", "Assigned to me (1)", "Starred (0)", "PROJ-3008", "[s] star"} {
		if !strings.Contains(out, want) {
			t.Errorf("view lacks %q:\n%s", want, out)
		}
	}
	m, _ = press(m, "1")
	if strings.Contains(m.View(), "[s]") {
		t.Error("the footer must not offer [s] on an issue row")
	}
}

func TestHelpKeyShowsFullKeyListAndAnyKeyCloses(t *testing.T) {
	m := readyWithOneIssue(t, &fakeJiraSource{})
	m, _ = press(m, "?")
	if !m.showHelp || !strings.Contains(m.View(), "Browse mode") {
		t.Fatal("? must show the full key list")
	}
	m, _ = press(m, "x")
	if m.showHelp {
		t.Error("any key closes the help")
	}
}

func TestBackgroundRefreshFailureDoesNotReplaceTheList(t *testing.T) {
	m := readyWithOneIssue(t, &fakeJiraSource{})
	m, _ = send(m, issuesLoadedMsg{err: errSignedOut})
	if m.phase != phaseReady || len(rowKeys(m)) != 1 {
		t.Fatalf("a failed refresh must keep the loaded list, phase %v rows %v", m.phase, rowKeys(m))
	}
	if !strings.Contains(m.status, "refresh") {
		t.Errorf("status = %q, want a refresh-failed note", m.status)
	}
}

func TestLoadEpicsTabReadsAssignedThenTheStarredBatch(t *testing.T) {
	withTempStateDir(t)
	orig := cloudIDFn
	cloudIDFn = func() string { return "cloud-a" }
	t.Cleanup(func() { cloudIDFn = orig })
	toggleStar("cloud-a", ticketStatus{Key: "E-2"})
	toggleStar("cloud-a", ticketStatus{Key: "E-9"}) // Jira will not return this one

	src := &fakeJiraSource{
		myEpicsList: []ticketStatus{{Key: "E-1"}},
		myEpicsMore: true,
		results:     map[string]ticketStatus{"E-2": {Key: "E-2", Summary: "team"}},
	}
	msg := loadEpicsTab(src)().(epicsLoadedMsg)
	if msg.err != nil || msg.cloudID != "cloud-a" || !msg.more {
		t.Fatalf("msg = %+v", msg)
	}
	if len(src.calls) != 1 || len(src.calls[0]) != 2 {
		t.Errorf("search calls = %v, want one batched lookup of both starred keys", src.calls)
	}
	if len(msg.starred) != 1 || msg.starred[0].Key != "E-2" {
		t.Errorf("starred = %v, want only E-2 (E-9 was not returned)", msg.starred)
	}
	if len(msg.starredKeys) != 2 {
		t.Errorf("starredKeys = %v, want both, including the one Jira did not return", msg.starredKeys)
	}
}

func TestLoadEpicsTabSkipsTheLookupWithNoStars(t *testing.T) {
	withTempStateDir(t)
	withTempConfigDir(t) // never read the real credentials
	src := &fakeJiraSource{}
	msg := loadEpicsTab(src)().(epicsLoadedMsg)
	if msg.err != nil || len(src.calls) != 0 {
		t.Errorf("err %v calls %v, want no lookup", msg.err, src.calls)
	}
	src = &fakeJiraSource{myEpicsErr: errSignedOut}
	if msg := loadEpicsTab(src)().(epicsLoadedMsg); msg.err != errSignedOut {
		t.Errorf("err = %v, want the myEpics error", msg.err)
	}
	withTempStateDir(t)
	toggleStar("", ticketStatus{Key: "E-2"})
	src = &fakeJiraSource{searchErr: errSignedOut}
	if msg := loadEpicsTab(src)().(epicsLoadedMsg); msg.err != errSignedOut {
		t.Errorf("err = %v, want the starred lookup error", msg.err)
	}
}

// ---- cache TTLs -----------------------------------------------------------

func withClock(t *testing.T, now time.Time) {
	t.Helper()
	orig := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = orig })
}

func TestFreshIssuesCacheSkipsTheStartupFetch(t *testing.T) {
	withTempStateDir(t)
	withTempConfigDir(t)
	if err := writePickerCache([]ticketStatus{{Key: "PROJ-1"}}, nil); err != nil {
		t.Fatal(err)
	}
	m := newPickerModel(&fakeJiraSource{})
	_, cmd := send(m, initialNoKeyMsg{})
	if cmd != nil {
		t.Error("a fresh Issues cache must not trigger an API fetch")
	}

	withClock(t, time.Now().Add(issuesCacheTTL+time.Minute))
	m = newPickerModel(&fakeJiraSource{})
	_, cmd = send(m, initialNoKeyMsg{})
	if cmd == nil {
		t.Error("a stale Issues cache must be refreshed")
	}
}

func TestEpicsCacheServesFreshAndRefreshesStale(t *testing.T) {
	withTempStateDir(t)
	withTempConfigDir(t)
	now := time.Now()
	withClock(t, now)
	_ = writePickerCache([]ticketStatus{{Key: "PROJ-1"}}, nil)
	if err := writeEpicsCache(epicsCache{FetchedAt: now.Add(-time.Hour), Epics: []ticketStatus{{Key: "EP-1", IssueType: "Epic"}}, CloudID: "c"}); err != nil {
		t.Fatal(err)
	}
	m := sized(newPickerModel(&fakeJiraSource{}))
	m, cmd := press(m, "2")
	if cmd != nil {
		t.Error("a fresh Epics cache must not trigger an API fetch")
	}
	if !strings.Contains(m.View(), "EP-1") {
		t.Errorf("cached epic not rendered:\n%s", m.View())
	}

	// Past 3h it still paints, but switching to the tab refetches.
	withClock(t, now.Add(epicsCacheTTL))
	m = sized(newPickerModel(&fakeJiraSource{}))
	m, cmd = press(m, "2")
	if cmd == nil {
		t.Error("a stale Epics cache must be refreshed")
	}
	if !strings.Contains(m.View(), "EP-1") {
		t.Errorf("stale cache should still paint:\n%s", m.View())
	}
}

func TestRefreshKeyBypassesFreshCaches(t *testing.T) {
	withTempStateDir(t)
	withTempConfigDir(t)
	now := time.Now()
	withClock(t, now)
	_ = writePickerCache([]ticketStatus{{Key: "PROJ-1"}}, nil)
	_ = writeEpicsCache(epicsCache{FetchedAt: now, Epics: []ticketStatus{{Key: "EP-1", IssueType: "Epic"}}})
	m := sized(newPickerModel(&fakeJiraSource{}))
	if _, cmd := press(m, "r"); cmd == nil {
		t.Error("r must refetch Issues even when the cache is fresh")
	}
	m, _ = press(m, "2")
	if _, cmd := press(m, "r"); cmd == nil {
		t.Error("r must refetch Epics even when the cache is fresh")
	}
}

func TestEpicsLoadedWritesCacheAndStarKeepsFetchTime(t *testing.T) {
	m := sized(newTestPickerModel(t, &fakeJiraSource{}))
	now := time.Now().Truncate(time.Second)
	withClock(t, now)
	m, _ = send(m, issuesLoadedMsg{issues: []ticketStatus{{Key: "PROJ-1"}}})
	m, _ = send(m, epicsLoadedMsg{epics: []ticketStatus{{Key: "EP-1", IssueType: "Epic"}}, cloudID: "c"})
	c, ok := readEpicsCache()
	if !ok || len(c.Epics) != 1 || !c.FetchedAt.Equal(now) {
		t.Fatalf("cache after load = %+v, %v", c, ok)
	}
	withClock(t, now.Add(time.Hour))
	m, _ = press(m, "2")
	m, _ = press(m, "s")
	c, _ = readEpicsCache()
	if !c.FetchedAt.Equal(now) {
		t.Errorf("starring changed FetchedAt to %v, want %v", c.FetchedAt, now)
	}
	if len(c.StarredKeys) != 1 || c.StarredKeys[0] != "EP-1" {
		t.Errorf("star not persisted to cache: %+v", c.StarredKeys)
	}
	_ = m
}
