package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeAPISource is fakeJiraSource plus the app-only calls.
type fakeAPISource struct {
	*fakeJiraSource
	full     issueFull
	fullErr  error
	user     userInfo
	fullKeys []string

	projectList    []jiraProject
	projectErr     error
	projectCalls   int
	typeList       []jiraIssueType
	typeErr        error
	typeCalls      int
	created        []createRequest
	createdAccount []string
	createKey      string
	createErr      error
}

func (f *fakeAPISource) getFull(ctx context.Context, key string) (issueFull, error) {
	f.fullKeys = append(f.fullKeys, key)
	if f.full.Key == "" {
		if r, ok := f.results[key]; ok {
			return issueFull{Key: r.Key, Summary: r.Summary, StatusName: r.StatusName, StatusCategory: r.StatusCategory,
				IssueType: r.IssueType, HierarchyLevel: r.HierarchyLevel, Priority: r.Priority}, nil
		}
	}
	return f.full, f.fullErr
}

// writePickerCache writes a fresh Issues cache, for tests.
func writePickerCache(issues, subtasks []ticketStatus) error {
	return writeIssuesCache(issuesCache{FetchedAt: nowFn(), Rows: append(append([]ticketStatus{}, issues...), subtasks...)})
}

func readPickerCache() []ticketStatus {
	c, _ := readIssuesCache()
	return c.Rows
}

func (f *fakeAPISource) projects(ctx context.Context) ([]jiraProject, error) {
	f.projectCalls++
	return f.projectList, f.projectErr
}

func (f *fakeAPISource) issueTypes(ctx context.Context, project string) ([]jiraIssueType, error) {
	f.typeCalls++
	return f.typeList, f.typeErr
}

func (f *fakeAPISource) createIssue(ctx context.Context, r createRequest, accountID string) (string, error) {
	f.created = append(f.created, r)
	f.createdAccount = append(f.createdAccount, accountID)
	return f.createKey, f.createErr
}

func (f *fakeAPISource) userInfo(ctx context.Context) (userInfo, error) { return f.user, nil }

func newFakeAPI() *fakeAPISource {
	return &fakeAPISource{fakeJiraSource: &fakeJiraSource{results: map[string]ticketStatus{}}}
}

// withAPIState gives a test its own state and config folders and a fixed
// cloud ID, and restores the seams afterwards.
func withAPIState(t *testing.T) {
	t.Helper()
	withTempStateDir(t)
	withTempConfigDir(t)
	origCloud, origEnsure := cloudIDFn, ensureCloudIDFn
	cloudIDFn = func() string { return "cloud-1" }
	ensureCloudIDFn = func(context.Context) (string, error) { return "cloud-1", nil }
	t.Cleanup(func() { cloudIDFn, ensureCloudIDFn = origCloud, origEnsure })
}

// callAPI runs one op and decodes its reply.
func callAPI(t *testing.T, src apiSource, args ...string) apiReplyDecoded {
	t.Helper()
	var buf bytes.Buffer
	if err := runAPI(context.Background(), src, args, &buf); err != nil {
		t.Fatalf("runAPI %v: %v", args, err)
	}
	if strings.Count(strings.TrimSpace(buf.String()), "\n") != 0 {
		t.Fatalf("reply is not one line: %q", buf.String())
	}
	var r apiReplyDecoded
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	return r
}

type apiReplyDecoded struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *apiError       `json:"error"`
}

func (r apiReplyDecoded) into(t *testing.T, out any) {
	t.Helper()
	if !r.OK {
		t.Fatalf("expected ok, got error %+v", r.Error)
	}
	if err := json.Unmarshal(r.Data, out); err != nil {
		t.Fatalf("decode data %s: %v", r.Data, err)
	}
}

func TestAPIUsageErrorsAreReplies(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	for _, args := range [][]string{{}, {"nope"}, {"issue"}, {"issue", "not a key"}, {"issue", "PROJ-1) OR (key=X"}, {"transition", "PROJ-1"}, {"issues", "--bogus"}} {
		r := callAPI(t, src, args...)
		if r.OK || r.Error == nil || r.Error.Code != "usage" {
			t.Errorf("%v: want a usage error, got %+v", args, r)
		}
	}
	if len(src.fullKeys) != 0 {
		t.Errorf("a bad key reached Jira: %v", src.fullKeys)
	}
}

