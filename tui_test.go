package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func newTestPickerModel(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	withTempStateDir(t)
	withTempConfigDir(t)
	return newPickerModel(src)
}

func TestPickerModelLoadsIssuesOnSuccess(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	issues := []ticketStatus{{Key: "TICKET-1", StatusName: "Open", Summary: "Widget"}}
	next, _ := m.Update(issuesLoadedMsg{issues: issues})
	pm := next.(pickerModel)
	if pm.phase != phaseReady {
		t.Fatalf("phase = %v, want phaseReady", pm.phase)
	}
	if len(pm.list.Items()) != 1 {
		t.Fatalf("list has %d items, want 1", len(pm.list.Items()))
	}
}

func TestPickerModelShowsErrorOnLoadFailure(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	next, _ := m.Update(issuesLoadedMsg{err: errSignedOut})
	pm := next.(pickerModel)
	if pm.phase != phaseError {
		t.Fatalf("phase = %v, want phaseError", pm.phase)
	}
	if pm.err != errSignedOut {
		t.Errorf("err = %v, want errSignedOut", pm.err)
	}
}

func TestPickerModelRetryFromErrorReloads(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	next, _ := m.Update(issuesLoadedMsg{err: errSignedOut})
	pm := next.(pickerModel)

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	pm = next.(pickerModel)
	if pm.phase != phaseLoading {
		t.Fatalf("phase after retry = %v, want phaseLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("retry must issue a command to reload")
	}
}

// readyWithOneIssue builds a model in phaseReady with one list item selected.
func readyWithOneIssue(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	m := newTestPickerModel(t, src)
	issues := []ticketStatus{{Key: "TICKET-1", StatusName: "Open", Summary: "Widget"}}
	next, _ := m.Update(issuesLoadedMsg{issues: issues})
	return next.(pickerModel)
}

func TestPickerModelEnterLoadsDetail(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseDetailLoading {
		t.Fatalf("phase after enter = %v, want phaseDetailLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("enter must issue a command to load the detail")
	}

	next, _ = pm.Update(detailLoadedMsg{detail: issueDetail{Key: "TICKET-1", StatusName: "Open", Summary: "Widget"}})
	pm = next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase after detail loaded = %v, want phaseDetail", pm.phase)
	}
	if pm.detail == nil || pm.detail.Key != "TICKET-1" {
		t.Errorf("detail = %+v, want TICKET-1", pm.detail)
	}
}

func TestPickerModelEscFromDetailReturnsToList(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	next, _ = pm.Update(detailLoadedMsg{detail: issueDetail{Key: "TICKET-1"}})
	pm = next.(pickerModel)

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseReady {
		t.Fatalf("phase after esc from detail = %v, want phaseReady", pm.phase)
	}
	if pm.detail != nil {
		t.Error("detail should be cleared after returning to the list")
	}
}

func TestPickerModelEscFromListQuits(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if !pm.quitting {
		t.Error("expected quitting=true after esc on the list")
	}
	if cmd == nil {
		t.Fatal("esc must issue tea.Quit")
	}
}

func TestPickerModelSignInFailureShowsError(t *testing.T) {
	m := newTestPickerModel(t, &fakeJiraSource{})
	next, _ := m.Update(signedInMsg{err: errSignedOut})
	pm := next.(pickerModel)
	if pm.phase != phaseError {
		t.Fatalf("phase = %v, want phaseError", pm.phase)
	}
}

// detailReady returns a model already showing TICKET-1's detail screen.
func detailReady(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	pm := readyWithOneIssue(t, src)
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	next, _ = pm.Update(detailLoadedMsg{detail: issueDetail{Key: "TICKET-1", StatusName: "Open"}})
	return next.(pickerModel)
}

func TestPickerModelTKeyLoadsTransitionsLive(t *testing.T) {
	src := &fakeJiraSource{transitionsList: []transitionOption{
		{ID: "11", Name: "Start Progress", ToStatusName: "In Progress"},
		{ID: "21", Name: "Close", ToStatusName: "Closed"},
	}}
	pm := detailReady(t, src)

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	pm = next.(pickerModel)
	if pm.phase != phaseTransitionsLoading {
		t.Fatalf("phase after t = %v, want phaseTransitionsLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("t must issue a command to fetch transitions live")
	}

	next, _ = pm.Update(transitionsLoadedMsg{transitions: src.transitionsList})
	pm = next.(pickerModel)
	if pm.phase != phaseTransitions {
		t.Fatalf("phase after transitions loaded = %v, want phaseTransitions", pm.phase)
	}
	if len(pm.transitionList.Items()) != 2 {
		t.Fatalf("transition list has %d items, want 2", len(pm.transitionList.Items()))
	}
}

func TestPickerModelSelectingTransitionAppliesIt(t *testing.T) {
	src := &fakeJiraSource{}
	pm := detailReady(t, src)
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	pm = next.(pickerModel)
	next, _ = pm.Update(transitionsLoadedMsg{transitions: []transitionOption{{ID: "21", Name: "Close", ToStatusName: "Closed"}}})
	pm = next.(pickerModel)

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseApplyingTransition {
		t.Fatalf("phase after selecting a transition = %v, want phaseApplyingTransition", pm.phase)
	}
	if cmd == nil {
		t.Fatal("selecting a transition must issue a command to apply it")
	}

	next, _ = pm.Update(transitionAppliedMsg{detail: issueDetail{Key: "TICKET-1", StatusName: "Closed"}})
	pm = next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase after transition applied = %v, want phaseDetail", pm.phase)
	}
	if pm.detail.StatusName != "Closed" {
		t.Errorf("detail.StatusName = %q, want Closed", pm.detail.StatusName)
	}
}

// enterSearch moves a ready model into search mode via ctrl+f.
func enterSearch(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	pm := readyWithOneIssue(t, src)
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	pm = next.(pickerModel)
	if pm.phase != phaseSearch {
		t.Fatalf("phase after ctrl+f = %v, want phaseSearch", pm.phase)
	}
	return pm
}

func TestPickerModelTypingDebouncesToOneQuery(t *testing.T) {
	src := &fakeJiraSource{}
	pm := enterSearch(t, src)

	// Three keystrokes in a row (as if typed fast): only the last one's
	// debounce should still match m.searchSeq by the time it fires.
	for _, r := range []rune("abc") {
		next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		pm = next.(pickerModel)
	}
	if pm.searchSeq != 3 {
		t.Fatalf("searchSeq = %d, want 3 after 3 keystrokes", pm.searchSeq)
	}

	// Stale debounce (from the first keystroke) must be dropped.
	next, cmd := pm.Update(debounceFireMsg{seq: 1, query: "a"})
	pm = next.(pickerModel)
	if cmd != nil {
		t.Error("a stale debounceFireMsg must not issue a search command")
	}
	if pm.searching {
		t.Error("a stale debounceFireMsg must not set searching")
	}

	// Current debounce fires the query exactly once.
	next, cmd = pm.Update(debounceFireMsg{seq: 3, query: "abc"})
	pm = next.(pickerModel)
	if cmd == nil {
		t.Fatal("the current debounceFireMsg must issue a search command")
	}
	if !pm.searching {
		t.Error("searching should be true once the query is dispatched")
	}
	if len(src.searchTextCalls) != 0 {
		t.Fatal("the search command hasn't run yet; dispatch alone must not call the source")
	}
}

func TestPickerModelStaleSearchResultsAreDropped(t *testing.T) {
	pm := enterSearch(t, &fakeJiraSource{})
	pm.searchSeq = 2
	pm.searching = true

	next, _ := pm.Update(searchResultsMsg{seq: 1, issues: []ticketStatus{{Key: "OLD-1"}}})
	pm = next.(pickerModel)
	if !pm.searching {
		t.Error("a stale searchResultsMsg must not clear searching")
	}
	if len(pm.searchList.Items()) != 0 {
		t.Error("a stale searchResultsMsg must not populate the search list")
	}

	next, _ = pm.Update(searchResultsMsg{seq: 2, issues: []ticketStatus{{Key: "NEW-1"}}})
	pm = next.(pickerModel)
	if pm.searching {
		t.Error("the current searchResultsMsg must clear searching")
	}
	if len(pm.searchList.Items()) != 1 {
		t.Fatalf("search list has %d items, want 1", len(pm.searchList.Items()))
	}
}

func TestPickerModelEmptyQueryClearsResultsWithoutSearching(t *testing.T) {
	pm := enterSearch(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	pm = next.(pickerModel)
	next, _ = pm.Update(searchResultsMsg{seq: pm.searchSeq, issues: []ticketStatus{{Key: "X-1"}}})
	pm = next.(pickerModel)
	if len(pm.searchList.Items()) != 1 {
		t.Fatal("precondition failed: expected one search result before clearing")
	}

	seqBefore := pm.searchSeq
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	pm = next.(pickerModel)
	if len(pm.searchList.Items()) != 0 {
		t.Error("clearing the query must clear the results")
	}
	if pm.searchSeq == seqBefore {
		t.Error("clearing the query must still bump searchSeq (invalidating any in-flight query)")
	}
}

func TestPickerModelSearchResultEscReturnsToSearchNotMyIssues(t *testing.T) {
	src := &fakeJiraSource{}
	pm := enterSearch(t, src)
	next, _ := pm.Update(searchResultsMsg{seq: pm.searchSeq, issues: []ticketStatus{{Key: "OTHER-1"}}})
	pm = next.(pickerModel)

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseDetailLoading {
		t.Fatalf("phase after enter on a search result = %v, want phaseDetailLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("enter on a search result must load its detail")
	}
	if pm.detailReturnPhase != phaseSearch {
		t.Fatalf("detailReturnPhase = %v, want phaseSearch", pm.detailReturnPhase)
	}

	next, _ = pm.Update(detailLoadedMsg{detail: issueDetail{Key: "OTHER-1"}})
	pm = next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase after detail loaded = %v, want phaseDetail", pm.phase)
	}

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseSearch {
		t.Fatalf("phase after esc from a search-opened detail = %v, want phaseSearch (not phaseReady)", pm.phase)
	}
}

func TestPickerModelEKeyLoadsEpicsThenDrillsIntoChildrenThenAssigns(t *testing.T) {
	src := &fakeJiraSource{
		myEpicsList:        []ticketStatus{{Key: "TICKET-3008", StatusName: "Backlog", Summary: "Tenant Access Vending"}},
		epicChildrenResult: []ticketStatus{{Key: "TICKET-3011", StatusName: "In Progress", Summary: "Roll out OIDC to stg"}},
		accountIDResult:    "account-123",
	}
	pm := readyWithOneIssue(t, src)

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	pm = next.(pickerModel)
	if pm.phase != phaseEpicsLoading {
		t.Fatalf("phase after e = %v, want phaseEpicsLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("e must issue a command to load epics")
	}

	next, _ = pm.Update(epicsLoadedMsg{epics: src.myEpicsList})
	pm = next.(pickerModel)
	if pm.phase != phaseEpics {
		t.Fatalf("phase after epics loaded = %v, want phaseEpics", pm.phase)
	}
	if len(pm.epicList.Items()) != 1 {
		t.Fatalf("epic list has %d items, want 1", len(pm.epicList.Items()))
	}

	next, cmd = pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseEpicChildrenLoading {
		t.Fatalf("phase after enter on an epic = %v, want phaseEpicChildrenLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("enter on an epic must issue a command to load its children")
	}

	next, _ = pm.Update(epicChildrenLoadedMsg{epicKey: "TICKET-3008", issues: src.epicChildrenResult})
	pm = next.(pickerModel)
	if pm.phase != phaseEpicChildren {
		t.Fatalf("phase after children loaded = %v, want phaseEpicChildren", pm.phase)
	}
	if len(pm.epicChildrenList.Items()) != 1 {
		t.Fatalf("epic children list has %d items, want 1", len(pm.epicChildrenList.Items()))
	}

	next, cmd = pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseDetailLoading {
		t.Fatalf("phase after enter on a child = %v, want phaseDetailLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("enter on a child must load its detail")
	}
	if pm.detailReturnPhase != phaseEpicChildren {
		t.Fatalf("detailReturnPhase = %v, want phaseEpicChildren", pm.detailReturnPhase)
	}

	next, _ = pm.Update(detailLoadedMsg{detail: issueDetail{Key: "TICKET-3011", StatusName: "In Progress"}})
	pm = next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase after detail loaded = %v, want phaseDetail", pm.phase)
	}

	next, cmd = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	pm = next.(pickerModel)
	if pm.phase != phaseAssigning {
		t.Fatalf("phase after a = %v, want phaseAssigning", pm.phase)
	}
	if cmd == nil {
		t.Fatal("a must issue a command to assign")
	}

	next, _ = pm.Update(assignAppliedMsg{detail: issueDetail{Key: "TICKET-3011", StatusName: "In Progress", AssigneeName: "Me"}})
	pm = next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase after assign applied = %v, want phaseDetail", pm.phase)
	}
	if pm.detail.AssigneeName != "Me" {
		t.Errorf("detail.AssigneeName = %q, want Me", pm.detail.AssigneeName)
	}

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseEpicChildren {
		t.Fatalf("phase after esc from an epic-child-opened detail = %v, want phaseEpicChildren (not phaseReady)", pm.phase)
	}
}

// epicsReady returns a model already showing the epics list with one epic.
func epicsReady(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	pm := readyWithOneIssue(t, src)
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	pm = next.(pickerModel)
	epics := []ticketStatus{{Key: "TICKET-3008", StatusName: "Backlog", Summary: "Tenant Access Vending"}}
	next, _ = pm.Update(epicsLoadedMsg{epics: epics})
	return next.(pickerModel)
}

func TestPickerModelQWhileFilteringEpicsDoesNotQuit(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})

	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	pm = next.(pickerModel)
	if pm.epicList.FilterState() != list.Filtering {
		t.Fatalf("filter state = %v, want Filtering", pm.epicList.FilterState())
	}

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	pm = next.(pickerModel)
	if pm.quitting {
		t.Error("q while filtering the epics list must not quit — it's a query character")
	}
}

func TestPickerModelEscFromEpicChildrenReturnsToEpicsNotReady(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	children := []ticketStatus{{Key: "TICKET-3011", StatusName: "In Progress"}}
	next, _ = pm.Update(epicChildrenLoadedMsg{epicKey: "TICKET-3008", issues: children})
	pm = next.(pickerModel)

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseEpics {
		t.Fatalf("phase after esc from epic children = %v, want phaseEpics", pm.phase)
	}

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseReady {
		t.Fatalf("phase after esc from epics = %v, want phaseReady", pm.phase)
	}
}

func TestPickerModelReloadingEpicChildrenResetsStaleFilter(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	firstChildren := []ticketStatus{{Key: "TICKET-3011", StatusName: "In Progress", Summary: "Roll out OIDC"}}
	next, _ = pm.Update(epicChildrenLoadedMsg{epicKey: "TICKET-3008", issues: firstChildren})
	pm = next.(pickerModel)

	// Apply a filter that still matches the first epic's one child, then
	// leave it applied — simulating a filter left on from a prior visit.
	// SetFilterText runs filterItems synchronously (list.go), unlike typing
	// through Update, which defers to an async tea.Cmd. (A filter matching
	// zero items can't be left "applied" via real keystrokes: bubbles/list
	// auto-clears it on Enter in that case — not relevant to this helper.)
	pm.epicChildrenList.SetFilterText("Roll")
	if pm.epicChildrenList.FilterState() != list.FilterApplied {
		t.Fatalf("precondition failed: filter state = %v, want FilterApplied", pm.epicChildrenList.FilterState())
	}
	if len(pm.epicChildrenList.VisibleItems()) != 1 {
		t.Fatalf("precondition failed: expected the filter to still match the one child")
	}

	secondChildren := []ticketStatus{{Key: "TICKET-9999", StatusName: "Open", Summary: "Something else"}}
	next, _ = pm.Update(epicChildrenLoadedMsg{epicKey: "TICKET-9000", issues: secondChildren})
	pm = next.(pickerModel)

	if len(pm.epicChildrenList.VisibleItems()) != 1 {
		t.Fatalf("visible items after reloading children = %d, want 1 (stale filter must be reset)", len(pm.epicChildrenList.VisibleItems()))
	}
}

// starEpic stars status via a search result, mirroring the real [ctrl+f]
// then [tab] then [s] flow: open epic search, type a query, land the
// result, focus the list, star it.
func starEpic(t *testing.T, pm pickerModel, status ticketStatus) pickerModel {
	t.Helper()
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	pm = next.(pickerModel)
	next, _ = pm.Update(epicSearchResultsMsg{seq: pm.epicSearchSeq, issues: []ticketStatus{status}})
	pm = next.(pickerModel)
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyTab})
	pm = next.(pickerModel)
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	return next.(pickerModel)
}

