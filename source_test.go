package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fixtureText reads a testdata fixture and strips the _fixture_note(s)
// used to document it, since Jira's real responses don't have them.
func fixtureText(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", name, err)
	}
	delete(m, "_fixture_note")
	delete(m, "_fixture_note2")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("remarshal fixture %s: %v", name, err)
	}
	return string(out)
}

// sseToolResult wraps a tool result's inner text payload (itself JSON) in
// the SSE-framed JSON-RPC envelope the live endpoint actually returns, per
// the Task 0a spike's confirmed shape.
func sseToolResult(innerJSON string) string {
	textJSON, _ := json.Marshal(innerJSON)
	envelope := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":%s}],"isError":false}}`, textJSON)
	return "event: message\ndata: " + envelope + "\n\n"
}

// newFakeMCPServer answers "initialize" trivially and "tools/call" by
// dispatching toolName -> fixed inner-JSON response.
func newFakeMCPServer(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "sess-test")
		w.WriteHeader(http.StatusOK)
		if req.Method == "initialize" {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
			return
		}
		inner, ok := responses[req.Params.Name]
		if !ok {
			t.Fatalf("unexpected tool call: %s", req.Params.Name)
		}
		fmt.Fprint(w, sseToolResult(inner))
	}))
}

func withSignedInCredentials(t *testing.T) {
	t.Helper()
	withTempConfigDir(t)
	if err := saveCredentials(credentials{
		AccessToken: "tok", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour),
		CloudID: "cloud-1", SiteURL: "https://example.atlassian.net",
	}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
}

