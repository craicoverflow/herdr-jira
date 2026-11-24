package main

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildCreateArgs(t *testing.T) {
	args := buildCreateArgs("cloud-1", createRequest{Project: " web ", IssueType: "Bug", Summary: "  Broken  ", Description: "Steps:\n1. go"}, "acct-9")
	want := map[string]any{
		"cloudId": "cloud-1", "projectKey": "WEB", "issueTypeName": "Bug", "summary": "Broken",
		"description": "Steps:\n1. go", "assignee_account_id": "acct-9",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v", args)
	}
	for k, v := range want {
		if args[k] != v {
			t.Errorf("%s = %v, want %v", k, args[k], v)
		}
	}
	bare := buildCreateArgs("c", createRequest{Project: "WEB", IssueType: "Task", Summary: "x", Description: "  "}, "")
	if _, ok := bare["description"]; ok {
		t.Error("an empty description should be left out")
	}
	if _, ok := bare["assignee_account_id"]; ok {
		t.Error("no assignee unless asked")
	}
}

func TestValidateCreate(t *testing.T) {
	ok := createRequest{Project: "WEB", IssueType: "Task", Summary: "s"}
	if m := validateCreate(ok); m != "" {
		t.Errorf("valid request rejected: %s", m)
	}
	for name, r := range map[string]createRequest{
		"project": {IssueType: "Task", Summary: "s"},
		"type":    {Project: "WEB", Summary: "s"},
		"summary": {Project: "WEB", IssueType: "Task", Summary: "   "},
		"long":    {Project: "WEB", IssueType: "Task", Summary: strings.Repeat("é", 256)},
	} {
		if validateCreate(r) == "" {
			t.Errorf("%s: should be invalid", name)
		}
	}
}

func TestChooseDefaultProject(t *testing.T) {
	ps := []jiraProject{{Key: "API"}, {Key: "WEB"}, {Key: "PLAT"}}
	cases := []struct{ ticket, last, want string }{
		{"WEB", "PLAT", "WEB"},   // the space's ticket first
		{"", "PLAT", "PLAT"},     // then the last used
		{"", "", "API"},          // then the first
		{"NOPE", "PLAT", "PLAT"}, // a ticket project you can't create in is skipped
		{"nope", "gone", "API"},
		{"web", "", "WEB"},
	}
	for _, c := range cases {
		if got := chooseDefaultProject(ps, c.ticket, c.last, ""); got != c.want {
			t.Errorf("choose(%q, %q) = %q, want %q", c.ticket, c.last, got, c.want)
		}
	}
	// The configured default sits after the ticket and the last used, before the first.
	for _, c := range []struct{ ticket, last, configured, want string }{
		{"", "", "WEB", "WEB"},
		{"", "PLAT", "WEB", "PLAT"},
		{"API", "PLAT", "WEB", "API"},
		{"", "", "gone", "API"}, // a default you can no longer create in is skipped
		{"", "", "web", "WEB"},
	} {
		if got := chooseDefaultProject(ps, c.ticket, c.last, c.configured); got != c.want {
			t.Errorf("choose(%q, %q, %q) = %q, want %q", c.ticket, c.last, c.configured, got, c.want)
		}
	}
	if chooseDefaultProject(nil, "WEB", "API", "PLAT") != "" {
		t.Error("no projects, no default")
	}
}

func TestMapCreateError(t *testing.T) {
	cases := []struct{ in, contains string }{
		{"tool call failed: You do not have permission to create issues in this project.", "permission"},
		{"tool call failed: Specify a valid issue type", "issue type"},
		{"tool call failed: summary: You must specify a summary of the issue. Field 'summary' is required", "Summary is required"},
		{"tool call failed: Field 'Epic Name' is required", "Epic Name"},
		{"tool call failed: project is required: valid project not found", "project"},
		{"tool call failed: kaboom", "Jira said: kaboom"},
	}
	for _, c := range cases {
		err := mapCreateError(errors.New(c.in))
		ae := classifyAPIError(err)
		if ae.Code != "create_failed" || !strings.Contains(ae.Message, c.contains) {
			t.Errorf("%q -> %+v, want it to contain %q", c.in, ae, c.contains)
		}
	}
	if ae := classifyAPIError(mapCreateError(errSignedOut)); ae.Code != "signed_out" {
		t.Errorf("sign-out keeps its code, got %s", ae.Code)
	}
}

