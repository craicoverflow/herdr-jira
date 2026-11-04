package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecodeSSEEnvelopeParsesDataLine(t *testing.T) {
	body := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n\n"
	env, err := decodeSSEEnvelope(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decodeSSEEnvelope: %v", err)
	}
	if string(env.Result) != `{"ok":true}` {
		t.Errorf("Result = %s, want %s", env.Result, `{"ok":true}`)
	}
}

func TestDecodeSSEEnvelopeSurfacesJSONRPCError(t *testing.T) {
	body := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32000,\"message\":\"boom\"}}\n\n"
	env, err := decodeSSEEnvelope(strings.NewReader(body))
	if err != nil {
		t.Fatalf("decodeSSEEnvelope: %v", err)
	}
	if env.Error == nil || env.Error.Message != "boom" {
		t.Errorf("Error = %+v, want message %q", env.Error, "boom")
	}
}

func TestDecodeToolResultTextUnmarshalsInnerJSON(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"text","text":"{\"name\":\"Ada\"}"}],"isError":false}`)
	var out struct {
		Name string `json:"name"`
	}
	if err := decodeToolResultText(raw, &out); err != nil {
		t.Fatalf("decodeToolResultText: %v", err)
	}
	if out.Name != "Ada" {
		t.Errorf("Name = %q, want Ada", out.Name)
	}
}

func TestDecodeToolResultTextErrorsOnIsError(t *testing.T) {
	raw := json.RawMessage(`{"content":[{"type":"text","text":"issue not found"}],"isError":true}`)
	var out map[string]any
	if err := decodeToolResultText(raw, &out); err == nil {
		t.Fatal("decodeToolResultText: expected an error when isError is true")
	}
}

func TestRawMCPCallSendsHeadersAndParsesSSE(t *testing.T) {
	var gotAuth, gotAccept, gotSession string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotSession = r.Header.Get("Mcp-Session-Id")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Mcp-Session-Id", "sess-1")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"hello\":true}}\n\n")
	}))
	defer srv.Close()

	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	result, sessionID, err := rawMCPCall(context.Background(), srv.Client(), "tok-1", "incoming-session", "tools/list", map[string]any{})
	if err != nil {
		t.Fatalf("rawMCPCall: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Errorf("Authorization = %q, want Bearer tok-1", gotAuth)
	}
	if !strings.Contains(gotAccept, "text/event-stream") {
		t.Errorf("Accept = %q, want it to include text/event-stream", gotAccept)
	}
	if gotSession != "incoming-session" {
		t.Errorf("Mcp-Session-Id sent = %q, want incoming-session", gotSession)
	}
	if sessionID != "sess-1" {
		t.Errorf("sessionID returned = %q, want sess-1", sessionID)
	}
	if string(result) != `{"hello":true}` {
		t.Errorf("result = %s", result)
	}
}

func TestRawMCPCallReturnsUnauthorizedOn401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "token expired")
	}))
	defer srv.Close()

	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	_, _, err := rawMCPCall(context.Background(), srv.Client(), "stale-token", "", "tools/list", map[string]any{})
	var unauth *mcpUnauthorized
	if err == nil {
		t.Fatal("rawMCPCall: expected an error for a 401 response")
	}
	if !asUnauthorized(err, &unauth) {
		t.Errorf("err = %v (%T), want *mcpUnauthorized", err, err)
	}
}

func asUnauthorized(err error, target **mcpUnauthorized) bool {
	u, ok := err.(*mcpUnauthorized)
	if ok {
		*target = u
	}
	return ok
}

func TestValidAccessTokenReturnsErrSignedOutWhenNeverSignedIn(t *testing.T) {
	withTempConfigDir(t)
	_, err := validAccessToken(context.Background(), http.DefaultClient)
	if err != errSignedOut {
		t.Errorf("err = %v, want errSignedOut", err)
	}
}

func TestValidAccessTokenReturnsCachedTokenWhenFresh(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{
		AccessToken:  "fresh-token",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	tok, err := validAccessToken(context.Background(), http.DefaultClient)
	if err != nil {
		t.Fatalf("validAccessToken: %v", err)
	}
	if tok != "fresh-token" {
		t.Errorf("token = %q, want fresh-token", tok)
	}
}

func TestValidAccessTokenRefreshesWhenExpired(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{
		ClientID:     "cid-1",
		AccessToken:  "stale-token",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("refresh_token") != "refresh-1" {
			t.Errorf("refresh_token = %q, want refresh-1", r.PostForm.Get("refresh_token"))
		}
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "new-token",
			RefreshToken: "refresh-2",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	orig := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = orig }()

	tok, err := validAccessToken(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("validAccessToken: %v", err)
	}
	if tok != "new-token" {
		t.Errorf("token = %q, want new-token", tok)
	}

	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if creds.RefreshToken != "refresh-2" {
		t.Errorf("stored RefreshToken = %q, want rotated value refresh-2", creds.RefreshToken)
	}
}

func TestRefreshAndSaveReturnsErrSignedOutOnInvalidGrant(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{
		ClientID:     "cid-1",
		RefreshToken: "revoked",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant"}`)
	}))
	defer srv.Close()

	orig := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = orig }()

	_, err := refreshAndSave(context.Background(), srv.Client())
	if err != errSignedOut {
		t.Errorf("err = %v, want errSignedOut", err)
	}
}

