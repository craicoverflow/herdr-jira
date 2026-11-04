package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ticketStatus is the sidebar/popup view of one issue: just enough to
// render a badge. Fields match the shape confirmed in the Task 0c spike.
type ticketStatus struct {
	Key            string
	Summary        string
	StatusName     string
	StatusCategory string // "new", "indeterminate", "done"
	IssueType      string // e.g. "Task", "Epic", "Subtask"
	ParentKey      string // "" unless the issue has a parent (sub-task, or child of an epic)
	// HierarchyLevel is Jira's issue type level: 0 standard, -1 sub-task,
	// 1 and up for Epic and above. 0 when Jira did not report one.
	HierarchyLevel int
}

// issueDetail is the detail screen's view of one issue.
type issueDetail struct {
	Key            string
	Summary        string
	StatusName     string
	StatusCategory string
	AssigneeName   string
	// AssigneeAccountID is "" when unassigned, so it can be compared
	// directly against accountID()'s result to answer "is this assigned
	// to me already?" without a name-matching heuristic.
	AssigneeAccountID string
	URL               string // browser link; empty if the site URL isn't known yet
}

// transitionOption is one entry from getTransitionsForJiraIssue. Workflow
// transitions are project/board-specific, so this always comes from a
// live call — never hardcoded (the plan's hard rule).
type transitionOption struct {
	ID               string
	Name             string
	ToStatusName     string
	ToStatusCategory string
	HasScreen        bool
}

// jiraSource is where ticket data comes from. search batches every key a
// tick needs into as few calls as possible (the plan's "avoid N+1
// lookups" rule): one searchJiraIssuesUsingJql call per batch of keys.
// myIssues is the popup's "your issues" list; get/transitions/transition
// back the detail screen and its live transition picker.
type jiraSource interface {
	search(ctx context.Context, keys []string) (map[string]ticketStatus, error)
	myIssues(ctx context.Context) ([]ticketStatus, error)
	searchText(ctx context.Context, query string) ([]ticketStatus, error)
	get(ctx context.Context, key string) (issueDetail, error)
	transitions(ctx context.Context, key string) ([]transitionOption, error)
	transition(ctx context.Context, key, transitionID string) error
	myEpics(ctx context.Context) ([]ticketStatus, error)
	searchEpics(ctx context.Context, query string) ([]ticketStatus, error)
	epicChildren(ctx context.Context, epicKey string) ([]ticketStatus, error)
	assign(ctx context.Context, key, accountID string) error
	// accountID returns the signed-in user's accountId, caching it the same
	// way ensureCloudID caches the cloud id. "Assign to me" needs this; the
	// TUI command calls it through the source rather than touching
	// credstore/mcp directly.
	accountID(ctx context.Context) (string, error)
}

// realJiraSource talks to mcp.atlassian.com through the standalone MCP
// client (mcp.go).
type realJiraSource struct {
	client *http.Client
}

func newJiraSource() *realJiraSource {
	return &realJiraSource{client: newHTTPClient()}
}

// searchFields mirrors the Task 0c spike: summary, status (name +
// category), and issuetype in one call is enough for the sidebar/popup;
// assignee isn't needed since every ticket shown is already scoped to a
// branch or to "assigned to me".
var searchFields = []string{"summary", "status", "issuetype", "parent"}

func (s *realJiraSource) search(ctx context.Context, keys []string) (map[string]ticketStatus, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	out := make(map[string]ticketStatus, len(keys))
	for start := 0; start < len(keys); start += keySearchBatch {
		end := min(start+keySearchBatch, len(keys))
		batch := keys[start:end]
		// No status clause: this serves branch-key confirmation, the sidebar
		// and the starred lookup, and a Done ticket must still be found.
		jql := "key in (" + strings.Join(batch, ",") + ")"
		issues, err := s.runSearch(ctx, jql, len(batch))
		if err != nil {
			return nil, fmt.Errorf("search %d ticket(s): %w", len(keys), err)
		}
		for _, iss := range issues {
			out[strings.ToUpper(iss.Key)] = iss
		}
	}
	return out, nil
}