func TestClassifyAPIError(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{errSignedOut, "signed_out"},
		{errNoCredentials, "signed_out"},
		{errConsentBlocked, "consent_blocked"},
		{errMultipleSites{names: []string{"a"}}, "multiple_sites"},
		{errNoSite, "no_site"},
		{errNotCloud, "not_cloud"},
		{context.DeadlineExceeded, "network"},
		{errors.New("boom"), "error"},
	}
	for _, c := range cases {
		if got := classifyAPIError(c.err).Code; got != c.want {
			t.Errorf("%v: got %s, want %s", c.err, got, c.want)
		}
	}
	wrapped := errors.Join(errors.New("tool"), errToolMissing)
	if got := classifyAPIError(wrapped).Code; got != "tool_missing" {
		t.Errorf("tool missing: got %s", got)
	}
}

func TestAPIIssuesCachedAutoRefresh(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.myIssuesList = []ticketStatus{{Key: "A-1", Summary: "one", StatusName: "To Do", StatusCategory: "new", Priority: "High"}}
	src.mySubtasksList = []ticketStatus{{Key: "A-2", Summary: "two", HierarchyLevel: -1, ParentKey: "A-1"}}

	var got struct {
		Issues   []apiIssue `json:"issues"`
		Subtasks []apiIssue `json:"subtasks"`
		Cached   bool       `json:"cached"`
		Fresh    bool       `json:"fresh"`
	}
	callAPI(t, src, "issues", "--cached").into(t, &got)
	if got.Cached || len(got.Issues) != 0 {
		t.Fatalf("--cached with no cache: %+v", got)
	}

	callAPI(t, src, "issues").into(t, &got) // no cache: fetches
	if !got.Fresh || len(got.Issues) != 1 || got.Issues[0].Priority != "High" || len(got.Subtasks) != 1 || got.Subtasks[0].Parent != "A-1" {
		t.Fatalf("auto fetch: %+v", got)
	}

	src.myIssuesList = nil
	callAPI(t, src, "issues").into(t, &got) // fresh cache: no fetch
	if len(got.Issues) != 1 {
		t.Fatalf("auto within the TTL should answer from the cache: %+v", got)
	}
	callAPI(t, src, "issues", "--refresh").into(t, &got)
	if len(got.Issues) != 0 {
		t.Fatalf("--refresh should ask Jira: %+v", got)
	}
}

func TestAPIIssuesAutoRefetchesStaleCache(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.myIssuesList = []ticketStatus{{Key: "A-1"}}
	callAPI(t, src, "issues")
	c, _ := readIssuesCache()
	c.FetchedAt = time.Now().Add(-2 * issuesCacheTTL)
	if err := writeIssuesCache(c); err != nil {
		t.Fatal(err)
	}
	src.myIssuesList = []ticketStatus{{Key: "A-1"}, {Key: "A-9"}}
	var got struct {
		Issues []apiIssue `json:"issues"`
	}
	callAPI(t, src, "issues").into(t, &got)
	if len(got.Issues) != 2 {
		t.Fatalf("a stale cache should be refetched: %+v", got)
	}
}

func TestAPIEpicsIncludesStars(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.myEpicsList = []ticketStatus{{Key: "E-1", HierarchyLevel: 1}}
	src.results["S-1"] = ticketStatus{Key: "S-1", HierarchyLevel: 1, StatusName: "In Progress", StatusCategory: "indeterminate"}
	if err := writeStars("cloud-1", []starEntry{{Key: "S-1"}, {Key: "GONE-1"}}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Epics       []apiIssue `json:"epics"`
		Starred     []apiIssue `json:"starred"`
		StarredKeys []string   `json:"starredKeys"`
	}
	callAPI(t, src, "epics").into(t, &got)
	if len(got.Epics) != 1 || len(got.Starred) != 1 || got.Starred[0].Key != "S-1" || len(got.StarredKeys) != 2 {
		t.Fatalf("epics: %+v", got)
	}
	if len(src.calls) != 1 || strings.Join(src.calls[0], ",") != "S-1,GONE-1" {
		t.Fatalf("stars should be one batched lookup, got %v", src.calls)
	}
}