func TestRealJiraSourceSearchDecodesFixture(t *testing.T) {
	withSignedInCredentials(t)
	srv := newFakeMCPServer(t, map[string]string{
		"searchJiraIssuesUsingJql": fixtureText(t, "search_response.json"),
	})
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	got, err := src.search(t.Context(), []string{"PROJ-9001", "PROJ-9002"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got["PROJ-9001"].StatusName != "In Progress" {
		t.Errorf("PROJ-9001 status = %q, want In Progress", got["PROJ-9001"].StatusName)
	}
	if got["PROJ-9002"].StatusCategory != "done" {
		t.Errorf("PROJ-9002 category = %q, want done", got["PROJ-9002"].StatusCategory)
	}
}

func TestRealJiraSourceTransitionsDecodesFixture(t *testing.T) {
	withSignedInCredentials(t)
	srv := newFakeMCPServer(t, map[string]string{
		"getTransitionsForJiraIssue": fixtureText(t, "transitions_response.json"),
	})
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	got, err := src.transitions(t.Context(), "PROJ-9001")
	if err != nil {
		t.Fatalf("transitions: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d transitions, want 4", len(got))
	}
	if got[2].Name != "Closed" || got[2].ToStatusCategory != "done" {
		t.Errorf("transitions[2] = %+v, want Closed/done", got[2])
	}
}

func TestRealJiraSourceSearchScopedBuildsExpectedJQL(t *testing.T) {
	const issuesClause = `(issuetype in standardIssueTypes() OR issuetype in subTaskIssueTypes()) AND issuetype != Epic AND statusCategory != Done`
	const epicsClause = `issuetype = Epic AND statusCategory != Done`
	cases := []struct {
		name  string
		scope searchScope
		query string
		want  string
	}{
		{"issues, full key", scopeIssues, "proj-1234", issuesClause + ` AND key ~ "PROJ-1234*" ORDER BY key ASC`},
		{"issues, partial key", scopeIssues, "PROJ-300", issuesClause + ` AND key ~ "PROJ-300*" ORDER BY key ASC`},
		{"issues, free text", scopeIssues, "widget", issuesClause + ` AND summary ~ "widget*"`},
		{"issues, quote", scopeIssues, `wid"get`, issuesClause + ` AND summary ~ "wid\"get*"`},
		{"issues, backslash", scopeIssues, `wid\get`, issuesClause + ` AND summary ~ "wid\\get*"`},
		{"epics, full key", scopeEpics, "OTHER-7", epicsClause + ` AND key ~ "OTHER-7*" ORDER BY key ASC`},
		{"epics, free text", scopeEpics, "widget", epicsClause + ` AND summary ~ "widget*"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withSignedInCredentials(t)
			srv, jqls, maxes := laneServer(t, nil)
			src := &realJiraSource{client: srv.Client()}
			if _, err := src.searchScoped(t.Context(), tc.query, tc.scope); err != nil {
				t.Fatalf("searchScoped: %v", err)
			}
			if len(*jqls) != 1 || (*jqls)[0] != tc.want {
				t.Errorf("jql = %q, want %q", *jqls, tc.want)
			}
			if (*maxes)[0] != 50 {
				t.Errorf("maxResults = %d, want 50", (*maxes)[0])
			}
		})
	}
}

func TestRealJiraSourceSearchScopedEmptyQueryDoesNotCallJira(t *testing.T) {
	withSignedInCredentials(t)
	srv, jqls, _ := laneServer(t, nil)
	src := &realJiraSource{client: srv.Client()}
	issues, err := src.searchScoped(t.Context(), "   ", scopeIssues)
	if err != nil {
		t.Fatalf("searchScoped: %v", err)
	}
	if issues != nil || len(*jqls) != 0 {
		t.Errorf("issues = %v, calls = %v, want nil and no calls", issues, *jqls)
	}
}

func TestRealJiraSourceSearchScopedIssuesDropsEpicsAboveLevelZero(t *testing.T) {
	withSignedInCredentials(t)
	srv, _, _ := laneServer(t, map[string]string{"standardIssueTypes()": "issues_standard.json"})
	src := &realJiraSource{client: srv.Client()}
	got, err := src.searchScoped(t.Context(), "doc", scopeIssues)
	if err != nil {
		t.Fatalf("searchScoped: %v", err)
	}
	for _, iss := range got {
		if iss.HierarchyLevel > 0 {
			t.Errorf("%s (level %d) leaked into the Issues scope", iss.Key, iss.HierarchyLevel)
		}
	}
	if len(got) == 0 {
		t.Fatal("expected the non-epic rows to survive")
	}
}

func TestRealJiraSourceMoreFlagFollowsIsLast(t *testing.T) {
	withSignedInCredentials(t)
	// paging_page1.json is a real response with isLast: false.
	srv, _, _ := laneServer(t, map[string]string{"standardIssueTypes()": "paging_page1.json"})
	src := &realJiraSource{client: srv.Client()}
	_, subtasks, issuesMore, subtasksMore, err := src.myIssues(t.Context())
	if err != nil {
		t.Fatalf("myIssues: %v", err)
	}
	if !issuesMore {
		t.Error("issuesMore = false, want true when Jira reports isLast=false")
	}
	if subtasksMore || len(subtasks) != 0 {
		t.Errorf("subtasksMore = %v with %d rows, want false (empty result has no isLast)", subtasksMore, len(subtasks))
	}
}

func TestRealJiraSourceMyEpicsBuildsExpectedJQL(t *testing.T) {
	withSignedInCredentials(t)
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					JQL string `json:"jql"`
				} `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if req.Method == "initialize" {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
			return
		}
		gotJQL = req.Params.Arguments.JQL
		fmt.Fprint(w, sseToolResult(`{"issues":[]}`))
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	if _, _, err := src.myEpics(t.Context()); err != nil {
		t.Fatalf("myEpics: %v", err)
	}
	want := `assignee = currentUser() AND issuetype = Epic AND statusCategory != Done ORDER BY updated DESC`
	if gotJQL != want {
		t.Errorf("jql = %q, want %q", gotJQL, want)
	}
}

func TestRealJiraSourceEpicChildrenBuildsExpectedJQL(t *testing.T) {
	withSignedInCredentials(t)
	var gotJQL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					JQL string `json:"jql"`
				} `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if req.Method == "initialize" {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
			return
		}
		gotJQL = req.Params.Arguments.JQL
		fmt.Fprint(w, sseToolResult(`{"issues":[]}`))
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	if _, err := src.epicChildren(t.Context(), "PROJ-3008"); err != nil {
		t.Fatalf("epicChildren: %v", err)
	}
	want := `parent = PROJ-3008 ORDER BY updated DESC`
	if gotJQL != want {
		t.Errorf("jql = %q, want %q", gotJQL, want)
	}
}

func TestRealJiraSourceEpicChildrenRejectsNonKeyShapedEpic(t *testing.T) {
	withSignedInCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("epicChildren must not call Jira for a non-key-shaped epic")
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	if _, err := src.epicChildren(t.Context(), "not an epic"); err == nil {
		t.Fatal("epicChildren: want error for a non-key-shaped epic, got nil")
	}
}

func TestRealJiraSourceAssignSendsAccountId(t *testing.T) {
	withSignedInCredentials(t)
	var gotIssueKey string
	var gotFields struct {
		Assignee struct {
			AccountID string `json:"accountId"`
		} `json:"assignee"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					IssueIdOrKey string          `json:"issueIdOrKey"`
					Fields       json.RawMessage `json:"fields"`
				} `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if req.Method == "initialize" {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
			return
		}
		if req.Params.Name != "editJiraIssue" {
			t.Fatalf("unexpected tool call: %s", req.Params.Name)
		}
		gotIssueKey = req.Params.Arguments.IssueIdOrKey
		json.Unmarshal(req.Params.Arguments.Fields, &gotFields)
		fmt.Fprint(w, sseToolResult(`{"key":"PROJ-9001"}`))
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	if err := src.assign(t.Context(), "PROJ-9001", "account-123"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if gotIssueKey != "PROJ-9001" {
		t.Errorf("issueIdOrKey = %q, want PROJ-9001", gotIssueKey)
	}
	if gotFields.Assignee.AccountID != "account-123" {
		t.Errorf("fields.assignee.accountId = %q, want account-123", gotFields.Assignee.AccountID)
	}
}

func TestEnsureAccountIDCachesAfterFirstCall(t *testing.T) {
	withSignedInCredentials(t)
	srv := newFakeMCPServer(t, map[string]string{
		"atlassianUserInfo": `{"name":"Example User","email":"user@example.com","account_id":"account-123"}`,
	})
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	client := srv.Client()
	id, err := ensureAccountID(t.Context(), client)
	if err != nil {
		t.Fatalf("ensureAccountID: %v", err)
	}
	if id != "account-123" {
		t.Fatalf("accountID = %q, want account-123", id)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if creds.AccountID != "account-123" {
		t.Fatalf("cached AccountID = %q, want account-123", creds.AccountID)
	}

	srv.Close() // a second call must use the cache, not hit the (now-dead) server
	id2, err := ensureAccountID(t.Context(), client)
	if err != nil {
		t.Fatalf("ensureAccountID (cached): %v", err)
	}
	if id2 != "account-123" {
		t.Fatalf("cached accountID = %q, want account-123", id2)
	}
}

func TestRealJiraSourceTransitionAppliesAndSucceeds(t *testing.T) {
	withSignedInCredentials(t)
	srv := newFakeMCPServer(t, map[string]string{
		"transitionJiraIssue": `{"message":"ok"}`,
	})
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	if err := src.transition(t.Context(), "PROJ-9001", "31"); err != nil {
		t.Fatalf("transition: %v", err)
	}
}

// laneServer answers searchJiraIssuesUsingJql by matching a substring of the
// JQL to a fixture file, and records each JQL and maxResults it receives.
func laneServer(t *testing.T, byJQLSubstring map[string]string) (srv *httptest.Server, jqls *[]string, maxes *[]int) {
	t.Helper()
	jqls, maxes = &[]string{}, &[]int{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Arguments struct {
					JQL        string `json:"jql"`
					MaxResults int    `json:"maxResults"`
				} `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if req.Method == "initialize" {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
			return
		}
		*jqls = append(*jqls, req.Params.Arguments.JQL)
		*maxes = append(*maxes, req.Params.Arguments.MaxResults)
		for sub, file := range byJQLSubstring {
			if strings.Contains(req.Params.Arguments.JQL, sub) {
				data, err := os.ReadFile("testdata/stage0/" + file)
				if err != nil {
					t.Fatalf("read %s: %v", file, err)
				}
				fmt.Fprint(w, sseToolResult(string(data)))
				return
			}
		}
		fmt.Fprint(w, sseToolResult(`{"issues":[]}`))
	}))
	t.Cleanup(srv.Close)
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	t.Cleanup(func() { mcpEndpoint = orig })
	return srv, jqls, maxes
}

