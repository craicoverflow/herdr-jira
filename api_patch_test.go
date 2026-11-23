package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withMovableClock pins nowFn to *now for the test; move it by assigning to *now.
func withMovableClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	orig := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = orig })
	return &now
}

func cachedKeys(rows []ticketStatus) string {
	var ks []string
	for _, r := range rows {
		ks = append(ks, r.Key+"="+r.StatusName)
	}
	return strings.Join(ks, ",")
}

func TestTransitionReadsTheIssueBackNotTheSearchIndex(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.myIssuesList = []ticketStatus{{Key: "A-1", StatusName: "To Do", StatusCategory: "new"}}
	callAPI(t, src, "issues")

	// The search index still has the old status; the issue itself has the new.
	src.results["A-1"] = ticketStatus{Key: "A-1", StatusName: "To Do", StatusCategory: "new"}
	src.full = issueFull{Key: "A-1", StatusName: "In Progress", StatusCategory: "indeterminate"}
	var got struct {
		Issue apiIssue `json:"issue"`
	}
	callAPI(t, src, "transition", "A-1", "21").into(t, &got)
	if got.Issue.Status != "In Progress" {
		t.Fatalf("reply status %q, want the issue's own", got.Issue.Status)
	}
	if c, _ := readIssuesCache(); cachedKeys(c.Rows) != "A-1=In Progress" {
		t.Fatalf("cache = %s", cachedKeys(c.Rows))
	}
}

func TestAFetchRunningDuringAChangeKeepsTheChange(t *testing.T) {
	withAPIState(t)
	now := withMovableClock(t)
	_ = writePickerCache([]ticketStatus{{Key: "A-1", StatusName: "To Do"}, {Key: "A-2", StatusName: "To Do"}}, nil)

	started := *now // a refresh begins...
	*now = now.Add(2 * time.Second)
	// ...and while it runs, A-1 moves on, A-2 is done, and B-1 is assigned.
	recordPatch(cachePatch{Op: patchStatus, Row: ticketStatus{Key: "A-1", StatusName: "In Progress", StatusCategory: "indeterminate"}})
	recordPatch(cachePatch{Op: patchStatus, Row: ticketStatus{Key: "A-2", StatusName: "Done", StatusCategory: "done"}})
	recordPatch(cachePatch{Op: patchMine, Row: ticketStatus{Key: "B-1", StatusName: "To Do"}})
	*now = now.Add(3 * time.Second)

	stale := []ticketStatus{{Key: "A-1", StatusName: "To Do"}, {Key: "A-2", StatusName: "To Do"}}
	issues, _ := storeFetchedIssues(started, stale, nil)
	if got := cachedKeys(issues); got != "B-1=To Do,A-1=In Progress" {
		t.Fatalf("shown after the fetch: %s", got)
	}
	if c, _ := readIssuesCache(); cachedKeys(c.Rows) != "B-1=To Do,A-1=In Progress" {
		t.Fatalf("cached after the fetch: %s", cachedKeys(c.Rows))
	}

	// A fetch begun well after the changes is believed as it is.
	*now = now.Add(patchGrace + time.Minute)
	issues, _ = storeFetchedIssues(*now, []ticketStatus{{Key: "A-1", StatusName: "Review"}}, nil)
	if got := cachedKeys(issues); got != "A-1=Review" {
		t.Fatalf("a later fetch should win: %s", got)
	}
	if c, _ := readIssuesCache(); len(c.Patches) != 0 {
		t.Fatalf("expired patches kept: %+v", c.Patches)
	}
}

