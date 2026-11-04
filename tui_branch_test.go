package main

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// withFakeCreateBranch stubs createBranchFn (branch.go's seam over `git
// checkout -b`) so these tests never shell out, restoring it afterward.
func withFakeCreateBranch(t *testing.T, fn func(cwd, name string) error) {
	t.Helper()
	orig := createBranchFn
	createBranchFn = fn
	t.Cleanup(func() { createBranchFn = orig })
}

func pressRune(m pickerModel, r rune) (pickerModel, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	return next.(pickerModel), cmd
}

func TestBranchShortcutStartsWizard(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})

	next, cmd := pressRune(pm, 'b')
	if next.phase != phaseBranchPrep {
		t.Fatalf("phase after [b] = %v, want phaseBranchPrep", next.phase)
	}
	if next.branchKey != "TICKET-1" {
		t.Errorf("branchKey = %q, want TICKET-1", next.branchKey)
	}
	if next.branchReturnPhase != phaseReady {
		t.Errorf("branchReturnPhase = %v, want phaseReady", next.branchReturnPhase)
	}
	if cmd == nil {
		t.Fatal("[b] must issue a command to load prep data")
	}
}

func TestBranchPrepAsksBothQuestionsWhenNeeded(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, _ = pressRune(pm, 'b')

	next, cmd := pm.Update(branchPrepLoadedMsg{
		detail:      issueDetail{Key: "TICKET-1", StatusName: "Open"},
		transitions: []transitionOption{{ID: "31", Name: "Start", ToStatusName: "In Progress"}},
		accountID:   "acc1",
	})
	pm = next.(pickerModel)
	if pm.phase != phaseBranchSuffix {
		t.Fatalf("phase after prep loaded = %v, want phaseBranchSuffix", pm.phase)
	}
	if !pm.branchPendingAssign {
		t.Error("expected branchPendingAssign=true for an unassigned issue")
	}
	if !pm.branchPendingTransition {
		t.Error("expected branchPendingTransition=true when a transition to In Progress exists")
	}
	if pm.branchTransitionID != "31" {
		t.Errorf("branchTransitionID = %q, want 31", pm.branchTransitionID)
	}
	if cmd == nil {
		t.Fatal("expected textinput.Blink command")
	}
}

func TestBranchPrepSkipsQuestionsWhenAlreadyAssignedAndInProgress(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, _ = pressRune(pm, 'b')

	next, _ := pm.Update(branchPrepLoadedMsg{
		detail:    issueDetail{Key: "TICKET-1", StatusName: "In Progress", AssigneeAccountID: "acc1"},
		accountID: "acc1",
	})
	pm = next.(pickerModel)
	if pm.branchPendingAssign {
		t.Error("expected branchPendingAssign=false when already assigned to the signed-in user")
	}
	if pm.branchPendingTransition {
		t.Error("expected branchPendingTransition=false when already In Progress")
	}
}

func TestBranchPrepErrorShowsErrorScreen(t *testing.T) {
	pm := readyWithOneIssue(t, &fakeJiraSource{})
	pm, _ = pressRune(pm, 'b')

	next, _ := pm.Update(branchPrepLoadedMsg{err: errSignedOut})
	pm = next.(pickerModel)
	if pm.phase != phaseError {
		t.Fatalf("phase = %v, want phaseError", pm.phase)
	}
	if pm.errReturnPhase != phaseReady {
		t.Errorf("errReturnPhase = %v, want phaseReady", pm.errReturnPhase)
	}
}

// suffixReady drives a model to phaseBranchSuffix with both follow-up
// questions pending, ready for the suffix to be typed and submitted.
func suffixReady(t *testing.T, src jiraSource) pickerModel {
	t.Helper()
	pm := readyWithOneIssue(t, src)
	pm, _ = pressRune(pm, 'b')
	next, _ := pm.Update(branchPrepLoadedMsg{
		detail:      issueDetail{Key: "TICKET-1", StatusName: "Open"},
		transitions: []transitionOption{{ID: "31", Name: "Start", ToStatusName: "In Progress"}},
		accountID:   "acc1",
	})
	return next.(pickerModel)
}

