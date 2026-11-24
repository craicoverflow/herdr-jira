package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestResolveViewsDefault(t *testing.T) {
	for _, in := range []json.RawMessage{nil, raw(`null`), raw(`[]`)} {
		views, notes := resolveViews(in)
		if len(views) != 1 || views[0].Kind != viewMyWork || views[0].GroupBy != groupByStatus || len(notes) != 0 {
			t.Errorf("resolveViews(%s) = %+v %v, want just My work", in, views, notes)
		}
	}
}

func TestResolveViewsAllThreeKinds(t *testing.T) {
	views, notes := resolveViews(raw(`[
		{"builtin": "my-work"},
		{"name": "Team bugs", "jql": "project = WEB AND type = Bug AND statusCategory != Done"},
		{"name": "Triage", "filter": 10234, "group_by": "none"},
		{"filter": "77"},
		{"builtin": "epics", "name": "Big things"}
	]`))
	if len(notes) != 0 {
		t.Fatalf("unexpected notes: %v", notes)
	}
	if len(views) != 5 {
		t.Fatalf("got %d views: %+v", len(views), views)
	}
	want := []viewDef{
		{ID: "my-work", Name: "My work", Kind: viewMyWork, JQL: myWorkJQL, GroupBy: "status"},
		{ID: "view-2", Name: "Team bugs", Kind: viewJQL, JQL: "project = WEB AND type = Bug AND statusCategory != Done", GroupBy: "status"},
		{ID: "view-3", Name: "Triage", Kind: viewJQL, JQL: "filter = 10234", GroupBy: "none"},
		{ID: "view-4", Name: "Filter 77", Kind: viewJQL, JQL: "filter = 77", GroupBy: "status"},
		{ID: "epics", Name: "Big things", Kind: viewEpics, GroupBy: "status"},
	}
	for i := range want {
		if views[i] != want[i] {
			t.Errorf("views[%d] = %+v, want %+v", i, views[i], want[i])
		}
	}
}

func TestResolveViewsSkipsInvalidEntries(t *testing.T) {
	views, notes := resolveViews(raw(`[
		"my-work",
		{"builtin": "nope"},
		{"name": "No query"},
		{"name": "Both", "jql": "x = 1", "filter": 5},
		{"name": "Bad filter", "filter": "abc"},
		{"name": "Zero filter", "filter": 0},
		{"name": "Bad group", "jql": "x = 1", "group_by": "assignee"},
		{"jql": "x = 1"},
		{"jql": 5},
		{"builtin": "epics"},
		{"builtin": "epics"},
		{"name": "Fine", "jql": "project = A"}
	]`))
	if len(views) != 2 || views[0].Kind != viewEpics || views[1].Name != "Fine" {
		t.Fatalf("valid views = %+v", views)
	}
	if views[1].ID != "view-2" {
		t.Errorf("id counts valid views only: %q", views[1].ID)
	}
	if len(notes) != 10 {
		t.Errorf("want a note per skipped entry, got %d: %v", len(notes), notes)
	}
	for _, n := range notes {
		if !strings.HasPrefix(n, "views[") {
			t.Errorf("note should name the entry: %q", n)
		}
	}
}

func TestResolveViewsNeverCrashes(t *testing.T) {
	for _, in := range []string{`"x"`, `5`, `{}`, `[null]`, `[[]]`, `[{"filter": {}}]`} {
		views, notes := resolveViews(raw(in))
		if len(views) != 1 || views[0].Kind != viewMyWork || len(notes) == 0 {
			t.Errorf("resolveViews(%s) = %+v %v, want My work and a note", in, views, notes)
		}
	}
}

