package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"testing"
)

func newTestPickerModelFor(t *testing.T, src jiraSource, initialKeys []string, noKeyBranch string) pickerModel {
	t.Helper()
	withTempStateDir(t)
	withTempConfigDir(t)
	return newPickerModelFor(src, initialKeys, noKeyBranch)
}

func TestIssueActionOpensDirectlyOnKeyedTicket(t *testing.T) {
	src := &fakeJiraSource{getResult: issueDetail{Key: "PROJ-1234", StatusName: "In Progress"}}
	m := newTestPickerModelFor(t, src, []string{"PROJ-1234"}, "")

	next, _ := m.Update(initialDetailMsg{detail: src.getResult})
	pm := next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase = %v, want phaseDetail", pm.phase)
	}
	if pm.detail == nil || pm.detail.Key != "PROJ-1234" {
		t.Errorf("detail = %+v, want PROJ-1234", pm.detail)
	}
}

func TestIssueActionNoKeyShowsNoticeAndList(t *testing.T) {
	src := &fakeJiraSource{myIssuesList: []ticketStatus{{Key: "PROJ-1", StatusName: "Open"}}}
	m := newTestPickerModelFor(t, src, nil, "no-ticket-branch")

	next, cmd := m.Update(initialNoKeyMsg{branch: "no-ticket-branch"})
	pm := next.(pickerModel)
	if pm.notice == "" {
		t.Fatal("expected a notice about the keyless branch")
	}
	if cmd == nil {
		t.Fatal("expected a command to load the list as a fallback")
	}

	next, _ = pm.Update(issuesLoadedMsg{issues: src.myIssuesList})
	pm = next.(pickerModel)
	if pm.phase != phaseReady {
		t.Fatalf("phase = %v, want phaseReady", pm.phase)
	}
}

func TestIssueActionUnknownKeyFallsBackToListWithNotice(t *testing.T) {
	src := &fakeJiraSource{myIssuesList: []ticketStatus{{Key: "PROJ-1", StatusName: "Open"}}}
	m := newTestPickerModelFor(t, src, []string{"PROJ-9999"}, "")

	next, cmd := m.Update(initialDetailMsg{key: "PROJ-9999", err: errSignedOut})
	pm := next.(pickerModel)
	if pm.notice == "" {
		t.Fatal("expected a notice about the unresolvable key")
	}
	if pm.phase == phaseError {
		t.Fatal("an unknown/unreachable key during the issue action should fall back to the list, not a hard error screen")
	}
	if cmd == nil {
		t.Fatal("expected a command to load the list as a fallback")
	}

	next, _ = pm.Update(issuesLoadedMsg{issues: src.myIssuesList})
	pm = next.(pickerModel)
	if pm.phase != phaseReady {
		t.Fatalf("phase = %v, want phaseReady", pm.phase)
	}
}

func TestIssueActionDetailLoadDoesNotKnockOutBackgroundListLoad(t *testing.T) {
	src := &fakeJiraSource{getResult: issueDetail{Key: "PROJ-1234", StatusName: "Open"}}
	m := newTestPickerModelFor(t, src, []string{"PROJ-1234"}, "")
	next, _ := m.Update(initialDetailMsg{detail: src.getResult})
	pm := next.(pickerModel)

	// The background list load lands while still on the detail screen; it
	// must not knock the user back to the list.
	next, _ = pm.Update(issuesLoadedMsg{issues: []ticketStatus{{Key: "PROJ-1234"}}})
	pm = next.(pickerModel)
	if pm.phase != phaseDetail {
		t.Fatalf("phase after background list load = %v, want still phaseDetail", pm.phase)
	}

	// esc from detail now has a populated list to fall back to.
	next, _ = pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseReady {
		t.Fatalf("phase after esc = %v, want phaseReady", pm.phase)
	}
	if len(pm.list.Items()) != 1 {
		t.Errorf("list has %d items, want 1 (from the background load)", len(pm.list.Items()))
	}
}
