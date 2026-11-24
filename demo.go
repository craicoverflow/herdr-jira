package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// demo.go is a pretend Jira site for `herdr-jira api --demo`: realistic
// issues, epics, sub-tasks and a workflow, kept in demo_store.json in a
// state folder of its own. It never touches the real sign-in, caches or
// Jira, so the app mod can be tried, tested and screenshotted without an
// account, and without writing to anyone's Jira.

const (
	demoSiteURL   = "https://acme.atlassian.net"
	demoCloudID   = "demo-cloud"
	demoAccountID = "demo-me"
	demoUser      = "Alex Rivera"
)

type demoIssue struct {
	Key         string        `json:"key"`
	Summary     string        `json:"summary"`
	Status      string        `json:"status"`
	Type        string        `json:"type"`
	Level       int           `json:"level"`
	Parent      string        `json:"parent,omitempty"`
	Priority    string        `json:"priority,omitempty"`
	Assignee    string        `json:"assignee,omitempty"`
	Reporter    string        `json:"reporter,omitempty"`
	Labels      []string      `json:"labels,omitempty"`
	Description string        `json:"description,omitempty"`
	Comments    []demoComment `json:"comments,omitempty"`
	Created     string        `json:"created"`
	Updated     string        `json:"updated"`
}

type demoComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
	Ago    int    `json:"ago"` // hours before now
}

// demoWorkflow is every demo project's workflow: status -> category.
var demoWorkflow = []struct{ ID, Status, Category string }{
	{"11", "To Do", "new"},
	{"21", "In Progress", "indeterminate"},
	{"31", "In Review", "indeterminate"},
	{"41", "Done", "done"},
}

func demoCategory(status string) string {
	for _, w := range demoWorkflow {
		if w.Status == status {
			return w.Category
		}
	}
	return "new"
}

