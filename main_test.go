package main

import "testing"

func TestIssuePopupEnvKeyedBranch(t *testing.T) {
	dir := gitRepo(t, "TICKET-4242-widgets")
	env := issuePopupEnv(dir, withConfigDefaults(config{}))
	if env["HERDR_JIRA_TICKET_KEY"] != "TICKET-4242" {
		t.Errorf("env = %v, want HERDR_JIRA_TICKET_KEY=TICKET-4242", env)
	}
	if _, ok := env["HERDR_JIRA_NO_KEY_BRANCH"]; ok {
		t.Error("HERDR_JIRA_NO_KEY_BRANCH should not be set alongside a resolved key")
	}
	if env["HERDR_JIRA_CWD"] != dir {
		t.Errorf("env[HERDR_JIRA_CWD] = %q, want %q (the [b] shortcut needs it to run git in the right repo)", env["HERDR_JIRA_CWD"], dir)
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
}

func TestIssuePopupEnvEmptyCwd(t *testing.T) {
	env := issuePopupEnv("", withConfigDefaults(config{}))
	if len(env) != 0 {
		t.Errorf("env = %v, want empty for an unknown cwd", env)
	}
}