// TestValidAccessTokenConcurrentCallsAllSucceed simulates herdr firing
// tick from many events at once against an expired token: all callers must
// succeed and leave one consistent, valid credential set (the server
// should only ever see one refresh, since the lock lets later callers
// reuse the first refresh's fresh token instead of racing their own).
func TestValidAccessTokenConcurrentCallsAllSucceed(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{
		ClientID:     "cid-1",
		AccessToken:  "stale-token",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	var refreshCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCalls++
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "new-token",
			RefreshToken: "refresh-2",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	orig := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = orig }()

	const n = 10
	var wg sync.WaitGroup
	tokens := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tokens[i], errs[i] = validAccessToken(context.Background(), srv.Client())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("call %d: %v", i, err)
		}
		if tokens[i] != "new-token" {
			t.Errorf("call %d: token = %q, want new-token", i, tokens[i])
		}
	}

	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if creds.AccessToken != "new-token" || creds.RefreshToken != "refresh-2" {
		t.Errorf("final credentials = %+v, want AccessToken=new-token RefreshToken=refresh-2", creds)
	}
}

func TestRefreshAndSaveKeepsCloudIDSiteAndAccount(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{
		ClientID: "cid-1", RefreshToken: "rt", ExpiresAt: time.Now().Add(-time.Minute),
		CloudID: "cloud-1", SiteURL: "https://example.atlassian.net", AccountID: "acct-1",
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"new","refresh_token":"rt2","expires_in":3600}`)
	}))
	defer srv.Close()
	orig := tokenEndpoint
	tokenEndpoint = srv.URL
	defer func() { tokenEndpoint = orig }()

	if _, err := refreshAndSave(context.Background(), srv.Client()); err != nil {
		t.Fatalf("refreshAndSave: %v", err)
	}
	got, err := loadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "new" || got.CloudID != "cloud-1" || got.SiteURL != "https://example.atlassian.net" || got.AccountID != "acct-1" {
		t.Errorf("creds after refresh = %+v, want new token and the site/account kept", got)
	}
}

func TestCallToolMapsMissingToolFromStage0Fixture(t *testing.T) {
	withSignedInCredentials(t)
	data, err := os.ReadFile("testdata/stage0/unknown_tool.json")
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil { // SSE data must be one line
		t.Fatal(err)
	}
	data = compact.Bytes()
	srv := newFakeMCPServer(t, map[string]string{})
	defer srv.Close()
	// Serve the recorded isError result for noSuchTool.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		if req.Method == "initialize" {
			fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
			return
		}
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":%s}\n\n", data)
	})
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	_, err = callTool(context.Background(), srv.Client(), "noSuchTool", map[string]any{})
	if !errors.Is(err, errToolMissing) {
		t.Fatalf("err = %v, want errToolMissing", err)
	}
	if !strings.Contains(err.Error(), "noSuchTool") {
		t.Errorf("err = %q, want the tool name in it", err)
	}
}

func TestToolMissingErrorIgnoresOtherFailures(t *testing.T) {
	other := json.RawMessage(`{"content":[{"type":"text","text":"Issue does not exist"}],"isError":true}`)
	if toolMissingError("getJiraIssue", other, nil) != nil {
		t.Error("an ordinary tool failure must not be reported as a missing tool")
	}
	if toolMissingError("getJiraIssue", nil, &mcpRPCError{Code: -32000, Message: "boom"}) != nil {
		t.Error("an unrelated RPC error must not be reported as a missing tool")
	}
	if toolMissingError("x", nil, fmt.Errorf("wrapped: %w", &mcpRPCError{Code: -32601, Message: "Method not found"})) == nil {
		t.Error("a -32601 RPC error should be a missing tool")
	}
}

func TestPluginVersionMatchesManifest(t *testing.T) {
	data, err := os.ReadFile("herdr-plugin.toml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^version\s*=\s*"([^"]+)"`).FindSubmatch(data)
	if m == nil || string(m[1]) != pluginVersion {
		t.Errorf("herdr-plugin.toml version = %q, pluginVersion = %q", m, pluginVersion)
	}
	if !strings.Contains(userAgent, "herdr-jira/"+pluginVersion) {
		t.Errorf("userAgent = %q, want it to carry pluginVersion", userAgent)
	}
}