func TestBranchSuffixEscCancelsWithoutCreating(t *testing.T) {
	called := false
	withFakeCreateBranch(t, func(cwd, name string) error { called = true; return nil })
	pm := suffixReady(t, &fakeJiraSource{})

	next, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEsc})
	pm = next.(pickerModel)
	if pm.phase != phaseReady {
		t.Fatalf("phase after esc = %v, want phaseReady", pm.phase)
	}
	if called {
		t.Error("esc from the suffix screen must not create a branch")
	}
}

func TestBranchSuffixEmptyEnterIsNoop(t *testing.T) {
	pm := suffixReady(t, &fakeJiraSource{})
	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseBranchSuffix {
		t.Fatalf("phase after enter with empty suffix = %v, want phaseBranchSuffix", pm.phase)
	}
	if cmd != nil {
		t.Error("expected no command for an empty suffix")
	}
}

func TestBranchCreateFailureShowsError(t *testing.T) {
	withFakeCreateBranch(t, func(cwd, name string) error { return errors.New("not a git repository") })
	pm := suffixReady(t, &fakeJiraSource{})
	pm.cwd = "/repo"
	pm.branchSuffixInput.SetValue("start-spec")

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if pm.phase != phaseBranchCreating {
		t.Fatalf("phase after enter = %v, want phaseBranchCreating", pm.phase)
	}
	if cmd == nil {
		t.Fatal("enter must issue a command to create the branch")
	}
	msg := cmd()
	next, _ = pm.Update(msg)
	pm = next.(pickerModel)
	if pm.phase != phaseError {
		t.Fatalf("phase after failed create = %v, want phaseError", pm.phase)
	}
	if pm.errReturnPhase != phaseReady {
		t.Errorf("errReturnPhase = %v, want phaseReady", pm.errReturnPhase)
	}
}

// TestBranchFullFlowCreatesAssignsAndTransitions drives the wizard end to
// end: suffix -> create -> "assign to me?" yes -> "transition?" yes ->
// applied -> back on the list.
func TestBranchFullFlowCreatesAssignsAndTransitions(t *testing.T) {
	var createdCwd, createdName string
	withFakeCreateBranch(t, func(cwd, name string) error {
		createdCwd, createdName = cwd, name
		return nil
	})
	src := &fakeJiraSource{accountIDResult: "acc1"}
	pm := suffixReady(t, src)
	pm.cwd = "/repo"
	pm.branchSuffixInput.SetValue("Start Spec")

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	if cmd == nil {
		t.Fatal("expected a command to create the branch")
	}
	next, _ = pm.Update(cmd())
	pm = next.(pickerModel)
	if createdCwd != "/repo" || createdName != "TICKET-1-start-spec" {
		t.Fatalf("createBranchFn called with (%q, %q), want (/repo, TICKET-1-start-spec)", createdCwd, createdName)
	}
	if pm.phase != phaseBranchConfirm || pm.branchConfirmKind != branchConfirmAssign {
		t.Fatalf("phase/kind after create = (%v, %v), want (phaseBranchConfirm, branchConfirmAssign)", pm.phase, pm.branchConfirmKind)
	}

	pm, _ = pressRune(pm, 'y')
	if pm.phase != phaseBranchConfirm || pm.branchConfirmKind != branchConfirmTransition {
		t.Fatalf("phase/kind after assign=yes = (%v, %v), want (phaseBranchConfirm, branchConfirmTransition)", pm.phase, pm.branchConfirmKind)
	}

	next2, cmd := pressRune(pm, 'y')
	pm = next2
	if pm.phase != phaseBranchApplying {
		t.Fatalf("phase after transition=yes = %v, want phaseBranchApplying", pm.phase)
	}
	if cmd == nil {
		t.Fatal("expected a command to apply assign+transition")
	}
	next, _ = pm.Update(cmd())
	pm = next.(pickerModel)

	if pm.phase != phaseReady {
		t.Fatalf("phase after follow-up applied = %v, want phaseReady", pm.phase)
	}
	if len(src.assignCalls) != 1 || src.assignCalls[0] != "TICKET-1" {
		t.Errorf("assignCalls = %v, want one call for TICKET-1", src.assignCalls)
	}
	if len(src.transitionCalls) != 1 || src.transitionCalls[0] != "31" {
		t.Errorf("transitionCalls = %v, want one call with ID 31", src.transitionCalls)
	}
}