// keySearchBatch is the most keys sent in one `key in (...)` call.
const keySearchBatch = 50

// The popup's "your issues" list is two queries: standard issues and
// sub-tasks, both assigned to the signed-in user and not Done, newest-updated
// first. Stage 0 found that standardIssueTypes() also returns Epics, so the
// first query excludes Epic by name and myIssues also drops any row whose
// hierarchy level is above 0 (an Initiative, or a renamed Epic). Epics have
// their own list (myEpicsJQL).
const (
	myStandardIssuesJQL = "assignee = currentUser() AND issuetype in standardIssueTypes() AND issuetype != Epic AND statusCategory != Done ORDER BY updated DESC"
	mySubtasksJQL       = "assignee = currentUser() AND issuetype in subTaskIssueTypes() AND statusCategory != Done ORDER BY updated DESC"
)

// myIssuesMaxResults bounds each query to one page; paging through
// everything assigned to a user, ever, isn't needed for a "what's mine
// right now" glance.
const myIssuesMaxResults = 50

// myIssues returns standard issues followed by sub-tasks as one slice.
// Sub-tasks carry ParentKey.
func (s *realJiraSource) myIssues(ctx context.Context) ([]ticketStatus, error) {
	issues, err := s.runSearch(ctx, myStandardIssuesJQL, myIssuesMaxResults)
	if err != nil {
		return nil, fmt.Errorf("list your issues: %w", err)
	}
	out := make([]ticketStatus, 0, len(issues))
	for _, iss := range issues {
		if iss.HierarchyLevel > 0 {
			continue
		}
		out = append(out, iss)
	}
	subtasks, err := s.runSearch(ctx, mySubtasksJQL, myIssuesMaxResults)
	if err != nil {
		return nil, fmt.Errorf("list your sub-tasks: %w", err)
	}
	return append(out, subtasks...), nil
}

// keyShapedRE recognizes a Jira key typed whole or in progress, e.g.
// "PROJ-1234" or "PROJ-300" (still being typed toward "PROJ-3008").
// It's deliberately generic, unlike a user-set key_pattern in branch.go: a search box must find any project's
// issue, not just the ones found in branches.
var keyShapedRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)

// searchTextMaxResults bounds the search box's results to a quick,
// narrow-it-down list, not a report.
const searchTextMaxResults = 20

// escapeJQL escapes a value for interpolation inside a double-quoted JQL
// string literal: backslash first, then quote, so an existing backslash
// isn't double-escaped by the quote pass.
func escapeJQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// searchText answers the search box: Jira's `key ~ "X"` matches only the
// exact issue "X" (a plain token match), but `key ~ "X*"` — the trailing
// wildcard — matches every key whose number starts with X, regardless of
// how many more digits follow (confirmed live: `key ~ "OTHER-137*"` found
// OTHER-13765 onward in one call). So any key-shaped query,
// complete or still being typed, gets that wildcard prefix search; anything
// else becomes a text search on summary. Unlike myIssues, this is never
// scoped to the signed-in user — the whole point is finding issues that
// aren't assigned to you.
func (s *realJiraSource) searchText(ctx context.Context, query string) ([]ticketStatus, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	var jql string
	if keyShapedRE.MatchString(query) {
		jql = `key ~ "` + escapeJQL(strings.ToUpper(query)) + `*" ORDER BY key ASC`
	} else {
		jql = `summary ~ "` + escapeJQL(query) + `*"`
	}
	issues, err := s.runSearch(ctx, jql, searchTextMaxResults)
	if err != nil {
		return nil, fmt.Errorf("search %q: %w", query, err)
	}
	return issues, nil
}

// getFields mirrors searchFields plus assignee, for the detail screen.
var getFields = []string{"summary", "status", "issuetype", "assignee"}