func TestAPICreate(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.createKey = "WEB-7"
	src.accountIDResult = "acct-1"
	src.results["WEB-7"] = ticketStatus{Key: "WEB-7", Summary: "Hello", StatusName: "To Do", StatusCategory: "new", IssueType: "Task", AssigneeName: "Me"}
	r := callAPI(t, src, "create", "--project", "web", "--type", "Task", "--summary", "Hello", "--description", "More", "--assign-me")
	var out struct {
		Key      string   `json:"key"`
		Issue    apiIssue `json:"issue"`
		Assigned bool     `json:"assigned"`
	}
	r.into(t, &out)
	if out.Key != "WEB-7" || !out.Assigned || out.Issue.Summary != "Hello" {
		t.Fatalf("reply = %+v", out)
	}
	if len(src.created) != 1 || src.created[0].Project != "web" || src.created[0].Description != "More" {
		t.Fatalf("created = %+v", src.created)
	}
	if got := readLastProject("cloud-1"); got != "WEB" {
		t.Errorf("last project = %q", got)
	}
}

func TestAPICreateInvalidAndFailing(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	r := callAPI(t, src, "create", "--project", "WEB", "--type", "Task", "--summary", " ")
	if r.OK || r.Error.Code != "invalid" || len(src.created) != 0 {
		t.Fatalf("empty summary should not reach Jira: %+v", r)
	}
	src.createErr = errors.New("tool call failed: You do not have permission to create issues")
	r = callAPI(t, src, "create", "--project", "WEB", "--type", "Task", "--summary", "x")
	if r.OK || r.Error.Code != "create_failed" || !strings.Contains(r.Error.Message, "permission") {
		t.Fatalf("reply = %+v", r)
	}
	if readLastProject("cloud-1") != "" {
		t.Error("a failed create must not become the last project")
	}
}

func TestAPIProjectsCacheAndDefault(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.projectList = []jiraProject{{Key: "WEB", Name: "Storefront"}, {Key: "API", Name: "Platform API"}}
	var out struct {
		Projects []jiraProject `json:"projects"`
		Default  string        `json:"default"`
	}
	callAPI(t, src, "projects", "--cached").into(t, &out)
	if len(out.Projects) != 0 || src.projectCalls != 0 {
		t.Fatalf("--cached with no cache must not call Jira: %+v", out)
	}
	callAPI(t, src, "projects").into(t, &out)
	if src.projectCalls != 1 || out.Default != "API" || out.Projects[0].Key != "API" { // sorted by name: Platform API first
		t.Fatalf("first load: %+v calls=%d", out, src.projectCalls)
	}
	callAPI(t, src, "projects", "--ticket", "WEB-3").into(t, &out)
	if src.projectCalls != 1 || out.Default != "WEB" {
		t.Fatalf("cached, ticket project: %+v calls=%d", out, src.projectCalls)
	}
	writeLastProject("cloud-1", "WEB")
	callAPI(t, src, "projects").into(t, &out)
	if out.Default != "WEB" {
		t.Fatalf("last used: %+v", out)
	}
	callAPI(t, src, "projects", "--refresh").into(t, &out)
	if src.projectCalls != 2 {
		t.Fatalf("--refresh should ask Jira, calls=%d", src.projectCalls)
	}
}

func TestAPIIssueTypesCachePerProject(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.typeList = []jiraIssueType{{Name: "Task"}, {Name: "Bug"}}
	callAPI(t, src, "issuetypes", "web")
	callAPI(t, src, "issuetypes", "WEB")
	if src.typeCalls != 1 {
		t.Fatalf("second call should come from the cache, calls=%d", src.typeCalls)
	}
	callAPI(t, src, "issuetypes", "API")
	callAPI(t, src, "issuetypes", "API", "--refresh")
	if src.typeCalls != 3 {
		t.Fatalf("calls=%d, want 3", src.typeCalls)
	}
	if r := callAPI(t, src, "issuetypes"); r.OK || r.Error.Code != "usage" {
		t.Errorf("no project: %+v", r)
	}
}

func TestDemoCreate(t *testing.T) {
	withAPIState(t)
	d := demoSource{}
	ctx := t.Context()
	key, err := d.createIssue(ctx, createRequest{Project: "WEB", IssueType: "Task", Summary: "New thing"}, demoAccountID)
	if err != nil || key != "WEB-200" {
		t.Fatalf("key = %q, err = %v", key, err)
	}
	issues, _, _, _, _ := d.myIssues(ctx)
	found := false
	for _, i := range issues {
		found = found || i.Key == key
	}
	if !found {
		t.Error("the new issue should show in the demo's list")
	}
	if _, err := d.createIssue(ctx, createRequest{Project: "NOPE", IssueType: "Task", Summary: "x"}, ""); err == nil {
		t.Error("unknown project should fail")
	}
}
