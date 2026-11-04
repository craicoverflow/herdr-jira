// Command herdr-jira is a herdr plugin: it shows Jira issue status in the
// terminal and lets you transition them, talking to Jira as a standalone
// MCP client against mcp.atlassian.com — no LLM in the path.
//
// See docs/SPEC.md for the design.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

const usage = `herdr-jira — Jira issues in herdr

  login            sign in via the browser (debugging entry point; the
                   open/issue actions sign you in automatically on first use)
  status           who you're signed in as, and your Jira site
  tick [--force]   refresh the sidebar labels ($ticket, $ticket_badge, …);
                   herdr runs this on its events
  action NAME      what a herdr action runs: open, issue
  picker           the popup itself
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-jira:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "login":
		return signIn(ctx)
	case "status":
		return status(ctx)
	case "tick":
		cfg, err := loadConfig()
		if err != nil {
			fmt.Fprintln(os.Stderr, "herdr-jira: tick: ignoring config error:", err)
		}
		force := len(args) > 1 && args[1] == "--force"
		if err := tick(ctx, cfg, force, newJiraSource()); err != nil && !isQuietTickErr(err) {
			return err
		}
		return nil
	case "picker":
		initialKey := os.Getenv("HERDR_JIRA_TICKET_KEY")
		noKeyBranch := os.Getenv("HERDR_JIRA_NO_KEY_BRANCH")
		cwd := os.Getenv("HERDR_JIRA_CWD")
		return runPickerFor(ctx, newJiraSource(), initialKey, noKeyBranch, cwd)
	case "action":
		if len(args) < 2 {
			return fmt.Errorf("action needs a name")
		}
		return runAction(args[1])
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runAction(name string) error {
	switch name {
	case "open":
		inv := invocationContext()
		cwd := inv.WorkspaceCwd
		if cwd == "" {
			cwd = inv.FocusedPaneCwd
		}
		env := map[string]string{}
		if cwd != "" {
			env["HERDR_JIRA_CWD"] = cwd
		}
		if err := openPopup(env); err != nil {
			notify("Jira", "Couldn't open the picker: "+err.Error())
			return err
		}
		return nil
	case "issue":
		inv := invocationContext()
		cwd := inv.WorkspaceCwd
		if cwd == "" {
			cwd = inv.FocusedPaneCwd
		}
		cfg, _ := loadConfig()
		if err := openPopup(issuePopupEnv(cwd, cfg)); err != nil {
			notify("Jira", "Couldn't open this agent's ticket: "+err.Error())
			return err
		}
		return nil
	}
	return fmt.Errorf("unknown action %q", name)
}

// issuePopupEnv is the "issue" action's branch resolution, as env vars for
// the picker process: the ticket key if the current branch has one, or
// the branch name (for a "no ticket in branch X" notice) if it doesn't —
// plus cwd itself, so the picker can create a branch (the [b] shortcut)
// in the right repo regardless of how it was invoked.
func issuePopupEnv(cwd string, cfg config) map[string]string {
	env := map[string]string{}
	if cwd == "" {
		return env
	}
	env["HERDR_JIRA_CWD"] = cwd
	if key, ok := resolveTicketKey(cwd, cfg); ok {
		env["HERDR_JIRA_TICKET_KEY"] = key
	} else if branch, ok := currentBranch(cwd); ok {
		env["HERDR_JIRA_NO_KEY_BRANCH"] = branch
	}
	return env
}