func TestPickerModelTabTogglesFocusBetweenOwnedAndStarredEpicLists(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	if pm.epicsOnStarred {
		t.Fatal("precondition: epics tab should open focused on the owned list")
	}
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyTab})
	pm = next.(pickerModel)
	if !pm.epicsOnStarred {
		t.Error("tab should move focus to the starred list")
	}
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyTab})
	pm = next.(pickerModel)
	if pm.epicsOnStarred {
		t.Error("tab again should move focus back to the owned list")
	}
}

func TestPickerModelSOnOwnedListIsANoop(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	pm = next.(pickerModel)
	if pm.phase != phaseEpics {
		t.Fatalf("phase after s on owned list = %v, want phaseEpics (no-op)", pm.phase)
	}
	st := readStarState()
	if len(st.Epics) != 0 {
		t.Errorf("s on the owned list must not star anything, got %v", st.Epics)
	}
}

func TestPickerModelCtrlFEntersEpicSearch(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	pm = next.(pickerModel)
	if pm.phase != phaseEpicSearch {
		t.Fatalf("phase = %v, want phaseEpicSearch", pm.phase)
	}
}

func TestPickerModelEpicSearchDebouncesAndQueries(t *testing.T) {
	src := &fakeJiraSource{searchEpicsResult: []ticketStatus{{Key: "OTHER-1", StatusName: "Open", Summary: "Shared epic"}}}
	pm := epicsReady(t, src)
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	pm = next.(pickerModel)

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	pm = next.(pickerModel)
	if cmd == nil {
		t.Fatal("typing a query character must schedule a debounced search")
	}

	next, cmd = pm.Update(epicSearchDebounceFireMsg{seq: pm.epicSearchSeq, query: "z"})
	pm = next.(pickerModel)
	if !pm.epicSearching {
		t.Fatal("epicSearching should be true once the debounce fires")
	}
	if cmd == nil {
		t.Fatal("the debounce fire must issue the actual search command")
	}

	next, _ = pm.Update(epicSearchResultsMsg{seq: pm.epicSearchSeq, issues: src.searchEpicsResult})
	pm = next.(pickerModel)
	if pm.epicSearching {
		t.Error("epicSearching should be false once results land")
	}
	if len(pm.epicSearchList.Items()) != 1 {
		t.Fatalf("epicSearchList has %d items, want 1", len(pm.epicSearchList.Items()))
	}
}