func TestRealJiraSourceMyIssuesRunsBothLaneQueriesAndDropsEpics(t *testing.T) {
	withSignedInCredentials(t)
	// The standard-types fixture is the real Stage 0 response, which
	// includes Epics (standardIssueTypes() does not exclude them).
	srv, jqls, _ := laneServer(t, map[string]string{
		"standardIssueTypes()": "issues_standard.json",
		"subTaskIssueTypes()":  "issues_subtask.json",
	})
	src := &realJiraSource{client: srv.Client()}
	issues, subtasks, _, _, err := src.myIssues(t.Context())
	if err != nil {
		t.Fatalf("myIssues: %v", err)
	}
	got := append(append([]ticketStatus{}, issues...), subtasks...)
	if len(*jqls) != 2 {
		t.Fatalf("jql calls = %d, want 2: %v", len(*jqls), *jqls)
	}
	wantStd := `assignee = currentUser() AND issuetype in standardIssueTypes() AND issuetype != Epic AND statusCategory != Done ORDER BY updated DESC`
	wantSub := `assignee = currentUser() AND issuetype in subTaskIssueTypes() AND statusCategory != Done ORDER BY updated DESC`
	if (*jqls)[0] != wantStd || (*jqls)[1] != wantSub {
		t.Errorf("jqls = %q, want [%q %q]", *jqls, wantStd, wantSub)
	}
	var keys []string
	for _, iss := range got {
		if iss.IssueType == "Epic" {
			t.Errorf("epic %s leaked into myIssues", iss.Key)
		}
		keys = append(keys, iss.Key)
	}
	// 7 non-epic rows in the standard fixture, then the sub-task last.
	if len(got) != 8 || keys[len(keys)-1] != "PROJ-3" {
		t.Fatalf("keys = %v, want 7 issues then PROJ-3", keys)
	}
}