func TestEpicsFetchKeepsAChangeMadeDuringIt(t *testing.T) {
	withAPIState(t)
	now := withMovableClock(t)
	_ = writeEpicsCache(epicsCache{FetchedAt: *now, CloudID: "cloud-1",
		Epics: []ticketStatus{{Key: "E-1", StatusName: "To Do"}}, Starred: []ticketStatus{{Key: "E-1", StatusName: "To Do"}}})
	started := *now
	recordPatch(cachePatch{Op: patchStatus, Row: ticketStatus{Key: "E-1", StatusName: "Done", StatusCategory: "done"}})
	c := storeFetchedEpics(started, epicsCache{FetchedAt: *now, CloudID: "cloud-1",
		Epics: []ticketStatus{{Key: "E-1", StatusName: "To Do"}}, Starred: []ticketStatus{{Key: "E-1", StatusName: "To Do"}}})
	if len(c.Epics) != 0 || cachedKeys(c.Starred) != "E-1=Done" {
		t.Fatalf("a Done epic leaves the assigned lane and stays starred: %+v", c)
	}
}

func TestAPIIssuesIgnoresAnotherSitesCache(t *testing.T) {
	withAPIState(t)
	_ = writeIssuesCache(issuesCache{FetchedAt: nowFn(), CloudID: "other-site", Rows: []ticketStatus{{Key: "X-1"}}})
	var got struct {
		Issues []apiIssue `json:"issues"`
		Cached bool       `json:"cached"`
	}
	callAPI(t, newFakeAPI(), "issues", "--cached").into(t, &got)
	if got.Cached || len(got.Issues) != 0 {
		t.Fatalf("another site's cache was shown: %+v", got)
	}
}