func TestPickerModelStaleEpicSearchResultIsDropped(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	pm = next.(pickerModel)
	staleSeq := pm.epicSearchSeq

	// A newer keystroke bumps the seq before the stale result arrives.
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	pm = next.(pickerModel)

	next, _ = pm.Update(epicSearchResultsMsg{seq: staleSeq, issues: []ticketStatus{{Key: "STALE-1"}}})
	pm = next.(pickerModel)
	if len(pm.epicSearchList.Items()) != 0 {
		t.Error("a stale epicSearchResultsMsg (superseded seq) must be dropped")
	}
}

func TestPickerModelStarFromSearchResultsAddsToStarredList(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	pm = starEpic(t, pm, ticketStatus{Key: "OTHER-1", StatusName: "Open", Summary: "Shared epic"})

	if pm.phase != phaseEpicSearch {
		t.Fatalf("phase after starring = %v, want phaseEpicSearch (stays put)", pm.phase)
	}
	st := readStarState()
	if len(st.Epics) != 1 || st.Epics[0].Key != "OTHER-1" {
		t.Fatalf("starred epics on disk = %v, want [OTHER-1]", st.Epics)
	}
	if len(pm.starredEpicList.Items()) != 1 {
		t.Fatalf("starredEpicList has %d items, want 1", len(pm.starredEpicList.Items()))
	}
}

