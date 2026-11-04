package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// fakeJiraSource records every search call and answers from a fixed map,
// so tests can assert exactly one batched call happened.
type fakeJiraSource struct {
	results        map[string]ticketStatus
	calls          [][]string
	searchErr      error
	myIssuesList   []ticketStatus
	mySubtasksList []ticketStatus
	myIssuesMore   bool
	mySubtasksMore bool
	myIssuesErr    error

	searchScopedCalls  []scopedCall // queries passed to searchScoped, in order
	searchScopedResult []ticketStatus
	searchScopedErr    error

	getResult       issueDetail
	getErr          error
	transitionsList []transitionOption
	transitionsErr  error
	transitionErr   error
	transitionCalls []string // transition IDs applied, in order

	myEpicsList        []ticketStatus
	myEpicsMore        bool
	myEpicsErr         error
	epicChildrenCalls  []string // epic keys passed to epicChildren, in order
	epicChildrenResult []ticketStatus
	epicChildrenErr    error
	assignCalls        []string // issue keys passed to assign, in order
	assignErr          error
	accountIDResult    string
	accountIDErr       error
}

func (f *fakeJiraSource) accountID(ctx context.Context) (string, error) {
	return f.accountIDResult, f.accountIDErr
}

// scopedCall records one searchScoped call.
type scopedCall struct {
	query string
	scope searchScope
}

func (f *fakeJiraSource) myEpics(ctx context.Context) ([]ticketStatus, bool, error) {
	return f.myEpicsList, f.myEpicsMore, f.myEpicsErr
}

func (f *fakeJiraSource) epicChildren(ctx context.Context, epicKey string) ([]ticketStatus, error) {
	f.epicChildrenCalls = append(f.epicChildrenCalls, epicKey)
	return f.epicChildrenResult, f.epicChildrenErr
}

func (f *fakeJiraSource) assign(ctx context.Context, key, accountID string) error {
	f.assignCalls = append(f.assignCalls, key)
	return f.assignErr
}

func (f *fakeJiraSource) myIssues(ctx context.Context) ([]ticketStatus, []ticketStatus, bool, bool, error) {
	return f.myIssuesList, f.mySubtasksList, f.myIssuesMore, f.mySubtasksMore, f.myIssuesErr
}

func (f *fakeJiraSource) searchScoped(ctx context.Context, query string, scope searchScope) ([]ticketStatus, error) {
	f.searchScopedCalls = append(f.searchScopedCalls, scopedCall{query, scope})
	return f.searchScopedResult, f.searchScopedErr
}

func (f *fakeJiraSource) get(ctx context.Context, key string) (issueDetail, error) {
	return f.getResult, f.getErr
}

func (f *fakeJiraSource) transitions(ctx context.Context, key string) ([]transitionOption, error) {
	return f.transitionsList, f.transitionsErr
}

func (f *fakeJiraSource) transition(ctx context.Context, key, transitionID string) error {
	f.transitionCalls = append(f.transitionCalls, transitionID)
	return f.transitionErr
}

func (f *fakeJiraSource) search(ctx context.Context, keys []string) (map[string]ticketStatus, error) {
	f.calls = append(f.calls, append([]string(nil), keys...))
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	out := map[string]ticketStatus{}
	for _, k := range keys {
		if s, ok := f.results[k]; ok {
			out[k] = s
		}
	}
	return out, nil
}

// fakeHerdr simulates the herdr socket: fixed workspace/pane lists, and a
// record of every report_metadata call for assertions.
type fakeHerdr struct {
	workspaces []workspaceInfo
	panes      []paneInfo
	reports    []reportCall
}

type reportCall struct {
	method string
	params map[string]any
}

func (f *fakeHerdr) call(method string, params any, out any) error {
	switch method {
	case "workspace.list":
		data, _ := json.Marshal(map[string]any{"workspaces": f.workspaces})
		return json.Unmarshal(data, out)
	case "pane.list":
		data, _ := json.Marshal(map[string]any{"panes": f.panes})
		return json.Unmarshal(data, out)
	default:
		data, _ := json.Marshal(params)
		var m map[string]any
		json.Unmarshal(data, &m)
		f.reports = append(f.reports, reportCall{method: method, params: m})
		return nil
	}
}

