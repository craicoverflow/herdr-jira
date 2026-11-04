package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestIssuePopupEnvCandidatePath(t *testing.T) {
	dir := gitRepo(t, "PROJ-4242-widgets")
	env := issuePopupEnv(dir, withConfigDefaults(config{}))
	if env["HERDR_JIRA_TICKET_CANDIDATES"] != "PROJ-4242" {
		t.Errorf("env = %v, want HERDR_JIRA_TICKET_CANDIDATES=PROJ-4242", env)
	}
	if env["HERDR_JIRA_NO_KEY_BRANCH"] != "PROJ-4242-widgets" {
		t.Errorf("env = %v, want HERDR_JIRA_NO_KEY_BRANCH set to the branch", env)
	}
	if _, ok := env["HERDR_JIRA_TICKET_KEY"]; ok {
		t.Error("HERDR_JIRA_TICKET_KEY must not be set on the candidate path")
	}
	if env["HERDR_JIRA_CWD"] != dir {
		t.Errorf("env[HERDR_JIRA_CWD] = %q, want %q (the [b] shortcut needs it to run git in the right repo)", env["HERDR_JIRA_CWD"], dir)
	}
}

func TestIssuePopupEnvTwoCandidatesKeepBranchOrder(t *testing.T) {
	dir := gitRepo(t, "pr-9-PROJ-5-fix")
	env := issuePopupEnv(dir, withConfigDefaults(config{}))
	if env["HERDR_JIRA_TICKET_CANDIDATES"] != "PR-9,PROJ-5" {
		t.Errorf("candidates = %q, want PR-9,PROJ-5", env["HERDR_JIRA_TICKET_CANDIDATES"])
	}
}

func TestIssuePopupEnvUserPatternPath(t *testing.T) {
	dir := gitRepo(t, "acme-7-widgets")
	env := issuePopupEnv(dir, config{KeyPattern: `(?i)\b(ACME-[0-9]+)\b`})
	if env["HERDR_JIRA_TICKET_KEY"] != "ACME-7" {
		t.Errorf("env = %v, want HERDR_JIRA_TICKET_KEY=ACME-7", env)
	}
	if _, ok := env["HERDR_JIRA_TICKET_CANDIDATES"]; ok {
		t.Error("candidates must not be set with a user pattern")
	}
	if _, ok := env["HERDR_JIRA_NO_KEY_BRANCH"]; ok {
		t.Error("HERDR_JIRA_NO_KEY_BRANCH should not be set alongside a resolved key")
	}
}

func TestIssuePopupEnvNoKeyBranch(t *testing.T) {
	dir := gitRepo(t, "no-ticket-here")
	env := issuePopupEnv(dir, withConfigDefaults(config{}))
	if env["HERDR_JIRA_NO_KEY_BRANCH"] != "no-ticket-here" {
		t.Errorf("env = %v, want HERDR_JIRA_NO_KEY_BRANCH=no-ticket-here", env)
	}
	if _, ok := env["HERDR_JIRA_TICKET_KEY"]; ok {
		t.Error("HERDR_JIRA_TICKET_KEY should not be set when the branch has no key")
	}
	if _, ok := env["HERDR_JIRA_TICKET_CANDIDATES"]; ok {
		t.Error("HERDR_JIRA_TICKET_CANDIDATES should not be set when the branch has no candidates")
	}
}

func TestIssuePopupEnvEmptyCwd(t *testing.T) {
	env := issuePopupEnv("", withConfigDefaults(config{}))
	if len(env) != 0 {
		t.Errorf("env = %v, want empty for an unknown cwd", env)
	}
}

// runStart runs the picker's start command and returns its message.
func runStart(t *testing.T, src jiraSource, keys []string, branch string) tea.Msg {
	t.Helper()
	withSignedInCredentials(t)
	withTempStateDir(t)
	return checkSignInThenStart(src, keys, branch)()
}

func TestPickerStartOpensFirstConfirmedCandidate(t *testing.T) {
	src := &fakeJiraSource{
		results:   map[string]ticketStatus{"PROJ-5": {Key: "PROJ-5"}},
		getResult: issueDetail{Key: "PROJ-5"},
	}
	msg := runStart(t, src, []string{"PR-9", "PROJ-5"}, "pr-9-PROJ-5")
	d, ok := msg.(initialDetailMsg)
	if !ok || d.err != nil || d.detail.Key != "PROJ-5" {
		t.Fatalf("msg = %#v, want initialDetailMsg for PROJ-5", msg)
	}
}

func TestPickerStartShowsNoKeyNoticeWhenNothingConfirmed(t *testing.T) {
	src := &fakeJiraSource{results: map[string]ticketStatus{}}
	msg := runStart(t, src, []string{"PR-4567"}, "pr-4567-fix")
	n, ok := msg.(initialNoKeyMsg)
	if !ok || n.branch != "pr-4567-fix" {
		t.Fatalf("msg = %#v, want initialNoKeyMsg for the branch", msg)
	}
}

func TestPickerStartFallsBackWhenLookupFails(t *testing.T) {
	src := &fakeJiraSource{searchErr: errors.New("boom")}
	msg := runStart(t, src, []string{"PROJ-5"}, "PROJ-5-x")
	if _, ok := msg.(initialNoKeyMsg); !ok {
		t.Fatalf("msg = %#v, want initialNoKeyMsg", msg)
	}
}