func TestPickerModelStarringOwnEpicFromSearchIsANoop(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{}) // owned epic is TICKET-3008
	pm = starEpic(t, pm, ticketStatus{Key: "TICKET-3008", StatusName: "Backlog", Summary: "Tenant Access Vending"})

	st := readStarState()
	if len(st.Epics) != 0 {
		t.Errorf("starring your own epic from search must be a no-op, got %v", st.Epics)
	}
}

func TestPickerModelStarTwiceFromSearchTogglesUnstar(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	status := ticketStatus{Key: "OTHER-1", StatusName: "Open", Summary: "Shared epic"}
	pm = starEpic(t, pm, status)
	if len(readStarState().Epics) != 1 {
		t.Fatalf("precondition failed: expected one starred epic")
	}

	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	pm = next.(pickerModel)
	if len(readStarState().Epics) != 0 {
		t.Error("pressing s again on the same search result should unstar it")
	}
	if len(pm.starredEpicList.Items()) != 0 {
		t.Errorf("starredEpicList should be empty after toggling unstar, got %d items", len(pm.starredEpicList.Items()))
	}
}

func TestPickerModelUnstarFromStarredList(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	pm = starEpic(t, pm, ticketStatus{Key: "OTHER-1", StatusName: "Open", Summary: "Shared epic"})

	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseEpics {
		t.Fatalf("phase after esc from epic search = %v, want phaseEpics", pm.phase)
	}

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus the starred list
	pm = next.(pickerModel)
	if !pm.epicsOnStarred {
		t.Fatal("precondition failed: expected focus on the starred list")
	}
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	pm = next.(pickerModel)

	if len(readStarState().Epics) != 0 {
		t.Error("s on the starred list should unstar the selected epic")
	}
	if len(pm.starredEpicList.Items()) != 0 {
		t.Errorf("starredEpicList should be empty after unstarring, got %d items", len(pm.starredEpicList.Items()))
	}
}