func withTempStateDir(t *testing.T) {
	t.Helper()
	t.Setenv("HERDR_PLUGIN_STATE_DIR", t.TempDir())
}

func withFakeHerdr(t *testing.T) *fakeHerdr {
	t.Helper()
	f := &fakeHerdr{}
	orig := herdrCallFn
	herdrCallFn = f.call
	t.Cleanup(func() { herdrCallFn = orig })
	return f
}

func workspaceAt(id, checkoutPath string) workspaceInfo {
	w := workspaceInfo{WorkspaceID: id}
	w.Worktree = &struct {
		CheckoutPath string `json:"checkout_path"`
	}{CheckoutPath: checkoutPath}
	return w
}

func TestTickSetsTicketBadgeForBranchWithKey(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	dir := gitRepo(t, "PROJ-1234-foo")
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", dir)}
	herdr.panes = []paneInfo{{PaneID: "pane-1", WorkspaceID: "ws-1", Agent: "claude"}}

	src := &fakeJiraSource{results: map[string]ticketStatus{
		"PROJ-1234": {Key: "PROJ-1234", StatusName: "In Progress"},
	}}

	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(herdr.reports) != 2 {
		t.Fatalf("got %d report calls, want 2 (workspace + pane)", len(herdr.reports))
	}
	wsReport := herdr.reports[0]
	if wsReport.method != "workspace.report_metadata" {
		t.Errorf("method = %q, want workspace.report_metadata", wsReport.method)
	}
	tokens := wsReport.params["tokens"].(map[string]any)
	if tokens["ticket_badge"] != "PROJ-1234 In Progress" {
		t.Errorf("ticket_badge = %v, want %q", tokens["ticket_badge"], "PROJ-1234 In Progress")
	}
	paneReport := herdr.reports[1]
	if paneReport.method != "pane.report_metadata" || paneReport.params["pane_id"] != "pane-1" {
		t.Errorf("pane report = %+v", paneReport)
	}
}

func TestTickBatchesAllSpacesIntoOneSearchCall(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	dir1 := gitRepo(t, "PROJ-1-a")
	dir2 := gitRepo(t, "PROJ-2-b")
	herdr.workspaces = []workspaceInfo{
		workspaceAt("ws-1", dir1),
		workspaceAt("ws-2", dir2),
	}

	src := &fakeJiraSource{results: map[string]ticketStatus{
		"PROJ-1": {Key: "PROJ-1", StatusName: "Open"},
		"PROJ-2": {Key: "PROJ-2", StatusName: "Open"},
	}}

	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(src.calls) != 1 {
		t.Fatalf("got %d search calls, want exactly 1 for %d spaces", len(src.calls), len(herdr.workspaces))
	}
	if len(src.calls[0]) != 2 {
		t.Errorf("search call keys = %v, want 2 distinct keys", src.calls[0])
	}
}

func TestTickClearsTokensWhenBranchHasNoKey(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	dir := gitRepo(t, "no-ticket-here")
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", dir)}

	src := &fakeJiraSource{}
	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if len(src.calls) != 0 {
		t.Errorf("expected no search call when no space has a key, got %d", len(src.calls))
	}
	if len(herdr.reports) != 1 {
		t.Fatalf("got %d report calls, want 1", len(herdr.reports))
	}
	tokens := herdr.reports[0].params["tokens"].(map[string]any)
	for _, n := range tokenNames {
		if tokens[n] != nil {
			t.Errorf("token %q = %v, want nil (cleared)", n, tokens[n])
		}
	}
}