func TestRealJiraSourceRunSearchDecodesTypeAndParent(t *testing.T) {
	withSignedInCredentials(t)
	srv, _, _ := laneServer(t, map[string]string{"issuetype in standardIssueTypes()": "issues_standard.json"})
	src := &realJiraSource{client: srv.Client()}
	got, _, err := src.runSearch(t.Context(), "issuetype in standardIssueTypes()", 50)
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	byKey := map[string]ticketStatus{}
	for _, iss := range got {
		byKey[iss.Key] = iss
	}
	if g := byKey["TEAM-10"]; g.IssueType != "Task" || g.ParentKey != "TEAM-5" || g.HierarchyLevel != 0 {
		t.Errorf("TEAM-10 = %+v, want Task with parent TEAM-5", g)
	}
	if g := byKey["TEAM-5"]; g.IssueType != "Epic" || g.HierarchyLevel != 1 || g.StatusCategory != "done" {
		t.Errorf("TEAM-5 = %+v, want done Epic at level 1", g)
	}
}

func TestRealJiraSourceSearchDropsUnknownKeys(t *testing.T) {
	withSignedInCredentials(t)
	srv, jqls, _ := laneServer(t, map[string]string{"key in": "key_in_mixed.json"})
	src := &realJiraSource{client: srv.Client()}
	got, err := src.search(t.Context(), []string{"PR-4567", "PROJ-1"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 || got["PROJ-1"].Key != "PROJ-1" {
		t.Errorf("got = %v, want only PROJ-1", got)
	}
	if strings.Contains((*jqls)[0], "statusCategory") {
		t.Errorf("search must not filter by status: %q", (*jqls)[0])
	}
}

func TestRealJiraSourceSearchChunksKeys(t *testing.T) {
	withSignedInCredentials(t)
	srv, jqls, maxes := laneServer(t, nil)
	src := &realJiraSource{client: srv.Client()}
	keys := make([]string, 120)
	for i := range keys {
		keys[i] = fmt.Sprintf("PROJ-%d", i+1)
	}
	if _, err := src.search(t.Context(), keys); err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(*jqls) != 3 {
		t.Fatalf("calls = %d, want 3 (50+50+20)", len(*jqls))
	}
	if (*maxes)[0] != 50 || (*maxes)[2] != 20 {
		t.Errorf("maxResults = %v, want [50 50 20]", *maxes)
	}
}