func demoSeed() []demoIssue {
	h := func(hours int) string {
		return time.Now().Add(-time.Duration(hours) * time.Hour).UTC().Format("2006-01-02T15:04:05.000+0000")
	}
	me, sam, priya, jo := demoUser, "Sam Okafor", "Priya Natarajan", "Jo Lindqvist"
	return []demoIssue{
		{Key: "WEB-100", Summary: "Checkout redesign", Status: "In Progress", Type: "Epic", Level: 1, Assignee: me, Reporter: priya, Priority: "High", Created: h(900), Updated: h(30),
			Description: "One page checkout with saved addresses and wallets.\n\n## Goals\n\n- Fewer steps from cart to paid\n- Saved shipping and billing details\n- Apple Pay and Google Pay on the same page"},
		{Key: "API-70", Summary: "Public API v2", Status: "To Do", Type: "Epic", Level: 1, Assignee: me, Reporter: sam, Priority: "Medium", Created: h(1400), Updated: h(200),
			Description: "Versioned, paginated and rate-limited. See the RFC in the API docs space."},
		{Key: "WEB-142", Summary: "Checkout: remember the shipping address between sessions", Status: "In Progress", Type: "Story", Parent: "WEB-100", Assignee: me, Reporter: priya, Priority: "High", Labels: []string{"checkout", "frontend"}, Created: h(160), Updated: h(2),
			Description: "Returning customers re-type their address on every order. Keep the last one used and offer it first.\n\n### Acceptance criteria\n\n1. A signed-in customer sees their last shipping address preselected\n2. They can pick another saved address or add a new one\n3. Guests are not affected\n\n```ts\ntype SavedAddress = { id: string; label: string; isDefault: boolean }\n```",
			Comments:    []demoComment{{Author: priya, Body: "Design is in the **Checkout v3** file, frame *Addresses*.", Ago: 50}, {Author: sam, Body: "The address book endpoint already returns `isDefault`, so no API change is needed.", Ago: 6}}},
		{Key: "WEB-143", Summary: "Store the last used address on the account", Status: "In Review", Type: "Sub-task", Level: -1, Parent: "WEB-142", Assignee: me, Reporter: me, Priority: "Medium", Created: h(150), Updated: h(4)},
		{Key: "WEB-144", Summary: "Address picker component", Status: "To Do", Type: "Sub-task", Level: -1, Parent: "WEB-142", Assignee: me, Reporter: me, Priority: "Medium", Created: h(150), Updated: h(20)},
		{Key: "WEB-151", Summary: "Price rounding is off by one cent on multi-currency carts", Status: "To Do", Type: "Bug", Parent: "WEB-100", Assignee: me, Reporter: jo, Priority: "Highest", Labels: []string{"payments"}, Created: h(40), Updated: h(9),
			Description: "Carts mixing EUR and GBP items can total one cent more than the sum of their lines.\n\n**Steps**\n\n1. Add a EUR item at 19.99\n2. Add a GBP item at 4.15\n3. Switch the display currency to USD\n\nThe total is rounded per line *and* again on the sum.",
			Comments:    []demoComment{{Author: jo, Body: "Seen by three customers this week.", Ago: 30}}},
		{Key: "API-88", Summary: "Rate-limit the public search endpoint", Status: "In Review", Type: "Task", Parent: "API-70", Assignee: me, Reporter: sam, Priority: "Medium", Labels: []string{"api"}, Created: h(300), Updated: h(12),
			Description: "100 requests a minute per token, with `Retry-After` on a 429."},
		{Key: "API-91", Summary: "Rotate the staging database credentials", Status: "To Do", Type: "Task", Assignee: me, Reporter: sam, Priority: "Low", Created: h(500), Updated: h(120)},
		{Key: "WEB-160", Summary: "Dark mode for the order history page", Status: "To Do", Type: "Story", Parent: "WEB-100", Assignee: me, Reporter: priya, Priority: "Medium", Created: h(260), Updated: h(70)},
		{Key: "API-95", Summary: "Write the migration for the audit columns", Status: "In Progress", Type: "Sub-task", Level: -1, Parent: "API-90", Assignee: me, Reporter: sam, Priority: "Medium", Created: h(90), Updated: h(16)},
		{Key: "API-90", Summary: "Audit log for admin actions", Status: "In Progress", Type: "Story", Parent: "API-70", Assignee: sam, Reporter: sam, Priority: "High", Created: h(400), Updated: h(16)},
		{Key: "PLAT-12", Summary: "Observability: one dashboard per service", Status: "In Progress", Type: "Epic", Level: 1, Assignee: jo, Reporter: jo, Priority: "Medium", Created: h(2000), Updated: h(48)},
		{Key: "PLAT-20", Summary: "Kubernetes 1.34 upgrade", Status: "Done", Type: "Epic", Level: 1, Assignee: jo, Reporter: jo, Priority: "High", Created: h(3000), Updated: h(400)},
		{Key: "WEB-180", Summary: "Mobile web performance", Status: "To Do", Type: "Epic", Level: 1, Assignee: priya, Reporter: priya, Priority: "Medium", Created: h(700), Updated: h(90)},
		{Key: "WEB-171", Summary: "Search suggestions show deleted products", Status: "To Do", Type: "Bug", Assignee: priya, Reporter: jo, Priority: "High", Created: h(80), Updated: h(40)},
		{Key: "WEB-165", Summary: "Cart badge count is stale after removing the last item", Status: "In Progress", Type: "Bug", Assignee: priya, Reporter: jo, Priority: "Medium", Created: h(50), Updated: h(5)},
		{Key: "WEB-168", Summary: "Promo code field accepts trailing spaces", Status: "In Review", Type: "Bug", Assignee: sam, Reporter: priya, Priority: "Low", Created: h(70), Updated: h(14)},
		{Key: "API-99", Summary: "Document the pagination tokens", Status: "To Do", Type: "Task", Parent: "API-70", Reporter: sam, Priority: "Low", Created: h(60), Updated: h(60)},
		{Key: "PLAT-31", Summary: "Alert on p99 checkout latency", Status: "In Progress", Type: "Task", Parent: "PLAT-12", Assignee: jo, Reporter: jo, Priority: "Medium", Created: h(100), Updated: h(10)},
	}
}