// TestBranchPartialFailureSaysWhatAlreadyHappened: if the branch was
// created and assigned but the transition then fails, the error shown must
// say so — a bare Jira error would read as if nothing had happened yet.
func TestBranchPartialFailureSaysWhatAlreadyHappened(t *testing.T) {
	withFakeCreateBranch(t, func(cwd, name string) error { return nil })
	src := &fakeJiraSource{accountIDResult: "acc1", transitionErr: errors.New("workflow condition not met")}
	pm := suffixReady(t, src)
	pm.cwd = "/repo"
	pm.branchSuffixInput.SetValue("start-spec")

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	next, _ = pm.Update(cmd())
	pm = next.(pickerModel)

	pm, _ = pressRune(pm, 'y') // assign: yes
	next2, cmd := pressRune(pm, 'y')
	pm = next2 // transition: yes
	next, _ = pm.Update(cmd())
	pm = next.(pickerModel)

	if pm.phase != phaseError {
		t.Fatalf("phase = %v, want phaseError", pm.phase)
	}
	if !strings.Contains(pm.err.Error(), "TICKET-1-start-spec") || !strings.Contains(pm.err.Error(), "assigned to you") {
		t.Errorf("err = %q, want it to mention the branch name and the assign that already succeeded", pm.err)
	}
}

func TestBranchConfirmNoSkipsThatFollowup(t *testing.T) {
	withFakeCreateBranch(t, func(cwd, name string) error { return nil })
	src := &fakeJiraSource{accountIDResult: "acc1"}
	pm := suffixReady(t, src)
	pm.cwd = "/repo"
	pm.branchSuffixInput.SetValue("start-spec")

	next, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = next.(pickerModel)
	next, _ = pm.Update(cmd())
	pm = next.(pickerModel)

	pm, _ = pressRune(pm, 'n') // skip assign
	next2, cmd := pressRune(pm, 'n')
	pm = next2 // skip transition -> nothing to apply, should land directly
	if pm.phase != phaseReady {
		t.Fatalf("phase after skipping both follow-ups = %v, want phaseReady", pm.phase)
	}
	if cmd == nil {
		t.Fatal("expected a status-message command even with nothing applied")
	}
	if len(src.assignCalls) != 0 || len(src.transitionCalls) != 0 {
		t.Errorf("expected no Jira writes when both follow-ups are skipped, got assign=%v transition=%v", src.assignCalls, src.transitionCalls)
	}
}

func TestBranchShortcutFromEpicsListReturnsToEpics(t *testing.T) {
	src := &fakeJiraSource{myEpicsList: []ticketStatus{{Key: "TICKET-9", StatusName: "Open"}}}
	m := newTestPickerModel(t, src)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	pm := next.(pickerModel)
	next, _ = pm.Update(epicsLoadedMsg{epics: src.myEpicsList})
	pm = next.(pickerModel)

	pm, _ = pressRune(pm, 'b')
	if pm.phase != phaseBranchPrep || pm.branchReturnPhase != phaseEpics {
		t.Fatalf("phase/returnPhase = (%v, %v), want (phaseBranchPrep, phaseEpics)", pm.phase, pm.branchReturnPhase)
	}
}