func TestLegacyIssuesCacheIsReadWithItsMtime(t *testing.T) {
	withAPIState(t)
	_ = os.MkdirAll(stateDir(), 0o700)
	if err := os.WriteFile(legacyIssuesCachePath(), []byte(`[{"Key":"A-1"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	_ = os.Chtimes(legacyIssuesCachePath(), old, old)
	c, ok := readIssuesCache()
	if !ok || len(c.Rows) != 1 || !c.FetchedAt.Equal(old) || c.fresh() {
		t.Fatalf("legacy cache: %+v ok=%v", c, ok)
	}
}

func TestAPIStarLeavesAnotherSitesEpicsCache(t *testing.T) {
	withAPIState(t)
	_ = writeEpicsCache(epicsCache{FetchedAt: nowFn(), CloudID: "other-site"})
	row := `{"key":"E-5","summary":"Five","status":"To Do","category":"new","type":"Epic","level":1}`
	callAPI(t, newFakeAPI(), "star", "E-5", "--row", row).into(t, &struct{}{})
	if c, _ := readEpicsCache(); len(c.StarredKeys) != 0 || len(c.Starred) != 0 {
		t.Fatalf("another site's cache got this site's star: %+v", c)
	}
}

func TestStartLoginKeepsAWorkerThatFailsAtOnce(t *testing.T) {
	withAPIState(t)
	origStart, origExe := startDetachedFn, executableFn
	t.Cleanup(func() { startDetachedFn, executableFn = origStart, origExe })
	executableFn = func() (string, error) { return "/bin/herdr-jira", nil }
	startDetachedFn = func(string, []string, string) (int, error) {
		// The worker can't open its listener and says so before we look.
		st, _ := readLoginState()
		_ = writeLoginState(loginState{State: "error", StartedAt: st.StartedAt, Error: &apiError{Code: "error", Message: "listen: address in use"}})
		return os.Getpid(), nil
	}
	if _, err := apiStartLogin(true); err != nil {
		t.Fatal(err)
	}
	st := apiSession()["login"].(loginState)
	if st.State != "error" || st.Error == nil || !strings.Contains(st.Error.Message, "address in use") {
		t.Fatalf("the worker's error was lost: %+v", st)
	}
}

func TestStartLoginReusesOneInProgress(t *testing.T) {
	withAPIState(t)
	origStart := startDetachedFn
	t.Cleanup(func() { startDetachedFn = origStart })
	started := 0
	startDetachedFn = func(string, []string, string) (int, error) { started++; return os.Getpid(), nil }
	_ = writeLoginState(loginState{State: "waiting", PID: os.Getpid(), StartedAt: nowFn(), URL: "https://auth"})
	got, err := apiStartLogin(true)
	if err != nil || started != 0 || got.(loginState).URL != "https://auth" {
		t.Fatalf("got %+v err %v, started %d workers", got, err, started)
	}
}

func TestALoginWhoseWorkerNeverStartedExpires(t *testing.T) {
	withAPIState(t)
	now := withMovableClock(t)
	st := loginState{State: "waiting", StartedAt: *now}
	if !loginInProgress(st) {
		t.Fatal("just started: in progress")
	}
	*now = now.Add(workerStartGrace + time.Second)
	if loginInProgress(st) {
		t.Fatal("a worker that never recorded its PID isn't running")
	}
}

func TestLogoutWithNothingStoredSucceeds(t *testing.T) {
	withAPIState(t)
	_ = writeLoginState(loginState{State: "waiting", PID: 999999, StartedAt: nowFn()})
	r := callAPI(t, newFakeAPI(), "logout")
	var got struct {
		SignedIn bool `json:"signedIn"`
	}
	r.into(t, &got)
	if got.SignedIn {
		t.Fatal("still signed in")
	}
	if _, ok := readLoginState(); ok {
		t.Fatal("login.json survived logout")
	}
}

func TestDemoLogoutRefusesTheRealStateFolder(t *testing.T) {
	withAPIState(t)
	demoMode = true
	t.Cleanup(func() { demoMode = false })
	_ = os.MkdirAll(stateDir(), 0o700)
	if _, err := apiLogout(); err == nil {
		t.Fatal("demo logout outside the demo folder should refuse")
	}
	if _, err := os.Stat(stateDir()); err != nil {
		t.Fatalf("state folder removed: %v", err)
	}
}

// ---- installer ----------------------------------------------------------------

func TestBindOpenKeyFollowsASymlinkAndKeepsTheMode(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles", "keys.json")
	_ = os.MkdirAll(filepath.Dir(real), 0o755)
	if err := os.WriteFile(real, []byte("{\n  \"global\": {}\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "keys.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := bindOpenKey(link, "cmd-j"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("keys.json is no longer a link: %v", err)
	}
	fi, _ := os.Stat(real)
	data, _ := os.ReadFile(real)
	if fi.Mode().Perm() != 0o644 || !strings.Contains(string(data), modAction) {
		t.Fatalf("mode %v, content %s", fi.Mode().Perm(), data)
	}
}

func TestUninstallRemovesABindingThatIsLastInItsSection(t *testing.T) {
	in := []byte("{\n  \"global\": {\n    \"cmd-k\": \"app.help\",\n    \"cmd-j\": \"" + modAction + "\"\n  }\n}\n")
	out, ok := unbindMod(in)
	if !ok || out == nil || strings.Contains(string(out), modAction) || !strings.Contains(string(out), "cmd-k") {
		t.Fatalf("ok=%v out=%s", ok, out)
	}
	if out, ok := unbindMod([]byte(`{"global":{"cmd-k":"app.help"}}`)); !ok || out != nil {
		t.Fatalf("nothing to remove: ok=%v out=%s", ok, out)
	}
}

func TestPruneKeepsFilesTheModDoesNotShip(t *testing.T) {
	dest := t.TempDir()
	for _, f := range []string{"main.js", "old.js", "notes.md", "icons/gone.svg", "icons/ticket.svg", "jsconfig.json", "gpui-kit.d.ts", ".bin/herdr-jira"} {
		p := filepath.Join(dest, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, nil, 0o644)
	}
	removed, err := pruneModFiles(dest, map[string]bool{"main.js": true, "icons/ticket.svg": true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "icons/gone.svg,old.js" {
		t.Fatalf("removed %v", removed)
	}
	for _, f := range []string{"notes.md", "jsconfig.json", "gpui-kit.d.ts", ".bin/herdr-jira", "main.js"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("%s was removed", f)
		}
	}
}