func TestLoadConfigReadsViews(t *testing.T) {
	dir := withTempConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"views": [{"builtin": "epics"}, {"name": "A", "jql": "project = A"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	views, notes := configuredViews()
	if len(views) != 2 || len(notes) != 0 || views[0].Kind != viewEpics {
		t.Fatalf("views = %+v notes = %v", views, notes)
	}
	// A config that isn't JSON still yields the default, with a note.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{nope`), 0o600); err != nil {
		t.Fatal(err)
	}
	views, notes = configuredViews()
	if len(views) != 1 || views[0].Kind != viewMyWork || len(notes) != 1 {
		t.Fatalf("malformed config: %+v %v", views, notes)
	}
}

func TestSplitOrderBy(t *testing.T) {
	cases := []struct{ in, cond, order string }{
		{"a = 1", "a = 1", ""},
		{"a = 1 ORDER BY updated DESC", "a = 1", "ORDER BY updated DESC"},
		{"a = 1 order by key", "a = 1", "order by key"},
		{`summary ~ "order by me" ORDER BY key`, `summary ~ "order by me"`, "ORDER BY key"},
		{`summary ~ "order by me"`, `summary ~ "order by me"`, ""},
		{"ORDER BY key", "", "ORDER BY key"},
	}
	for _, c := range cases {
		cond, order := splitOrderBy(c.in)
		if cond != c.cond || order != c.order {
			t.Errorf("splitOrderBy(%q) = %q, %q; want %q, %q", c.in, cond, order, c.cond, c.order)
		}
	}
}

func TestSearchViewJQL(t *testing.T) {
	got := searchViewJQL("project = WEB AND type = Bug ORDER BY updated DESC", `cart "x"`)
	want := `(project = WEB AND type = Bug) AND (summary ~ "cart \"x\"*") ORDER BY updated DESC`
	if got != want {
		t.Errorf("text search = %s\nwant %s", got, want)
	}
	got = searchViewJQL("filter = 10234", "web-12")
	want = `(filter = 10234) AND (key ~ "WEB-12*")`
	if got != want {
		t.Errorf("key search = %s\nwant %s", got, want)
	}
}

func TestViewCacheKeyPerView(t *testing.T) {
	a := viewDef{Kind: viewJQL, JQL: "project = A"}
	b := viewDef{Kind: viewJQL, JQL: "project = B"}
	my := viewDef{Kind: viewMyWork, JQL: myWorkJQL}
	keys := map[string]bool{viewCacheKey(a): true, viewCacheKey(b): true, viewCacheKey(my): true}
	if len(keys) != 3 {
		t.Fatalf("views share a cache key: %v", keys)
	}
	if viewCacheKey(a) != viewCacheKey(viewDef{ID: "other", Name: "x", Kind: viewJQL, JQL: "project = A"}) {
		t.Errorf("the key follows the query, not the name")
	}
}

func apiViewRows(t *testing.T, src apiSource, args ...string) (issues, subtasks []apiIssue, more bool) {
	t.Helper()
	var got struct {
		Issues        []apiIssue `json:"issues"`
		Subtasks      []apiIssue `json:"subtasks"`
		IssuesMore    bool       `json:"issuesMore"`
		Cached, Fresh bool
	}
	callAPI(t, src, args...).into(t, &got)
	return got.Issues, got.Subtasks, got.IssuesMore
}

func TestViewOpBuildsJQLPerKind(t *testing.T) {
	withAPIState(t)
	dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	cfg := `{"views": [{"builtin": "my-work"}, {"name": "Bugs", "jql": "type = Bug"}, {"name": "Tri", "filter": 10234}, {"builtin": "epics"}]}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	src := newFakeAPI()
	src.jqlResult = []ticketStatus{
		{Key: "A-1", StatusName: "In Progress", StatusCategory: "indeterminate"},
		{Key: "A-2", StatusName: "Sub", HierarchyLevel: -1, ParentKey: "A-1"},
		{Key: "A-3", HierarchyLevel: 1, IssueType: "Epic"},
	}
	src.jqlMore = true
	for _, arg := range []string{"0", "my-work", "view-2", "2", "view-3"} {
		issues, subtasks, more := apiViewRows(t, src, "view", arg, "--refresh")
		if len(issues) != 2 || len(subtasks) != 1 || !more {
			t.Fatalf("view %s: issues %d subtasks %d more %v (epics stay ordinary rows)", arg, len(issues), len(subtasks), more)
		}
	}
	got := strings.Join(src.jqlCalls, "|")
	for _, jql := range []string{myWorkJQL, "type = Bug", "filter = 10234"} {
		if !strings.Contains(got, jql) {
			t.Errorf("JQL %q never ran: %v", jql, src.jqlCalls)
		}
	}
	if len(src.jqlCalls) != 5 {
		t.Errorf("--refresh should always ask Jira: %v", src.jqlCalls)
	}
	// An unknown view is a usage error.
	if r := callAPI(t, src, "view", "9"); r.OK || r.Error.Code != "usage" {
		t.Errorf("unknown view: %+v", r)
	}
	// A definition passed inline needs no config.
	src.jqlCalls = nil
	apiViewRows(t, src, "view", "--def", `{"name":"X","filter":"5"}`, "--refresh")
	if len(src.jqlCalls) != 1 || src.jqlCalls[0] != "filter = 5" {
		t.Errorf("--def JQL: %v", src.jqlCalls)
	}
}

func TestViewOpCachesPerView(t *testing.T) {
	withAPIState(t)
	now := time.Now()
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = time.Now })
	src := newFakeAPI()
	src.jqlResult = []ticketStatus{{Key: "A-1"}}
	a, b := `{"name":"A","jql":"project = A"}`, `{"name":"B","jql":"project = B"}`

	apiViewRows(t, src, "view", "--def", a)
	apiViewRows(t, src, "view", "--def", a)
	if len(src.jqlCalls) != 1 {
		t.Fatalf("a fresh cache should answer: %v", src.jqlCalls)
	}
	apiViewRows(t, src, "view", "--def", b)
	if len(src.jqlCalls) != 2 {
		t.Fatalf("another view must not read A's cache: %v", src.jqlCalls)
	}
	// --cached never calls Jira, and says so when there is nothing cached.
	apiViewRows(t, src, "view", "--def", `{"name":"C","jql":"project = C"}`, "--cached")
	if len(src.jqlCalls) != 2 {
		t.Fatalf("--cached called Jira: %v", src.jqlCalls)
	}
	var miss struct{ Cached bool }
	callAPI(t, src, "view", "--def", `{"name":"C","jql":"project = C"}`, "--cached").into(t, &miss)
	if miss.Cached {
		t.Errorf("nothing was cached for C")
	}
	// Past the TTL it asks again.
	now = now.Add(viewsCacheTTL + time.Minute)
	apiViewRows(t, src, "view", "--def", a)
	if len(src.jqlCalls) != 3 {
		t.Fatalf("a stale cache should refetch: %v", src.jqlCalls)
	}
}

func TestViewLocalPatches(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.jqlResult = []ticketStatus{{Key: "A-1", StatusName: "In Progress", StatusCategory: "indeterminate"}}
	apiViewRows(t, src, "view", "my-work", "--refresh")
	custom := `{"name":"A","jql":"project = A"}`
	apiViewRows(t, src, "view", "--def", custom, "--refresh")

	done := ticketStatus{Key: "A-1", StatusName: "Done", StatusCategory: "done"}
	recordPatch(cachePatch{Op: patchStatus, Row: done})
	issues, _, _ := apiViewRows(t, src, "view", "my-work", "--cached")
	if len(issues) != 0 {
		t.Errorf("My work should drop a Done issue: %+v", issues)
	}
	issues, _, _ = apiViewRows(t, src, "view", "--def", custom, "--cached")
	if len(issues) != 1 || issues[0].Status != "Done" {
		t.Errorf("a custom view keeps the row with its new status: %+v", issues)
	}
}

func TestViewSearchIsScopedToTheView(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.jqlResult = []ticketStatus{{Key: "WEB-9", Summary: "cart"}}
	var got struct{ Hits []apiIssue }
	callAPI(t, src, "search", "--def", `{"name":"Bugs","jql":"project = WEB AND type = Bug ORDER BY rank"}`, "--query", "cart").into(t, &got)
	if len(got.Hits) != 1 {
		t.Fatalf("hits = %+v", got.Hits)
	}
	want := `(project = WEB AND type = Bug) AND (summary ~ "cart*") ORDER BY rank`
	if len(src.jqlCalls) != 1 || src.jqlCalls[0] != want {
		t.Errorf("search JQL = %v, want %s", src.jqlCalls, want)
	}
	// The old --scope search still works.
	callAPI(t, src, "search", "--scope", "epics", "--query", "cart")
}

func TestSessionAndViewsOpListViews(t *testing.T) {
	withAPIState(t)
	dir := os.Getenv("HERDR_PLUGIN_CONFIG_DIR")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"views": [{"builtin": "epics"}, {"name": "x"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Views []viewDef `json:"views"`
		Notes []string  `json:"notes"`
	}
	callAPI(t, newFakeAPI(), "views").into(t, &got)
	if len(got.Views) != 1 || got.Views[0].Kind != viewEpics || len(got.Notes) != 1 {
		t.Errorf("views op = %+v", got)
	}
	s := apiSession()
	if v, _ := s["views"].([]viewDef); len(v) != 1 {
		t.Errorf("session views = %v", s["views"])
	}
	if n, _ := s["viewNotes"].([]string); len(n) != 1 {
		t.Errorf("session viewNotes = %v", s["viewNotes"])
	}
}

func TestDemoViews(t *testing.T) {
	withTempStateDir(t)
	views, notes := demoViews()
	if len(notes) != 0 || len(views) != 4 {
		t.Fatalf("demo views = %+v %v", views, notes)
	}
	src := demoSource{}
	rows, _, err := src.runJQL(nil, "project = WEB AND type = Bug AND statusCategory != Done", 50)
	if err != nil || len(rows) < 3 {
		t.Fatalf("Team bugs: %d rows, %v", len(rows), err)
	}
	for _, r := range rows {
		if r.IssueType != "Bug" || !strings.HasPrefix(r.Key, "WEB-") {
			t.Errorf("not a WEB bug: %+v", r)
		}
	}
	rows, _, _ = src.runJQL(nil, "filter = 10234", 50)
	if len(rows) == 0 {
		t.Fatalf("demo filter 10234 is empty")
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Key, "PLAT-") || r.StatusCategory == "done" {
			t.Errorf("filter 10234 should be open PLAT issues: %+v", r)
		}
	}
	rows, _, _ = src.runJQL(nil, searchViewJQL("project = WEB AND type = Bug", "promo"), 50)
	if len(rows) != 1 || rows[0].Key != "WEB-168" {
		t.Errorf("scoped demo search = %+v", rows)
	}
	mine, _, _ := src.runJQL(nil, myWorkJQL, 50)
	if len(mine) == 0 {
		t.Fatal("My work is empty in the demo")
	}
	for _, r := range mine {
		if r.AssigneeName != demoUser || r.StatusCategory == "done" {
			t.Errorf("not my open work: %+v", r)
		}
	}
	or, _, _ := src.runJQL(nil, `(project = API OR project = PLAT) AND status in ("In Review", "In Progress")`, 50)
	if len(or) == 0 {
		t.Errorf("OR/IN clauses matched nothing")
	}
}