// demoSource is the pretend site. Every call reads the store and every
// change writes it back, so separate helper runs agree.
type demoSource struct{}

func demoStorePath() string { return filepath.Join(stateDir(), "demo_store.json") }

func (demoSource) load() []demoIssue {
	if data, err := os.ReadFile(demoStorePath()); err == nil {
		var out []demoIssue
		if json.Unmarshal(data, &out) == nil && len(out) > 0 {
			return out
		}
	}
	return demoSeed()
}

func (demoSource) save(all []demoIssue) error {
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return writeFileAtomic(demoStorePath(), ".demo-*.json.tmp", data)
}

func (d demoSource) find(key string) (demoIssue, []demoIssue, int) {
	all := d.load()
	for i, is := range all {
		if strings.EqualFold(is.Key, key) {
			return is, all, i
		}
	}
	return demoIssue{}, all, -1
}

func (d demoSource) status(is demoIssue, all []demoIssue) ticketStatus {
	t := ticketStatus{
		Key: is.Key, Summary: is.Summary, StatusName: is.Status, StatusCategory: demoCategory(is.Status),
		IssueType: is.Type, ParentKey: is.Parent, HierarchyLevel: is.Level, Priority: is.Priority,
		AssigneeName: is.Assignee, Updated: is.Updated,
	}
	for _, p := range all {
		if p.Key == is.Parent {
			t.ParentSummary = p.Summary
		}
	}
	return t
}

