package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	got, err := src.search(t.Context(), []string{"TICKET-9001", "TICKET-9002"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got["TICKET-9001"].StatusName != "In Progress" {
		t.Errorf("TICKET-9001 status = %q, want In Progress", got["TICKET-9001"].StatusName)
	}
	if got["TICKET-9002"].StatusCategory != "done" {
		t.Errorf("TICKET-9002 category = %q, want done", got["TICKET-9002"].StatusCategory)
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
	got, err := src.transitions(t.Context(), "TICKET-9001")
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

func TestRealJiraSourceSearchTextBuildsExpectedJQL(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"full key, upper", "TICKET-1234", `key ~ "TICKET-1234*" ORDER BY key ASC`},
		{"full key, lower", "TICKET-1234", `key ~ "TICKET-1234*" ORDER BY key ASC`},
		{"partial key, still typing", "TICKET-300", `key ~ "TICKET-300*" ORDER BY key ASC`},
		{"full key, other project", "OTHER-7", `key ~ "OTHER-7*" ORDER BY key ASC`},
		{"free text", "widget", `summary ~ "widget*"`},
		{"text with quote", `wid"get`, `summary ~ "wid\"get*"`},
		{"text with backslash", `wid\get`, `summary ~ "wid\\get*"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			if _, err := src.searchText(t.Context(), tc.query); err != nil {
				t.Fatalf("searchText: %v", err)
			}
			if gotJQL != tc.want {
				t.Errorf("jql = %q, want %q", gotJQL, tc.want)
			}
		})
	}
}

func TestRealJiraSourceSearchTextEmptyQueryDoesNotCallJira(t *testing.T) {
	withSignedInCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("searchText must not call Jira for an empty query")
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	issues, err := src.searchText(t.Context(), "   ")
	if err != nil {
		t.Fatalf("searchText: %v", err)
	}
	if issues != nil {
		t.Errorf("issues = %v, want nil", issues)
	}
}

func TestRealJiraSourceSearchEpicsBuildsExpectedJQL(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{"full key, upper", "TICKET-1234", `issuetype = Epic AND key ~ "TICKET-1234*" ORDER BY key ASC`},
		{"full key, lower", "TICKET-1234", `issuetype = Epic AND key ~ "TICKET-1234*" ORDER BY key ASC`},
		{"full key, other project", "OTHER-7", `issuetype = Epic AND key ~ "OTHER-7*" ORDER BY key ASC`},
		{"free text", "widget", `issuetype = Epic AND summary ~ "widget*"`},
		{"text with quote", `wid"get`, `issuetype = Epic AND summary ~ "wid\"get*"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			if _, err := src.searchEpics(t.Context(), tc.query); err != nil {
				t.Fatalf("searchEpics: %v", err)
			}
			if gotJQL != tc.want {
				t.Errorf("jql = %q, want %q", gotJQL, tc.want)
			}
		})
	}
}

func TestRealJiraSourceSearchEpicsEmptyQueryDoesNotCallJira(t *testing.T) {
	withSignedInCredentials(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("searchEpics must not call Jira for an empty query")
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	issues, err := src.searchEpics(t.Context(), "   ")
	if err != nil {
		t.Fatalf("searchEpics: %v", err)
	}
	if issues != nil {
		t.Errorf("issues = %v, want nil", issues)
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
	if _, err := src.myEpics(t.Context()); err != nil {
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
	if _, err := src.epicChildren(t.Context(), "TICKET-3008"); err != nil {
		t.Fatalf("epicChildren: %v", err)
	}
	want := `parent = TICKET-3008 ORDER BY updated DESC`
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
		fmt.Fprint(w, sseToolResult(`{"key":"TICKET-9001"}`))
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	if err := src.assign(t.Context(), "TICKET-9001", "account-123"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if gotIssueKey != "TICKET-9001" {
		t.Errorf("issueIdOrKey = %q, want TICKET-9001", gotIssueKey)
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
	if err := src.transition(t.Context(), "TICKET-9001", "31"); err != nil {
		t.Fatalf("transition: %v", err)
	}
}