func TestAPITransitionPatchesCachesAndDropsDone(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.myIssuesList = []ticketStatus{{Key: "A-1", StatusName: "To Do", StatusCategory: "new", Priority: "High"}, {Key: "A-2", StatusName: "To Do", StatusCategory: "new"}}
	callAPI(t, src, "issues")
	before, _ := readIssuesCache()

	src.results["A-1"] = ticketStatus{Key: "A-1", StatusName: "In Progress", StatusCategory: "indeterminate"}
	var got struct {
		Issue apiIssue `json:"issue"`
	}
	callAPI(t, src, "transition", "a-1", "21").into(t, &got)
	if got.Issue.Status != "In Progress" || strings.Join(src.transitionCalls, ",") != "21" {
		t.Fatalf("transition reply %+v, calls %v", got, src.transitionCalls)
	}
	rows := readPickerCache()
	if rows[0].StatusName != "In Progress" || rows[0].Priority != "High" {
		t.Fatalf("cache row not patched (or lost its priority): %+v", rows[0])
	}
	after, _ := readIssuesCache()
	if !after.FetchedAt.Equal(before.FetchedAt) {
		t.Errorf("a local patch must not extend the cache's TTL")
	}

	src.results["A-2"] = ticketStatus{Key: "A-2", StatusName: "Done", StatusCategory: "done"}
	callAPI(t, src, "transition", "A-2", "41")
	for _, r := range readPickerCache() {
		if r.Key == "A-2" {
			t.Fatalf("a Done issue should leave the cached lane")
		}
	}
}

func TestAPIAssignAddsToCache(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.accountIDResult = "me"
	src.myIssuesList = []ticketStatus{{Key: "A-1"}}
	callAPI(t, src, "issues")
	src.results["B-7"] = ticketStatus{Key: "B-7", StatusName: "To Do", StatusCategory: "new"}
	callAPI(t, src, "assign", "B-7").into(t, &struct{}{})
	if strings.Join(src.assignCalls, ",") != "B-7" {
		t.Fatalf("assign calls %v", src.assignCalls)
	}
	rows := readPickerCache()
	if len(rows) != 2 || rows[0].Key != "B-7" {
		t.Fatalf("assigned issue should lead the cached lane: %+v", rows)
	}
}

func TestAPIStarToggleWithRow(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	callAPI(t, src, "epics")
	row := `{"key":"E-5","summary":"Five","status":"To Do","category":"new","type":"Epic","level":1}`
	var got struct {
		Starred bool `json:"starred"`
	}
	callAPI(t, src, "star", "E-5", "--row", row).into(t, &got)
	if !got.Starred || len(src.calls) != 0 {
		t.Fatalf("star with a row: %+v, lookups %v", got, src.calls)
	}
	c, _ := readEpicsCache()
	if len(c.Starred) != 1 || c.Starred[0].Summary != "Five" || strings.Join(c.StarredKeys, ",") != "E-5" {
		t.Fatalf("epics cache after star: %+v", c)
	}
	callAPI(t, src, "star", "E-5").into(t, &got)
	if got.Starred || len(src.calls) != 0 {
		t.Fatalf("unstar should need no lookup: %+v %v", got, src.calls)
	}
	if c, _ := readEpicsCache(); len(c.Starred) != 0 || len(c.StarredKeys) != 0 {
		t.Fatalf("epics cache after unstar: %+v", c)
	}
}

func TestAPITicketsConfirmsAndCaches(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.results["PROJ-12"] = ticketStatus{Key: "PROJ-12", StatusName: "In Progress", StatusCategory: "indeterminate"}
	var got struct {
		Tickets map[string]*apiIssue `json:"tickets"`
	}
	callAPI(t, src, "tickets", "--branch", "pr-4567-PROJ-12-fix", "--branch", "main").into(t, &got)
	if got.Tickets["pr-4567-PROJ-12-fix"] == nil || got.Tickets["pr-4567-PROJ-12-fix"].Key != "PROJ-12" {
		t.Fatalf("ticket not confirmed: %+v", got.Tickets)
	}
	if v, ok := got.Tickets["main"]; !ok || v != nil {
		t.Fatalf("a branch with no key should be null: %+v", got.Tickets)
	}
	calls := len(src.calls)
	callAPI(t, src, "tickets", "--branch", "pr-4567-PROJ-12-fix").into(t, &got)
	if len(src.calls) != calls {
		t.Fatalf("a fresh answer should not ask Jira again: %v", src.calls)
	}
	if got.Tickets["pr-4567-PROJ-12-fix"].Status != "In Progress" {
		t.Fatalf("cached status lost: %+v", got.Tickets)
	}
}

func TestAPITicketsSignedOutIsAnError(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.searchErr = errSignedOut
	r := callAPI(t, src, "tickets", "--branch", "PROJ-1-x")
	if r.OK || r.Error.Code != "signed_out" {
		t.Fatalf("want signed_out, got %+v", r)
	}
}

