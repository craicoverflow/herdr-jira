package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// create.go is quick-create for the app mod: the projects a person may create
// issues in, each project's issue types, and the create call itself. Projects
// and issue types are cached with TTLs (like the list caches); creating an
// issue is a write and only ever happens when the mod asks for it.

const (
	projectsCacheTTL   = 6 * time.Hour
	issueTypesCacheTTL = 6 * time.Hour
	// summaryMax is Jira's own limit on an issue summary.
	summaryMax = 255
)

type jiraProject struct {
	ID   string `json:"id,omitempty"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type jiraIssueType struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Subtask bool   `json:"subtask,omitempty"`
	Level   int    `json:"level"`
}

// createRequest is what the form asks for.
type createRequest struct {
	Project     string
	IssueType   string
	Summary     string
	Description string
	AssignToMe  bool
}

// validateCreate reports what is wrong with a request in plain words, or "".
func validateCreate(r createRequest) string {
	switch {
	case strings.TrimSpace(r.Project) == "":
		return "Pick a project."
	case strings.TrimSpace(r.IssueType) == "":
		return "Pick an issue type."
	case strings.TrimSpace(r.Summary) == "":
		return "Summary is required."
	case utf8.RuneCountInString(strings.TrimSpace(r.Summary)) > summaryMax:
		return fmt.Sprintf("Summary can't be longer than %d characters.", summaryMax)
	}
	return ""
}

// buildCreateArgs is the createJiraIssue call for a request. The description
// is plain text, which the tool takes as markdown (its default content
// format); an empty one is left out. accountID is set only to assign.
func buildCreateArgs(cloudID string, r createRequest, accountID string) map[string]any {
	args := map[string]any{
		"cloudId":       cloudID,
		"projectKey":    strings.ToUpper(strings.TrimSpace(r.Project)),
		"issueTypeName": strings.TrimSpace(r.IssueType),
		"summary":       strings.TrimSpace(r.Summary),
	}
	if d := strings.TrimSpace(r.Description); d != "" {
		args["description"] = d
	}
	if accountID != "" {
		args["assignee_account_id"] = accountID
	}
	return args
}

// chooseDefaultProject picks the form's project: the project of the space's
// ticket, else the last one used to create an issue, else the default_project
// from config.json (first-run setup writes it), else the first visible. Each
// must be among the projects the person may create in.
func chooseDefaultProject(projects []jiraProject, ticketProject, last, configured string) string {
	has := func(k string) bool {
		for _, p := range projects {
			if strings.EqualFold(p.Key, k) {
				return k != ""
			}
		}
		return false
	}
	for _, k := range []string{ticketProject, last, configured} {
		if has(k) {
			for _, p := range projects {
				if strings.EqualFold(p.Key, k) {
					return p.Key
				}
			}
		}
	}
	if len(projects) > 0 {
		return projects[0].Key
	}
	return ""
}

// projectOfKey is the project part of an issue key ("WEB-12" is "WEB").
func projectOfKey(key string) string {
	if i := strings.LastIndex(key, "-"); i > 0 {
		return strings.ToUpper(key[:i])
	}
	return ""
}

var (
	reRequired   = regexp.MustCompile(`(?i)(?:field\s+)?'?([\w ]+)'?\s+is required`)
	rePermission = regexp.MustCompile(`(?i)permission|not allowed|forbidden|403`)
)

// mapCreateError puts a failed create in plain words. Sign-in and network
// problems keep their own codes; Jira's own messages are kept, shortened, when
// nothing more helpful can be said.
func mapCreateError(err error) error {
	if err == nil {
		return nil
	}
	if ae := classifyAPIError(err); ae.Code != "error" {
		return ae
	}
	msg := strings.TrimSpace(strings.TrimPrefix(err.Error(), "tool call failed:"))
	low := strings.ToLower(msg)
	say := func(m string) error { return &apiError{Code: "create_failed", Message: m} }
	switch {
	case rePermission.MatchString(msg):
		return say("You don't have permission to create issues in that project.")
	case strings.Contains(low, "issue type") || strings.Contains(low, "issuetype"):
		return say("That issue type isn't available in this project. Pick another one.")
	case strings.Contains(low, "summary") && strings.Contains(low, "required"):
		return say("Summary is required.")
	case strings.Contains(low, "project") && (strings.Contains(low, "valid") || strings.Contains(low, "not found") || strings.Contains(low, "exist")):
		return say("Jira doesn't know that project. Pick one from the list.")
	case reRequired.MatchString(msg):
		field := strings.TrimSpace(reRequired.FindStringSubmatch(msg)[1])
		return say(fmt.Sprintf("This issue type also needs %q, which this form can't fill in. Create it in Jira instead.", field))
	}
	if len(msg) > 240 {
		msg = msg[:240] + "…"
	}
	if msg == "" {
		msg = "Jira didn't create the issue."
	}
	return say("Jira said: " + msg)
}

// ---- caches ---------------------------------------------------------------------

type projectsCacheFile struct {
	FetchedAt time.Time
	CloudID   string
	Projects  []jiraProject
}

type typesCacheEntry struct {
	FetchedAt time.Time
	Types     []jiraIssueType
}

type typesCacheFile struct {
	CloudID  string
	Projects map[string]typesCacheEntry
}

func projectsCachePath() string   { return filepath.Join(stateDir(), "projects_cache.json") }
func issueTypesCachePath() string { return filepath.Join(stateDir(), "issuetypes_cache.json") }
func lastProjectPath() string     { return filepath.Join(stateDir(), "last_project.json") }

func readJSONFile(path string, out any) bool {
	data, err := os.ReadFile(path)
	return err == nil && json.Unmarshal(data, out) == nil
}

func writeJSONFile(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, ".create-*.json.tmp", data)
}

// readLastProject is the project last used to create an issue on this site,
// kept in the state folder the way stars are.
func readLastProject(cloudID string) string {
	var m map[string]string
	if !readJSONFile(lastProjectPath(), &m) {
		return ""
	}
	return m[cloudID]
}

func writeLastProject(cloudID, key string) {
	var m map[string]string
	if !readJSONFile(lastProjectPath(), &m) || m == nil {
		m = map[string]string{}
	}
	m[cloudID] = key
	_ = writeJSONFile(lastProjectPath(), m)
}

// apiProjects answers `projects`: the projects the person can create issues
// in, and the one the form starts on. Modes as apiIssues: "cached" never calls
// Jira, "auto" only past the TTL, "refresh" always.
func apiProjects(ctx context.Context, src apiSource, mode, ticket string) (any, error) {
	cloudID := cloudIDFn()
	var c projectsCacheFile
	ok := readJSONFile(projectsCachePath(), &c) && !c.FetchedAt.IsZero() && sameSite(c.CloudID, cloudID)
	fresh := ok && nowFn().Sub(c.FetchedAt) < projectsCacheTTL
	if !(mode == "cached" && ok) && !(mode == "auto" && fresh) {
		if mode == "cached" {
			return map[string]any{"projects": []jiraProject{}, "default": "", "cached": false, "fresh": false}, nil
		}
		list, err := src.projects(ctx)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
		c = projectsCacheFile{FetchedAt: nowFn(), CloudID: cloudID, Projects: list}
		_ = writeJSONFile(projectsCachePath(), c)
		fresh, ok = true, true
	}
	if c.Projects == nil {
		c.Projects = []jiraProject{}
	}
	return map[string]any{
		"projects": c.Projects, "default": chooseDefaultProject(c.Projects, projectOfKey(ticket), readLastProject(cloudID), configuredDefaultProject()),
		"cached": ok, "fresh": fresh, "ttlSeconds": int(projectsCacheTTL.Seconds()),
	}, nil
}

// apiIssueTypes answers `issuetypes <project>`: the types you can create there.
func apiIssueTypes(ctx context.Context, src apiSource, project, mode string) (any, error) {
	project = strings.ToUpper(strings.TrimSpace(project))
	if project == "" {
		return nil, usageErr("issuetypes needs a project key")
	}
	cloudID := cloudIDFn()
	var c typesCacheFile
	if !readJSONFile(issueTypesCachePath(), &c) || !sameSite(c.CloudID, cloudID) {
		c = typesCacheFile{}
	}
	e, ok := c.Projects[project]
	ok = ok && !e.FetchedAt.IsZero()
	fresh := ok && nowFn().Sub(e.FetchedAt) < issueTypesCacheTTL
	if !(mode == "cached" && ok) && !(mode == "auto" && fresh) {
		if mode == "cached" {
			return map[string]any{"project": project, "types": []jiraIssueType{}, "cached": false, "fresh": false}, nil
		}
		list, err := src.issueTypes(ctx, project)
		if err != nil {
			return nil, err
		}
		e = typesCacheEntry{FetchedAt: nowFn(), Types: list}
		if c.Projects == nil {
			c.Projects = map[string]typesCacheEntry{}
		}
		c.CloudID = cloudID
		c.Projects[project] = e
		_ = writeJSONFile(issueTypesCachePath(), c)
		fresh, ok = true, true
	}
	if e.Types == nil {
		e.Types = []jiraIssueType{}
	}
	return map[string]any{"project": project, "types": e.Types, "cached": ok, "fresh": fresh, "ttlSeconds": int(issueTypesCacheTTL.Seconds())}, nil
}

// apiCreate creates the issue, remembers its project for next time, and reads
// the new issue back for the mod to show. A person's own new issue also goes
// into the cached lists (as an assign does), so the next list load has it.
func apiCreate(ctx context.Context, src apiSource, r createRequest) (any, error) {
	if msg := validateCreate(r); msg != "" {
		return nil, &apiError{Code: "invalid", Message: msg}
	}
	accountID := ""
	if r.AssignToMe {
		id, err := src.accountID(ctx)
		if err != nil {
			return nil, err
		}
		accountID = id
	}
	key, err := src.createIssue(ctx, r, accountID)
	if err != nil {
		return nil, mapCreateError(err)
	}
	writeLastProject(cloudIDFn(), strings.ToUpper(strings.TrimSpace(r.Project)))
	t, ok := readBack(ctx, src, key)
	if !ok {
		t = ticketStatus{Key: key, Summary: strings.TrimSpace(r.Summary), StatusName: "To Do", StatusCategory: "new", IssueType: r.IssueType}
	}
	if accountID != "" {
		if t.AssigneeName == "" {
			t.AssigneeName = "me"
		}
		recordPatch(cachePatch{Op: patchMine, Row: t})
	}
	return map[string]any{"key": key, "issue": toAPIIssue(t), "assigned": accountID != ""}, nil
}

// ---- the real source ---------------------------------------------------------------

// decodeList decodes a tool result that is either a bare array or an object
// holding the array under one of keys.
func decodeList(raw json.RawMessage, out any, keys ...string) error {
	var tr toolResult
	if err := json.Unmarshal(raw, &tr); err != nil {
		return fmt.Errorf("decode tool result: %w", err)
	}
	if tr.IsError {
		return fmt.Errorf("tool call failed: %s", firstText(tr))
	}
	if len(tr.Content) == 0 {
		return fmt.Errorf("tool result has no text content")
	}
	text := []byte(tr.Content[0].Text)
	if json.Unmarshal(text, out) == nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(text, &obj); err != nil {
		return err
	}
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			return json.Unmarshal(v, out)
		}
	}
	return fmt.Errorf("unexpected reply: no %s", strings.Join(keys, " or "))
}

func firstText(tr toolResult) string {
	if len(tr.Content) > 0 {
		return tr.Content[0].Text
	}
	return ""
}

// getVisibleJiraProjects refuses maxResults above 50, so the project list is
// fetched in pages of that size, up to projectsMaxPages pages.
const (
	projectsPageSize = 50
	projectsMaxPages = 20
)

func (s *realJiraSource) projects(ctx context.Context) ([]jiraProject, error) {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return nil, err
	}
	var list []jiraProject
	seen := map[string]bool{}
	for page := 0; page < projectsMaxPages; page++ {
		raw, err := callTool(ctx, s.client, "getVisibleJiraProjects", map[string]any{
			"cloudId": cloudID, "action": "create",
			"startAt": page * projectsPageSize, "maxResults": projectsPageSize,
		})
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		var batch []jiraProject
		if err := decodeList(raw, &batch, "values", "projects"); err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		added := 0
		for _, p := range batch {
			if !seen[p.Key] {
				seen[p.Key] = true
				list = append(list, p)
				added++
			}
		}
		// A short page is the last one. A page with nothing new means the
		// server ignored startAt, so asking again would loop.
		if len(batch) < projectsPageSize || added == 0 {
			break
		}
	}
	return list, nil
}

func (s *realJiraSource) issueTypes(ctx context.Context, project string) ([]jiraIssueType, error) {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return nil, err
	}
	raw, err := callTool(ctx, s.client, "getJiraProjectIssueTypesMetadata", map[string]any{
		"cloudId": cloudID, "projectIdOrKey": project,
	})
	if err != nil {
		return nil, fmt.Errorf("list issue types for %s: %w", project, err)
	}
	var list []struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Subtask        bool   `json:"subtask"`
		HierarchyLevel int    `json:"hierarchyLevel"`
	}
	if err := decodeList(raw, &list, "issueTypes", "values"); err != nil {
		return nil, fmt.Errorf("list issue types for %s: %w", project, err)
	}
	out := make([]jiraIssueType, 0, len(list))
	for _, t := range list {
		if t.Subtask || t.HierarchyLevel < 0 { // sub-tasks need a parent, which the form doesn't take
			continue
		}
		out = append(out, jiraIssueType{ID: t.ID, Name: t.Name, Level: t.HierarchyLevel})
	}
	return out, nil
}

func (s *realJiraSource) createIssue(ctx context.Context, r createRequest, accountID string) (string, error) {
	cloudID, err := ensureCloudID(ctx, s.client)
	if err != nil {
		return "", err
	}
	raw, err := callTool(ctx, s.client, "createJiraIssue", buildCreateArgs(cloudID, r, accountID))
	if err != nil {
		return "", err
	}
	var res struct {
		Key string `json:"key"`
	}
	if err := decodeToolResultText(raw, &res); err != nil {
		return "", err
	}
	if res.Key == "" {
		return "", fmt.Errorf("Jira didn't return the new issue's key")
	}
	return res.Key, nil
}
