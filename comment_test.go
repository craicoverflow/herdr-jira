package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRealJiraSourceAddCommentSendsMarkdownBody(t *testing.T) {
	withSignedInCredentials(t)
	var gotTool, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments struct {
					IssueIdOrKey string `json:"issueIdOrKey"`
					CommentBody  string `json:"commentBody"`
				} `json:"arguments"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if req.Method == "initialize" {
			w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
			return
		}
		gotTool, gotKey, gotBody = req.Params.Name, req.Params.Arguments.IssueIdOrKey, req.Params.Arguments.CommentBody
		w.Write([]byte(sseToolResult(`{"id":"10001"}`)))
	}))
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	src := &realJiraSource{client: srv.Client()}
	if err := src.addComment(t.Context(), "PROJ-9", "Line one\n\n**bold** two"); err != nil {
		t.Fatalf("addComment: %v", err)
	}
	if gotTool != "addCommentToJiraIssue" || gotKey != "PROJ-9" || gotBody != "Line one\n\n**bold** two" {
		t.Fatalf("sent %q %q %q", gotTool, gotKey, gotBody)
	}
}

func TestCommentBody(t *testing.T) {
	if b, err := commentBody(`{"body":"hi\nthere"}`, strings.NewReader("ignored")); err != nil || b != "hi\nthere" {
		t.Fatalf("json arg: %q %v", b, err)
	}
	if b, err := commentBody("", strings.NewReader("from stdin\n")); err != nil || b != "from stdin\n" {
		t.Fatalf("stdin: %q %v", b, err)
	}
	for _, bad := range []string{`{"body":"  \n"}`, `{"nope":1}`, `not json`} {
		if _, err := commentBody(bad, strings.NewReader("")); err == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
	if _, err := commentBody("", strings.NewReader(" \n")); err == nil {
		t.Fatal("blank stdin should be refused")
	}
}

func TestAPICommentPostsAndReturnsIssue(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.full = issueFull{Key: "A-1", Summary: "One", Comments: []issueComment{{Author: "Me", Body: "hello"}}}
	var got struct {
		Posted bool `json:"posted"`
		Issue  struct {
			Comments []struct{ Body string } `json:"comments"`
		} `json:"issue"`
	}
	callAPI(t, src, "comment", "a-1", "--json", `{"body":"hello"}`).into(t, &got)
	if len(src.commentCalls) != 1 || src.commentCalls[0] != [2]string{"A-1", "hello"} {
		t.Fatalf("calls %v", src.commentCalls)
	}
	if !got.Posted || len(got.Issue.Comments) != 1 {
		t.Fatalf("reply %+v", got)
	}
}

func TestAPICommentErrorsAreReplies(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.commentErr = errors.New("comment on A-1: Jira said no")
	r := callAPI(t, src, "comment", "A-1", "--json", `{"body":"x"}`)
	if r.OK || r.Error == nil || !strings.Contains(r.Error.Message, "Jira said no") {
		t.Fatalf("want the failure in the reply: %+v", r)
	}
	if r := callAPI(t, src, "comment", "A-1", "--json", `{"body":" "}`); r.OK || r.Error.Code != "usage" {
		t.Fatalf("blank comment: %+v", r)
	}
	if r := callAPI(t, src, "comment", "--json", `{"body":"x"}`); r.OK {
		t.Fatalf("a key is needed: %+v", r)
	}
	if len(src.commentCalls) != 1 {
		t.Fatalf("only the first should have posted: %v", src.commentCalls)
	}
}

func TestDemoSourceAddCommentStaysLocal(t *testing.T) {
	withTempStateDir(t)
	src := demoSource{}
	if err := src.addComment(context.Background(), "WEB-142", "Looks good **to me**"); err != nil {
		t.Fatal(err)
	}
	full, err := src.getFull(context.Background(), "WEB-142")
	if err != nil || len(full.Comments) == 0 || full.Comments[len(full.Comments)-1].Body != "Looks good **to me**" {
		t.Fatalf("demo comment: %+v %v", full.Comments, err)
	}
	if err := src.addComment(context.Background(), "NOPE-1", "x"); err == nil {
		t.Fatal("unknown issue should fail")
	}
}
