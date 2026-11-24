package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMergeConfigKeepsUnknownKeysAndOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	orig := `{
  "label_refresh_seconds": 30,
  "future_key": {"a": [1, 2, {"b": null}], "s": "x"},
  "key_pattern": "(PROJ-\\d+)"
}`
	if err := os.WriteFile(path, []byte(orig), 0o640); err != nil {
		t.Fatal(err)
	}
	err := mergeConfigFile(path, []configEntry{{Key: "default_project", Val: rawJSON("WEB")}, {Key: "label_refresh_seconds", Val: json.RawMessage("45")}})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, got)
	}
	if string(m["key_pattern"]) != `"(PROJ-\\d+)"` {
		t.Errorf("key_pattern changed: %s", m["key_pattern"])
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, m["future_key"])
	if compact.String() != `{"a":[1,2,{"b":null}],"s":"x"}` {
		t.Errorf("future_key changed: %s", m["future_key"])
	}
	if string(m["label_refresh_seconds"]) != "45" || string(m["default_project"]) != `"WEB"` {
		t.Errorf("updates not applied: %s", got)
	}
	// Existing keys keep their place; the new one goes last.
	if !(strings.Index(got, "label_refresh_seconds") < strings.Index(got, "future_key") &&
		strings.Index(got, "future_key") < strings.Index(got, "key_pattern") &&
		strings.Index(got, "key_pattern") < strings.Index(got, "default_project")) {
		t.Errorf("order not kept:\n%s", got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", st.Mode().Perm())
	}
}

func TestMergeConfigCreatesAndRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	if err := mergeConfigFile(path, []configEntry{{Key: "default_project", Val: rawJSON("API")}}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.Contains(got, `"default_project": "API"`) {
		t.Errorf("new file: %s", got)
	}
	if err := mergeConfigFile(path, []configEntry{{Key: "default_project"}}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); strings.TrimSpace(got) != "{}" {
		t.Errorf("removing the only key should leave {}: %s", got)
	}
}

func TestMergeConfigRefusesABrokenFileAndLeavesItAlone(t *testing.T) {
	for name, content := range map[string]string{
		"truncated": `{"views": [`, "array": `[1]`, "trailing": `{"a":1} x`, "text": `hello`,
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := mergeConfigFile(path, []configEntry{{Key: "default_project", Val: rawJSON("WEB")}}); err == nil {
			t.Errorf("%s: should refuse", name)
		}
		if got := readFile(t, path); got != content {
			t.Errorf("%s: file was changed to %q", name, got)
		}
	}
}

func TestMergeConfigIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"a": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mergeConfigFile(path, []configEntry{{Key: "b", Val: json.RawMessage("2")}}); err != nil {
		t.Fatal(err)
	}
	// Written beside the file and renamed over it: no temp file is left, and
	// a failed write (a directory in the way) leaves the original untouched.
	left, _ := filepath.Glob(filepath.Join(dir, ".config-*"))
	if len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
	ro := t.TempDir()
	rpath := filepath.Join(ro, "config.json")
	if err := os.WriteFile(rpath, []byte(`{"a": 1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(ro, 0o700)
	if os.Geteuid() != 0 {
		if err := mergeConfigFile(rpath, []configEntry{{Key: "b", Val: json.RawMessage("2")}}); err == nil {
			t.Error("a write that can't be made should fail")
		}
		if got := readFile(t, rpath); got != `{"a": 1}` {
			t.Errorf("original damaged: %q", got)
		}
	}
}

func TestSetupUpdatesValidates(t *testing.T) {
	ok, err := setupUpdates(`{"default_project": " web ", "views": [{"builtin":"epics"},{"name":"Bugs","jql":"type = Bug"}]}`)
	if err != nil || len(ok) != 2 {
		t.Fatalf("got %v, %v", ok, err)
	}
	if string(ok[0].Val) != `"WEB"` {
		t.Errorf("project not normalised: %s", ok[0].Val)
	}
	// My work is always on: it is put first when missing.
	if !strings.HasPrefix(string(ok[1].Val), `[{"builtin":"my-work"},`) {
		t.Errorf("my-work not added: %s", ok[1].Val)
	}
	if u, err := setupUpdates(`{"views": []}`); err != nil || string(u[0].Val) != `[{"builtin":"my-work"}]` {
		t.Errorf("empty views: %v %v", u, err)
	}
	if u, err := setupUpdates(`{"default_project": ""}`); err != nil || u[0].Val != nil {
		t.Errorf("empty project should remove the key: %v %v", u, err)
	}
	if u, err := setupUpdates(`{}`); err != nil || len(u) != 0 {
		t.Errorf("nothing to write: %v %v", u, err)
	}
	for name, p := range map[string]string{
		"unknown key": `{"key_pattern": "x"}`,
		"bad project": `{"default_project": "no spaces"}`,
		"bad view":    `{"views": [{"name": "x"}]}`,
		"dup builtin": `{"views": [{"builtin":"epics"},{"builtin":"epics"}]}`,
		"not a list":  `{"views": {"builtin": "epics"}}`,
		"not json":    `{`,
	} {
		if _, err := setupUpdates(p); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
}

func TestSetupSaveMergesAndMarksDone(t *testing.T) {
	withAPIState(t)
	path := configPathFn()
	if err := os.WriteFile(path, []byte(`{"key_pattern": "(X-\\d+)"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	src := newFakeAPI()
	var s struct {
		Setup struct{ Needed bool } `json:"setup"`
	}
	callAPI(t, src, "session").into(t, &s)
	if !s.Setup.Needed {
		t.Fatal("a fresh install should be offered setup")
	}
	r := callAPI(t, src, "setup-save", "--json", `{"default_project":"WEB","views":[{"builtin":"my-work"},{"builtin":"epics"}]}`)
	if !r.OK {
		t.Fatalf("%+v", r.Error)
	}
	got := readFile(t, path)
	for _, want := range []string{`"key_pattern": "(X-\\d+)"`, `"default_project": "WEB"`, `"builtin": "epics"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	cfg, err := loadConfig()
	if err != nil || cfg.DefaultProject != "WEB" {
		t.Errorf("config doesn't read back: %+v %v", cfg, err)
	}
	if views, _ := configuredViews(); len(views) != 2 || views[1].Kind != viewEpics {
		t.Errorf("views: %+v", views)
	}
	callAPI(t, src, "session").into(t, &s)
	if s.Setup.Needed {
		t.Error("setup should not be offered again")
	}
	if bad := callAPI(t, src, "setup-save", "--json", `{"views":[{"name":"x"}]}`); bad.OK || bad.Error.Code != "invalid" {
		t.Errorf("a bad view must be refused: %+v", bad)
	}
}

func TestSetupSaveLeavesABrokenConfigAlone(t *testing.T) {
	withAPIState(t)
	path := configPathFn()
	os.WriteFile(path, []byte(`{oops`), 0o644)
	r := callAPI(t, newFakeAPI(), "setup-save", "--json", `{"default_project":"WEB"}`)
	if r.OK || r.Error.Code != "config" {
		t.Errorf("want a config error: %+v", r)
	}
	if readFile(t, path) != `{oops` {
		t.Error("the broken file was overwritten")
	}
	if setupDone() {
		t.Error("a failed save must not mark setup done")
	}
}

func TestSetupNeededRespectsExistingConfigAndSkip(t *testing.T) {
	withAPIState(t)
	if !setupNeeded() {
		t.Error("fresh: needed")
	}
	os.WriteFile(configPathFn(), []byte(`{"views": [{"builtin":"my-work"}]}`), 0o644)
	if setupNeeded() {
		t.Error("someone who already set views isn't nagged")
	}
	os.WriteFile(configPathFn(), []byte(`{`), 0o644)
	if setupNeeded() {
		t.Error("a broken config isn't offered setup")
	}
	os.Remove(configPathFn())
	callAPI(t, newFakeAPI(), "setup-skip")
	if setupNeeded() {
		t.Error("skipped setup stays skipped")
	}
	if _, err := os.Stat(configPathFn()); err == nil {
		t.Error("skipping must not write config.json")
	}
}

func TestSitesAndSelect(t *testing.T) {
	withAPIState(t)
	if err := saveCredentials(credentials{ClientID: "c", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	src := newFakeAPI()
	src.siteList = []accessibleResource{
		{ID: "a", URL: "https://alpha.atlassian.net", Name: "alpha"},
		{ID: "b", URL: "https://beta.atlassian.net"},
		{ID: "dc", URL: "https://jira.example.com", Name: "dc"},
	}
	var got struct {
		Sites []struct {
			ID, Name, Host string
			Cloud, Current bool
		}
		Current string
	}
	callAPI(t, src, "sites").into(t, &got)
	if len(got.Sites) != 3 || got.Sites[1].Name != "beta.atlassian.net" || got.Sites[2].Cloud || got.Current != "" {
		t.Fatalf("sites: %+v", got)
	}
	if r := callAPI(t, src, "site-select", "dc"); r.OK || r.Error.Code != "not_cloud" {
		t.Errorf("a non-cloud site can't be chosen: %+v", r)
	}
	if r := callAPI(t, src, "site-select", "nope"); r.OK || r.Error.Code != "invalid" {
		t.Errorf("an unknown id can't be chosen: %+v", r)
	}
	if r := callAPI(t, src, "site-select", "b"); !r.OK {
		t.Fatalf("%+v", r.Error)
	}
	// The same cache ensureCloudID fills for a one-site account.
	creds, _ := loadCredentials()
	if creds.CloudID != "b" || creds.SiteURL != "https://beta.atlassian.net" || creds.RefreshToken != "r" {
		t.Errorf("credentials: %+v", creds)
	}
	callAPI(t, src, "sites").into(t, &got)
	if got.Current != "b" || !got.Sites[1].Current {
		t.Errorf("current not reported: %+v", got)
	}
	src.siteErr = errMultipleSites{}
	if r := callAPI(t, src, "sites"); r.OK {
		t.Error("an error should come back as one")
	}
}

func TestJQLCheck(t *testing.T) {
	withAPIState(t)
	src := newFakeAPI()
	src.jqlResult = []ticketStatus{{Key: "A-1"}, {Key: "A-2"}}
	var got struct {
		Valid bool
		Count int
		More  bool
	}
	callAPI(t, src, "jql-check", "--query", "project = A").into(t, &got)
	if !got.Valid || got.Count != 2 || len(src.jqlCalls) != 1 || src.jqlCalls[0] != "project = A" {
		t.Errorf("got %+v, calls %v", got, src.jqlCalls)
	}
	// Mistakes Jira would only report later never reach it.
	for _, q := range []string{"", "  ", `summary ~ "open`, "(a = 1", "a = 1)"} {
		r := callAPI(t, src, "jql-check", "--query", q)
		if r.OK || r.Error.Code != "invalid_jql" {
			t.Errorf("%q: want invalid_jql, got %+v", q, r)
		}
	}
	if len(src.jqlCalls) != 1 {
		t.Errorf("syntax errors reached Jira: %v", src.jqlCalls)
	}
	// Jira's own complaint is the answer; a sign-out or network failure is not "bad JQL".
	src.jqlErr = errors.New("tool call failed: Error in the JQL Query: Field 'projet' does not exist.")
	r := callAPI(t, src, "jql-check", "--query", "projet = A")
	if r.OK || r.Error.Code != "invalid_jql" || !strings.Contains(r.Error.Message, "'projet' does not exist") || strings.Contains(r.Error.Message, "tool call failed") {
		t.Errorf("%+v", r.Error)
	}
	src.jqlErr = errSignedOut
	if r := callAPI(t, src, "jql-check", "--query", "a = 1"); r.OK || r.Error.Code != "signed_out" {
		t.Errorf("%+v", r.Error)
	}
}

func TestDemoSetupWritesOnlyTheDemoState(t *testing.T) {
	withTempStateDir(t)
	state := os.Getenv("HERDR_PLUGIN_STATE_DIR")
	withTempConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	origCfg, origViews, origCloud, origEnsure, origSite := configPathFn, viewsFn, cloudIDFn, ensureCloudIDFn, siteURLFn
	t.Cleanup(func() {
		configPathFn, viewsFn, cloudIDFn, ensureCloudIDFn, siteURLFn = origCfg, origViews, origCloud, origEnsure, origSite
		demoMode = false
	})
	enterDemo()
	demoMode = true
	real := filepath.Join(configDir(), "config.json")
	r := callAPI(t, demoSource{}, "setup-save", "--json", `{"default_project":"PLAT","views":[{"builtin":"my-work"},{"name":"Mine","jql":"project = WEB"}]}`)
	if !r.OK {
		t.Fatalf("%+v", r.Error)
	}
	if _, err := os.Stat(real); err == nil {
		t.Fatal("demo setup wrote the real config.json")
	}
	if !strings.HasPrefix(configPathFn(), filepath.Join(state, "demo")) {
		t.Errorf("demo config is at %s", configPathFn())
	}
	var s struct{ Views []viewDef }
	callAPI(t, demoSource{}, "session").into(t, &s)
	if len(s.Views) != 2 || s.Views[1].Name != "Mine" {
		t.Errorf("demo tabs didn't follow setup: %+v", s.Views)
	}
	var p struct{ Default string }
	callAPI(t, demoSource{}, "projects").into(t, &p)
	if p.Default != "PLAT" {
		t.Errorf("quick-create default: %q", p.Default)
	}
	if r := callAPI(t, demoSource{}, "site-select", "demo-cloud"); r.OK {
		t.Error("the demo has one site")
	}
}

func TestProjectsReplyUsesConfiguredDefault(t *testing.T) {
	withAPIState(t)
	os.WriteFile(configPathFn(), []byte(`{"default_project": "api"}`), 0o644)
	src := newFakeAPI()
	src.projectList = []jiraProject{{Key: "WEB", Name: "Web"}, {Key: "API", Name: "Api"}}
	var p struct{ Default string }
	callAPI(t, src, "projects").into(t, &p)
	if p.Default != "API" {
		t.Errorf("default = %q", p.Default)
	}
	writeLastProject("cloud-1", "WEB")
	callAPI(t, src, "projects").into(t, &p)
	if p.Default != "WEB" {
		t.Errorf("last used beats the configured default: %q", p.Default)
	}
}