func TestPickerModelTypingSInEpicSearchWithoutTabTypesNotStars(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	pm = next.(pickerModel)
	next, _ = pm.Update(epicSearchResultsMsg{seq: pm.epicSearchSeq, issues: []ticketStatus{{Key: "OTHER-1"}}})
	pm = next.(pickerModel)

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	pm = next.(pickerModel)

	if pm.epicSearchInput.Value() != "s" {
		t.Errorf("epicSearchInput value = %q, want %q (s must type, not star, before tab)", pm.epicSearchInput.Value(), "s")
	}
	if len(readStarState().Epics) != 0 {
		t.Error("typing s before tab must not star anything")
	}
}

func TestPickerModelEnterOnStarredListDrillsIntoStarredEpicNotOwned(t *testing.T) {
	src := &fakeJiraSource{}
	pm := epicsReady(t, src) // owned epic is TICKET-3008
	pm = starEpic(t, pm, ticketStatus{Key: "OTHER-1", StatusName: "Open", Summary: "Shared epic"})
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back to phaseEpics
	pm = next.(pickerModel)
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus starred list
	pm = next.(pickerModel)
	if !pm.epicsOnStarred {
		t.Fatal("precondition failed: expected focus on the starred list")
	}

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseEpicChildrenLoading {
		t.Fatalf("phase after enter on starred list = %v, want phaseEpicChildrenLoading", pm.phase)
	}
	if cmd == nil {
		t.Fatal("enter on the starred list must issue a command to load its children")
	}
	cmd() // runs loadEpicChildren, recording the key it was called with
	if len(src.epicChildrenCalls) != 1 || src.epicChildrenCalls[0] != "OTHER-1" {
		t.Fatalf("epicChildren calls = %v, want [OTHER-1] (the starred epic, not the owned one)", src.epicChildrenCalls)
	}
}