func TestTickClearsTokensWhenSwitchingAwayFromKeyedBranch(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	// Space starts on a keyed branch...
	dir := gitRepo(t, "PROJ-99-x")
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", dir)}
	src := &fakeJiraSource{results: map[string]ticketStatus{
		"PROJ-99": {Key: "PROJ-99", StatusName: "Open"},
	}}
	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}
	tokens := herdr.reports[len(herdr.reports)-1].params["tokens"].(map[string]any)
	if tokens["ticket"] == nil {
		t.Fatal("precondition failed: expected a ticket token before switching branches")
	}

	// ...then switches to one with no key.
	runGit(t, dir, "checkout", "-q", "-b", "no-key-branch")
	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick (second): %v", err)
	}
	last := herdr.reports[len(herdr.reports)-1].params["tokens"].(map[string]any)
	for _, n := range tokenNames {
		if last[n] != nil {
			t.Errorf("after switching away from the keyed branch, token %q = %v, want nil", n, last[n])
		}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func tokensOf(t *testing.T, r reportCall) map[string]any {
	t.Helper()
	return r.params["tokens"].(map[string]any)
}

func TestTickPRNumberBranchShowsNoLabel(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", gitRepo(t, "pr-4567-fix"))}
	// Jira knows no PR-4567, so the lookup drops the candidate.
	src := &fakeJiraSource{results: map[string]ticketStatus{}}

	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(src.calls) != 1 {
		t.Fatalf("search calls = %d, want 1 (the lookup)", len(src.calls))
	}
	if tokensOf(t, herdr.reports[0])["ticket_badge"] != nil {
		t.Errorf("tokens = %v, want cleared", tokensOf(t, herdr.reports[0]))
	}
}

func TestTickUserPatternSkipsLookup(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", gitRepo(t, "acme-7-widgets"))}
	src := &fakeJiraSource{results: map[string]ticketStatus{
		"ACME-7": {Key: "ACME-7", StatusName: "Open"},
	}}
	cfg := withConfigDefaults(config{KeyPattern: `(?i)\b(ACME-[0-9]+)\b`})

	if err := tick(context.Background(), cfg, true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(src.calls) != 1 {
		t.Fatalf("search calls = %d, want exactly 1 (status fetch, no key lookup)", len(src.calls))
	}
	if tokensOf(t, herdr.reports[0])["ticket_badge"] != "ACME-7 Open" {
		t.Errorf("tokens = %v", tokensOf(t, herdr.reports[0]))
	}
	if _, err := os.Stat(keyCachePath()); err == nil {
		t.Error("keys.json written for a user pattern")
	}
}

func TestTickTwoWorktreesCostOneLookupCall(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	herdr.workspaces = []workspaceInfo{
		workspaceAt("ws-1", gitRepo(t, "PROJ-1-a")),
		workspaceAt("ws-2", gitRepo(t, "PROJ-2-b")),
	}
	src := &fakeJiraSource{results: map[string]ticketStatus{
		"PROJ-1": {Key: "PROJ-1", StatusName: "Open"},
		"PROJ-2": {Key: "PROJ-2", StatusName: "Review"},
	}}

	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if len(src.calls) != 1 {
		t.Fatalf("search calls = %d, want 1 for both worktrees (lookup seeds the status cache)", len(src.calls))
	}
	if tokensOf(t, herdr.reports[0])["ticket_badge"] != "PROJ-1 Open" || tokensOf(t, herdr.reports[1])["ticket_badge"] != "PROJ-2 Review" {
		t.Errorf("reports = %+v", herdr.reports)
	}
}

func TestTickDoneTicketIsStillConfirmedAndLabelled(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", gitRepo(t, "PROJ-9-old"))}
	src := &fakeJiraSource{results: map[string]ticketStatus{
		"PROJ-9": {Key: "PROJ-9", StatusName: "Done", StatusCategory: "done"},
	}}

	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if tokensOf(t, herdr.reports[0])["ticket_badge"] != "PROJ-9 Done" {
		t.Errorf("tokens = %v, want the Done ticket labelled", tokensOf(t, herdr.reports[0]))
	}
}

func TestTickSignedOutKeepsLastLabel(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", gitRepo(t, "PROJ-3-x"))}
	src := &fakeJiraSource{results: map[string]ticketStatus{
		"PROJ-3": {Key: "PROJ-3", StatusName: "Open"},
	}}
	if err := tick(context.Background(), withConfigDefaults(config{}), true, src); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	src.searchErr = errSignedOut
	err := tick(context.Background(), withConfigDefaults(config{}), true, src)
	if !isQuietTickErr(err) {
		t.Fatalf("err = %v, want a quiet signed-out error", err)
	}
	last := herdr.reports[len(herdr.reports)-1]
	if tokensOf(t, last)["ticket_badge"] != "PROJ-3 Open" {
		t.Errorf("tokens = %v, want the last label kept", tokensOf(t, last))
	}
}