func (d demoSource) filter(keep func(demoIssue) bool) []ticketStatus {
	all := d.load()
	var out []ticketStatus
	for _, is := range all {
		if keep(is) {
			out = append(out, d.status(is, all))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out
}

func (d demoSource) search(_ context.Context, keys []string) (map[string]ticketStatus, error) {
	all := d.load()
	out := map[string]ticketStatus{}
	for _, k := range keys {
		for _, is := range all {
			if strings.EqualFold(is.Key, k) {
				out[strings.ToUpper(k)] = d.status(is, all)
			}
		}
	}
	return out, nil
}

func (d demoSource) myIssues(context.Context) (issues, subtasks []ticketStatus, issuesMore, subtasksMore bool, err error) {
	open := func(is demoIssue) bool { return is.Assignee == demoUser && demoCategory(is.Status) != "done" }
	issues = d.filter(func(is demoIssue) bool { return open(is) && is.Level == 0 })
	subtasks = d.filter(func(is demoIssue) bool { return open(is) && is.Level < 0 })
	return issues, subtasks, false, false, nil
}

func (d demoSource) searchScoped(_ context.Context, query string, scope searchScope) ([]ticketStatus, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, nil
	}
	return d.filter(func(is demoIssue) bool {
		if demoCategory(is.Status) == "done" || (scope == scopeEpics) != (is.Level > 0) {
			return false
		}
		if keyShapedRE.MatchString(q) {
			return strings.HasPrefix(strings.ToLower(is.Key), q)
		}
		return strings.Contains(strings.ToLower(is.Summary), q)
	}), nil
}

func (d demoSource) get(_ context.Context, key string) (issueDetail, error) {
	is, _, i := d.find(key)
	if i < 0 {
		return issueDetail{}, fmt.Errorf("get %s: issue does not exist", key)
	}
	acct := ""
	if is.Assignee == demoUser {
		acct = demoAccountID
	}
	return issueDetail{Key: is.Key, Summary: is.Summary, StatusName: is.Status, StatusCategory: demoCategory(is.Status),
		IssueType: is.Type, AssigneeName: is.Assignee, AssigneeAccountID: acct, URL: demoSiteURL + "/browse/" + is.Key}, nil
}

func (d demoSource) getFull(_ context.Context, key string) (issueFull, error) {
	is, all, i := d.find(key)
	if i < 0 {
		return issueFull{}, fmt.Errorf("get %s: issue does not exist", key)
	}
	project := strings.SplitN(is.Key, "-", 2)[0]
	names := map[string]string{"WEB": "Storefront", "API": "Platform API", "PLAT": "Platform"}
	full := issueFull{
		Key: is.Key, Summary: is.Summary, StatusName: is.Status, StatusCategory: demoCategory(is.Status),
		IssueType: is.Type, HierarchyLevel: is.Level, Priority: is.Priority, AssigneeName: is.Assignee,
		ReporterName: is.Reporter, Created: is.Created, Updated: is.Updated, Labels: is.Labels,
		ProjectKey: project, ProjectName: names[project], Description: is.Description,
		URL: demoSiteURL + "/browse/" + is.Key,
	}
	if is.Assignee == demoUser {
		full.AssigneeAccountID = demoAccountID
	}
	for _, p := range all {
		if p.Key == is.Parent {
			full.ParentKey, full.ParentSummary, full.ParentType = p.Key, p.Summary, p.Type
		}
		if p.Parent == is.Key && p.Level < 0 {
			full.Subtasks = append(full.Subtasks, d.status(p, all))
		}
	}
	for _, c := range is.Comments {
		created := time.Now().Add(-time.Duration(c.Ago) * time.Hour).UTC().Format("2006-01-02T15:04:05.000+0000")
		full.Comments = append(full.Comments, issueComment{Author: c.Author, Created: created, Body: c.Body})
	}
	return full, nil
}

func (d demoSource) transitions(_ context.Context, key string) ([]transitionOption, error) {
	is, _, i := d.find(key)
	if i < 0 {
		return nil, fmt.Errorf("get transitions for %s: issue does not exist", key)
	}
	var out []transitionOption
	for _, w := range demoWorkflow {
		if w.Status != is.Status {
			out = append(out, transitionOption{ID: w.ID, Name: w.Status, ToStatusName: w.Status, ToStatusCategory: w.Category})
		}
	}
	return out, nil
}

func (d demoSource) transition(_ context.Context, key, id string) error {
	_, all, i := d.find(key)
	if i < 0 {
		return fmt.Errorf("transition %s: issue does not exist", key)
	}
	for _, w := range demoWorkflow {
		if w.ID == id {
			all[i].Status = w.Status
			all[i].Updated = time.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
			return d.save(all)
		}
	}
	return fmt.Errorf("transition %s: no transition %s", key, id)
}

func (d demoSource) myEpics(context.Context) ([]ticketStatus, bool, error) {
	return d.filter(func(is demoIssue) bool {
		return is.Level > 0 && is.Assignee == demoUser && demoCategory(is.Status) != "done"
	}), false, nil
}

func (d demoSource) epicChildren(_ context.Context, key string) ([]ticketStatus, error) {
	return d.filter(func(is demoIssue) bool { return strings.EqualFold(is.Parent, key) }), nil
}

func (d demoSource) assign(_ context.Context, key, accountID string) error {
	_, all, i := d.find(key)
	if i < 0 {
		return fmt.Errorf("assign %s: issue does not exist", key)
	}
	all[i].Assignee = demoUser
	all[i].Updated = time.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
	return d.save(all)
}

// addComment posts locally: the comment is kept in the demo state, never sent.
func (d demoSource) addComment(_ context.Context, key, body string) error {
	_, all, i := d.find(key)
	if i < 0 {
		return fmt.Errorf("comment on %s: issue does not exist", key)
	}
	all[i].Comments = append(all[i].Comments, demoComment{Author: demoUser, Body: body, Ago: 0})
	all[i].Updated = time.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
	return d.save(all)
}

func (demoSource) accountID(context.Context) (string, error) { return demoAccountID, nil }

func (demoSource) userInfo(context.Context) (userInfo, error) {
	return userInfo{Name: demoUser, Email: "alex@example.com", AccountID: demoAccountID}, nil
}

// enterDemo points the helper at the demo site: its own state folder, a
// fixed cloud ID, and stars seeded once so the Starred lane has something.
func enterDemo() {
	dir := filepath.Join(stateDir(), "demo")
	os.Setenv("HERDR_PLUGIN_STATE_DIR", dir)
	cloudIDFn = func() string { return demoCloudID }
	ensureCloudIDFn = func(context.Context) (string, error) { return demoCloudID, nil }
	siteURLFn = func() (string, error) { return demoSiteURL, nil }
	viewsFn = demoViews
	// The demo keeps its own config.json in its state folder, so setup
	// writes (tabs, default project) never touch the real one.
	configPathFn = func() string { return filepath.Join(stateDir(), "config.json") }
	if _, err := os.Stat(starStatePath()); os.IsNotExist(err) {
		_ = writeStars(demoCloudID, []starEntry{{Key: "PLAT-12"}, {Key: "PLAT-20"}})
	}
}

// demoViews are the demo site's tabs: My work, two custom queries (one a
// saved filter) and Epics, so every kind shows.
func demoViews() ([]viewDef, []string) {
	// Tabs chosen in the demo's setup win over the fixed set.
	if cfg, err := loadConfig(); err == nil && len(cfg.Views) > 0 {
		return resolveViews(cfg.Views)
	}
	return resolveViews(json.RawMessage(`[
		{"builtin": "my-work"},
		{"name": "Team bugs", "jql": "project = WEB AND type = Bug AND statusCategory != Done"},
		{"name": "Platform", "filter": 10234, "group_by": "none"},
		{"builtin": "epics"}
	]`))
}

// sites: the demo has the one, so setup skips that step.
func (demoSource) sites(context.Context) ([]accessibleResource, error) {
	return []accessibleResource{{ID: demoCloudID, URL: demoSiteURL, Name: "acme"}}, nil
}

// ---- quick-create ---------------------------------------------------------------

var demoProjects = []jiraProject{{ID: "1", Key: "API", Name: "Platform API"}, {ID: "3", Key: "PLAT", Name: "Platform"}, {ID: "2", Key: "WEB", Name: "Storefront"}}

func (demoSource) projects(context.Context) ([]jiraProject, error) {
	return append([]jiraProject{}, demoProjects...), nil
}

func (demoSource) issueTypes(_ context.Context, project string) ([]jiraIssueType, error) {
	for _, p := range demoProjects {
		if strings.EqualFold(p.Key, project) {
			return []jiraIssueType{{ID: "10", Name: "Task"}, {ID: "11", Name: "Story"}, {ID: "12", Name: "Bug"}, {ID: "13", Name: "Epic", Level: 1}}, nil
		}
	}
	return nil, fmt.Errorf("create: project %q doesn't exist", project)
}

// createIssue adds the issue to the demo store, as the next number in its
// project. Nothing leaves the machine.
func (d demoSource) createIssue(_ context.Context, r createRequest, accountID string) (string, error) {
	project := strings.ToUpper(strings.TrimSpace(r.Project))
	known := false
	for _, p := range demoProjects {
		known = known || p.Key == project
	}
	if !known {
		return "", fmt.Errorf("tool call failed: project %q doesn't exist", project)
	}
	level := 0
	if strings.EqualFold(r.IssueType, "Epic") {
		level = 1
	}
	all := d.load()
	n := 199
	for _, is := range all {
		var num int
		if p, rest, ok := strings.Cut(is.Key, "-"); ok && p == project {
			if _, err := fmt.Sscanf(rest, "%d", &num); err == nil && num > n {
				n = num
			}
		}
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
	is := demoIssue{Key: fmt.Sprintf("%s-%d", project, n+1), Summary: strings.TrimSpace(r.Summary), Status: "To Do", Type: r.IssueType,
		Level: level, Reporter: demoUser, Priority: "Medium", Description: strings.TrimSpace(r.Description), Created: now, Updated: now}
	if accountID != "" {
		is.Assignee = demoUser
	}
	if err := d.save(append(all, is)); err != nil {
		return "", err
	}
	return is.Key, nil
}