func TestPickerModelTabRoutesArrowKeysToTheFocusedListOnly(t *testing.T) {
	src := &fakeJiraSource{}
	pm := newTestPickerModel(t, src)
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	pm = next.(pickerModel)
	owned := []ticketStatus{{Key: "TICKET-1"}, {Key: "TICKET-2"}}
	next, _ = pm.Update(epicsLoadedMsg{epics: owned})
	pm = next.(pickerModel)
	pm = starEpic(t, pm, ticketStatus{Key: "OTHER-1"})
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	pm = starEpic(t, pm, ticketStatus{Key: "OTHER-2"})
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)

	ownedIndexBefore := pm.epicList.Index()
	starredIndexBefore := pm.starredEpicList.Index()

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus starred list
	pm = next.(pickerModel)
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyDown})
	pm = next.(pickerModel)

	if pm.epicList.Index() != ownedIndexBefore {
		t.Errorf("owned list cursor moved from %d to %d; it should be untouched while the starred list is focused", ownedIndexBefore, pm.epicList.Index())
	}
	if pm.starredEpicList.Index() == starredIndexBefore {
		t.Error("starred list cursor should move on KeyDown once it's focused")
	}
}

func TestPickerModelEpicsViewFitsWindowAndShowsBothTitles(t *testing.T) {
	pm := epicsReady(t, &fakeJiraSource{})
	next, _ := pm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	pm = next.(pickerModel)

	out := pm.View()
	if got := lipgloss.Height(out); got > 30 {
		t.Errorf("View() height = %d lines, want <= 30 (the WindowSizeMsg height)", got)
	}
	if !strings.Contains(out, "Your epics") {
		t.Error("View() must show the owned list's title")
	}
	if !strings.Contains(out, "Starred epics") {
		t.Error("View() must show the starred list's title")
	}
}