func TestAPIIssueDetail(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.accountIDResult = "me"
	src.full = issueFull{Key: "A-1", Summary: "S", StatusName: "To Do", StatusCategory: "new", AssigneeAccountID: "me", Description: "**hi**",
		Subtasks: []ticketStatus{{Key: "A-2"}}, Comments: []issueComment{{Author: "Bo", Body: "ok"}}}
	var got map[string]any
	callAPI(t, src, "issue", "A-1").into(t, &got)
	if got["mine"] != true || got["description"] != "**hi**" || len(got["subtasks"].([]any)) != 1 || len(got["comments"].([]any)) != 1 {
		t.Fatalf("detail: %+v", got)
	}
	if labels, ok := got["labels"].([]any); !ok || len(labels) != 0 {
		t.Fatalf("labels should be an empty list, got %#v", got["labels"])
	}
}

func TestAPISessionAndLogout(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	var got map[string]any
	callAPI(t, src, "session").into(t, &got)
	if got["signedIn"] != false {
		t.Fatalf("no credentials: %+v", got)
	}
	if err := saveCredentials(credentials{ClientID: "c", RefreshToken: "r", SiteURL: "https://x.atlassian.net", CloudID: "cloud-1"}); err != nil {
		t.Fatal(err)
	}
	callAPI(t, src, "session").into(t, &got)
	if got["signedIn"] != true || got["siteURL"] != "https://x.atlassian.net" {
		t.Fatalf("signed in: %+v", got)
	}
	if strings.Contains(mustJSON(t, got), "\"r\"") {
		t.Fatalf("session must never carry tokens: %+v", got)
	}
	callAPI(t, src, "logout").into(t, &got)
	if _, err := loadCredentials(); !errors.Is(err, errNoCredentials) {
		t.Fatalf("logout left credentials: %v", err)
	}
}

func TestLoginStateTimesOut(t *testing.T) {
	withAPIState(t)
	if err := writeLoginState(loginState{State: "waiting", StartedAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	s := apiSession()
	st := s["login"].(loginState)
	if st.State != "error" || st.Error.Code != "timeout" {
		t.Fatalf("an abandoned sign-in should read as timed out: %+v", st)
	}
	if loginInProgress(loginState{State: "waiting", StartedAt: time.Now(), PID: os.Getpid()}) != true {
		t.Fatalf("a live worker's sign-in is in progress")
	}
}

func TestDemoSourceWorkflow(t *testing.T) {
	withTempStateDir(t)
	src := demoSource{}
	ctx := context.Background()
	issues, subtasks, _, _, _ := src.myIssues(ctx)
	if len(issues) == 0 || len(subtasks) == 0 {
		t.Fatalf("demo has issues and sub-tasks: %d %d", len(issues), len(subtasks))
	}
	trs, _ := src.transitions(ctx, "WEB-151")
	var done string
	for _, tr := range trs {
		if tr.ToStatusCategory == "done" {
			done = tr.ID
		}
	}
	if err := src.transition(ctx, "WEB-151", done); err != nil {
		t.Fatal(err)
	}
	found, _ := src.search(ctx, []string{"WEB-151"})
	if found["WEB-151"].StatusCategory != "done" {
		t.Fatalf("transition did not stick: %+v", found["WEB-151"])
	}
	full, err := src.getFull(ctx, "WEB-142")
	if err != nil || full.ParentKey != "WEB-100" || len(full.Subtasks) != 2 || full.Description == "" {
		t.Fatalf("demo detail: %+v %v", full, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAPITicketsCustomPatternOnlySendsKeys(t *testing.T) {
	withAPIState(t)
	dir := withTempConfigDir(t)
	os.WriteFile(dir+"/config.json", []byte(`{"key_pattern": "^([^_]+)_"}`), 0o600)
	src := newFakeAPI()
	src.results["AB-1"] = ticketStatus{Key: "AB-1"}
	var got struct {
		Tickets map[string]*apiIssue `json:"tickets"`
	}
	callAPI(t, src, "tickets", "--branch", "AB-1_fix", "--branch", "x) OR (y_z").into(t, &got)
	if got.Tickets["AB-1_fix"] == nil || got.Tickets["x) OR (y_z"] != nil {
		t.Fatalf("tickets: %+v", got.Tickets)
	}
	for _, c := range src.calls {
		for _, k := range c {
			if !keyShapedRE.MatchString(k) {
				t.Fatalf("a non-key reached the JQL lookup: %q", k)
			}
		}
	}
}