func (s *realJiraSource) get(ctx context.Context, key string) (issueDetail, error) {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return issueDetail{}, err
	}
	raw, err := callTool(ctx, s.client, "getJiraIssue", map[string]any{
		"cloudId":      cloudID,
		"issueIdOrKey": key,
		"fields":       getFields,
	})
	if err != nil {
		return issueDetail{}, fmt.Errorf("get %s: %w", key, err)
	}
	var result struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
			Status  struct {
				Name           string `json:"name"`
				StatusCategory struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"status"`
			Assignee struct {
				DisplayName string `json:"displayName"`
				AccountID   string `json:"accountId"`
			} `json:"assignee"`
		} `json:"fields"`
	}
	if err := decodeToolResultText(raw, &result); err != nil {
		return issueDetail{}, err
	}
	detail := issueDetail{
		Key:               result.Key,
		Summary:           result.Fields.Summary,
		StatusName:        result.Fields.Status.Name,
		StatusCategory:    result.Fields.Status.StatusCategory.Key,
		AssigneeName:      result.Fields.Assignee.DisplayName,
		AssigneeAccountID: result.Fields.Assignee.AccountID,
	}
	if creds, err := loadCredentials(); err == nil && creds.SiteURL != "" {
		detail.URL = creds.SiteURL + "/browse/" + detail.Key
	}
	return detail, nil
}

func (s *realJiraSource) transitions(ctx context.Context, key string) ([]transitionOption, error) {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return nil, err
	}
	raw, err := callTool(ctx, s.client, "getTransitionsForJiraIssue", map[string]any{
		"cloudId":      cloudID,
		"issueIdOrKey": key,
	})
	if err != nil {
		return nil, fmt.Errorf("get transitions for %s: %w", key, err)
	}
	var result struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				Name           string `json:"name"`
				StatusCategory struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"to"`
			HasScreen bool `json:"hasScreen"`
		} `json:"transitions"`
	}
	if err := decodeToolResultText(raw, &result); err != nil {
		return nil, err
	}
	out := make([]transitionOption, 0, len(result.Transitions))
	for _, t := range result.Transitions {
		out = append(out, transitionOption{
			ID:               t.ID,
			Name:             t.Name,
			ToStatusName:     t.To.Name,
			ToStatusCategory: t.To.StatusCategory.Key,
			HasScreen:        t.HasScreen,
		})
	}
	return out, nil
}

func (s *realJiraSource) transition(ctx context.Context, key, transitionID string) error {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return err
	}
	raw, err := callTool(ctx, s.client, "transitionJiraIssue", map[string]any{
		"cloudId":      cloudID,
		"issueIdOrKey": key,
		"transition":   map[string]any{"id": transitionID},
	})
	if err != nil {
		return fmt.Errorf("transition %s: %w", key, err)
	}
	return checkToolResult(raw)
}

// myEpicsJQL is the epics tab's JQL: every non-done epic assigned to the
// signed-in user, newest-updated first — mirrors myIssuesJQL's
// statusCategory != Done scoping, so a closed-out epic doesn't linger in
// the list.
const myEpicsJQL = "assignee = currentUser() AND issuetype = Epic AND statusCategory != Done ORDER BY updated DESC"

// myEpicsMaxResults mirrors myIssuesMaxResults: one page is enough for a
// glance, not a report.
const myEpicsMaxResults = 50

func (s *realJiraSource) myEpics(ctx context.Context) ([]ticketStatus, error) {
	issues, err := s.runSearch(ctx, myEpicsJQL, myEpicsMaxResults)
	if err != nil {
		return nil, fmt.Errorf("list your epics: %w", err)
	}
	return issues, nil
}