func TestPickerModelEpicsOnLoadRefreshesStarredStatuses(t *testing.T) {
	withTempStateDir(t)
	withTempConfigDir(t)
	var st starState
	st.add(ticketStatus{Key: "OTHER-1", StatusName: "Open", Summary: "Shared epic"})
	if err := writeStarState(st); err != nil {
		t.Fatalf("writeStarState: %v", err)
	}

	src := &fakeJiraSource{results: map[string]ticketStatus{
		"OTHER-1": {Key: "OTHER-1", StatusName: "Done", Summary: "Shared epic"},
	}}
	pm := newPickerModel(src)
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	pm = next.(pickerModel)
	next, cmd := pm.Update(epicsLoadedMsg{epics: nil})
	pm = next.(pickerModel)
	if len(pm.starredEpicList.Items()) != 1 {
		t.Fatalf("starredEpicList should show the cached entry immediately, got %d items", len(pm.starredEpicList.Items()))
	}
	if cmd == nil {
		t.Fatal("loading the epics tab with a starred epic must kick off a background refresh")
	}

	next, _ = pm.Update(starredEpicsRefreshedMsg{statuses: src.results})
	pm = next.(pickerModel)
	item, ok := pm.starredEpicList.Items()[0].(issueItem)
	if !ok || item.StatusName != "Done" {
		t.Fatalf("starred epic status after refresh = %+v, want StatusName Done", item)
	}
	onDisk := readStarState()
	if onDisk.Epics[0].Status.StatusName != "Done" {
		t.Errorf("refreshed status should be persisted, got %q", onDisk.Epics[0].Status.StatusName)
	}
}

func TestPickerModelFailedTransitionShowsErrorAndKeepsIssueUnchanged(t *testing.T) {
	src := &fakeJiraSource{}
	pm := detailReady(t, src)
	originalDetail := pm.detail
	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	pm = next.(pickerModel)
	next, _ = pm.Update(transitionsLoadedMsg{transitions: []transitionOption{{ID: "21", Name: "Close"}}})
	pm = next.(pickerModel)
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)

	next, _ = pm.Update(transitionAppliedMsg{err: errSignedOut})
	pm = next.(pickerModel)
	if pm.phase != phaseError {
		t.Fatalf("phase after failed transition = %v, want phaseError", pm.phase)
	}
	if pm.errReturnPhase != phaseDetail {
		t.Errorf("errReturnPhase = %v, want phaseDetail", pm.errReturnPhase)
	}

	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	pm = next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase after retrying from a failed transition = %v, want phaseDetail", pm.phase)
	}
	if pm.detail != originalDetail {
		t.Error("the issue's detail must be unchanged after a failed transition")
	}
}