// searchEpics answers the epics tab's [ctrl+f]: find an epic regardless of
// who owns it, so a team epic you only contribute to can be found and
// starred. Mirrors searchText's key-shaped-vs-text split verbatim, just
// with issuetype = Epic added to the clause — deliberately a bare search,
// not scoped to known project prefixes the way branch detection is, since
// the whole point is finding epics outside your own projects too.
func (s *realJiraSource) searchEpics(ctx context.Context, query string) ([]ticketStatus, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	var jql string
	if keyShapedRE.MatchString(query) {
		jql = `issuetype = Epic AND key ~ "` + escapeJQL(strings.ToUpper(query)) + `*" ORDER BY key ASC`
	} else {
		jql = `issuetype = Epic AND summary ~ "` + escapeJQL(query) + `*"`
	}
	issues, err := s.runSearch(ctx, jql, searchTextMaxResults)
	if err != nil {
		return nil, fmt.Errorf("search epics %q: %w", query, err)
	}
	return issues, nil
}

// epicChildrenMaxResults bounds one epic's children list to a page.
const epicChildrenMaxResults = 50

// epicChildren answers the epics tab's drill-down: `parent = <epicKey>`,
// confirmed live — "Epic Link" also exists on company-managed projects but
// mirrors the same value, and `parent =` is the one to query on. epicKey is
// validated with keyShapedRE the same way the search feature's key-shaped
// branch does, since it's interpolated directly into the JQL.
func (s *realJiraSource) epicChildren(ctx context.Context, epicKey string) ([]ticketStatus, error) {
	if !keyShapedRE.MatchString(epicKey) {
		return nil, fmt.Errorf("epicChildren: %q doesn't look like an issue key", epicKey)
	}
	jql := "parent = " + strings.ToUpper(epicKey) + " ORDER BY updated DESC"
	issues, err := s.runSearch(ctx, jql, epicChildrenMaxResults)
	if err != nil {
		return nil, fmt.Errorf("list children of %s: %w", epicKey, err)
	}
	return issues, nil
}

// assign sets key's assignee to accountID via editJiraIssue — confirmed
// against herdr-jira's own tools/list.
func (s *realJiraSource) assign(ctx context.Context, key, accountID string) error {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return err
	}
	raw, err := callTool(ctx, s.client, "editJiraIssue", map[string]any{
		"cloudId":      cloudID,
		"issueIdOrKey": key,
		"fields": map[string]any{
			"assignee": map[string]any{"accountId": accountID},
		},
	})
	if err != nil {
		return fmt.Errorf("assign %s: %w", key, err)
	}
	return checkToolResult(raw)
}

func (s *realJiraSource) accountID(ctx context.Context) (string, error) {
	return ensureAccountID(ctx, s.client)
}

// runSearch is one searchJiraIssuesUsingJql call, decoded into ticketStatus
// in Jira's returned order.
func (s *realJiraSource) runSearch(ctx context.Context, jql string, maxResults int) ([]ticketStatus, error) {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return nil, err
	}
	args := map[string]any{
		"cloudId": cloudID,
		"jql":     jql,
		"fields":  searchFields,
	}
	if maxResults > 0 {
		args["maxResults"] = maxResults
	}
	raw, err := callTool(ctx, s.client, "searchJiraIssuesUsingJql", args)
	if err != nil {
		return nil, err
	}
	var result struct {
		Issues []struct {
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
				Status  struct {
					Name           string `json:"name"`
					StatusCategory struct {
						Key string `json:"key"`
					} `json:"statusCategory"`
				} `json:"status"`
				IssueType struct {
					Name           string `json:"name"`
					HierarchyLevel int    `json:"hierarchyLevel"`
				} `json:"issuetype"`
				Parent struct {
					Key string `json:"key"`
				} `json:"parent"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := decodeToolResultText(raw, &result); err != nil {
		return nil, err
	}
	out := make([]ticketStatus, 0, len(result.Issues))
	for _, iss := range result.Issues {
		out = append(out, ticketStatus{
			Key:            iss.Key,
			Summary:        iss.Fields.Summary,
			StatusName:     iss.Fields.Status.Name,
			StatusCategory: iss.Fields.Status.StatusCategory.Key,
			IssueType:      iss.Fields.IssueType.Name,
			ParentKey:      iss.Fields.Parent.Key,
			HierarchyLevel: iss.Fields.IssueType.HierarchyLevel,
		})
	}
	return out, nil
}
