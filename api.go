package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// api.go is the JSON interface the Herdr app mod (app/mods/jira) drives:
//
//	herdr-jira api <op> [flags] [args]
//
// Each op prints exactly one JSON object on stdout, {"ok":true,"data":…} or
// {"ok":false,"error":{"code":…,"message":…}}, and exits 0 either way, so the
// mod reads every answer, failures included, from stdout. The ops reuse the
// popup's source, caches and star file, so the TUI plugin and the app mod
// share one sign-in and one set of caches.

// apiVersion is bumped when an op's shape changes, so a mod can tell an old
// helper binary from a new one. 2: transitions takes --type and --status.
const apiVersion = 2

type apiReply struct {
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Message }

// usageErr is a mistake in how the mod called an op.
func usageErr(format string, a ...any) error {
	return &apiError{Code: "usage", Message: fmt.Sprintf(format, a...)}
}

// classifyAPIError gives an error a stable code the mod can switch on.
func classifyAPIError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var multi errMultipleSites
	var netErr net.Error
	code := "error"
	switch {
	case errors.Is(err, errSignedOut), errors.Is(err, errNoCredentials):
		code = "signed_out"
	case errors.Is(err, errConsentBlocked):
		code = "consent_blocked"
	case errors.As(err, &multi):
		code = "multiple_sites"
	case errors.Is(err, errNoSite):
		code = "no_site"
	case errors.Is(err, errNotCloud):
		code = "not_cloud"
	case errors.Is(err, errToolMissing):
		code = "tool_missing"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr):
		code = "network"
	}
	return &apiError{Code: code, Message: err.Error()}
}

// apiIssue is one list row as the app sees it.
type apiIssue struct {
	Key           string `json:"key"`
	Summary       string `json:"summary"`
	Status        string `json:"status"`
	Category      string `json:"category"` // new, indeterminate or done
	Type          string `json:"type"`
	Level         int    `json:"level"` // -1 sub-task, 0 standard, 1+ epic and up
	Parent        string `json:"parent,omitempty"`
	ParentSummary string `json:"parentSummary,omitempty"`
	Priority      string `json:"priority,omitempty"`
	Assignee      string `json:"assignee,omitempty"`
	Updated       string `json:"updated,omitempty"`
}

func toAPIIssue(t ticketStatus) apiIssue {
	return apiIssue{
		Key: t.Key, Summary: t.Summary, Status: t.StatusName, Category: t.StatusCategory,
		Type: t.IssueType, Level: t.HierarchyLevel, Parent: t.ParentKey, ParentSummary: t.ParentSummary,
		Priority: t.Priority, Assignee: t.AssigneeName, Updated: t.Updated,
	}
}

func fromAPIIssue(a apiIssue) ticketStatus {
	return ticketStatus{
		Key: a.Key, Summary: a.Summary, StatusName: a.Status, StatusCategory: a.Category,
		IssueType: a.Type, HierarchyLevel: a.Level, ParentKey: a.Parent, ParentSummary: a.ParentSummary,
		Priority: a.Priority, AssigneeName: a.Assignee, Updated: a.Updated,
	}
}

func toAPIIssues(in []ticketStatus) []apiIssue {
	out := make([]apiIssue, 0, len(in))
	for _, t := range in {
		out = append(out, toAPIIssue(t))
	}
	return out
}

// apiSource is jiraSource plus what only the app needs.
type apiSource interface {
	jiraSource
	getFull(ctx context.Context, key string) (issueFull, error)
	userInfo(ctx context.Context) (userInfo, error)
}

func (s *realJiraSource) userInfo(ctx context.Context) (userInfo, error) {
	return fetchUserInfo(ctx, s.client)
}

// runAPICommand is `herdr-jira api [--demo] <op> …`: --demo, or a file named
// herdr-jira.demo beside the binary (`app install --demo` writes one),
// answers from the pretend site in demo.go instead of Jira.
func runAPICommand(ctx context.Context, args []string, w io.Writer) error {
	flagged := len(args) > 0 && args[0] == "--demo"
	if flagged {
		args = args[1:]
	}
	if flagged || demoMarkerPresent() {
		enterDemo()
		demoMode = true
		return runAPI(ctx, demoSource{}, args, w)
	}
	return runAPI(ctx, newJiraSource(), args, w)
}

// demoMarkerName is the file beside the helper that puts it in demo mode.
// It is named for this program so that a helper installed among other
// binaries (~/.local/bin) can't pick up someone else's file.
const demoMarkerName = "herdr-jira.demo"

// demoMarkerPath is the file that puts an installed helper in demo mode.
func demoMarkerPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), demoMarkerName)
}

func demoMarkerPresent() bool {
	p := demoMarkerPath()
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// demoMode is set while answering from the demo site.
var demoMode bool

// siteURLFn is the signed-in site's URL without a network call; a seam for
// the demo site.
var siteURLFn = func() (string, error) {
	creds, err := loadCredentials()
	if err != nil {
		return "", err
	}
	return creds.SiteURL, nil
}

// runAPI runs one op and writes its reply to w.
func runAPI(ctx context.Context, src apiSource, args []string, w io.Writer) error {
	data, err := dispatchAPI(ctx, src, args)
	reply := apiReply{OK: err == nil, Data: data}
	if err != nil {
		reply.Data = nil
		reply.Error = classifyAPIError(err)
	}
	return json.NewEncoder(w).Encode(reply)
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parseInterleaved parses flags wherever they are among the positional
// arguments (the flag package stops at the first positional), so
// `star KEY --row {…}` reads --row.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func dispatchAPI(ctx context.Context, src apiSource, args []string) (any, error) {
	if len(args) == 0 {
		return nil, usageErr("api needs an op")
	}
	op, rest := args[0], args[1:]
	fs := flag.NewFlagSet(op, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cached := fs.Bool("cached", false, "answer from the cache only")
	refresh := fs.Bool("refresh", false, "always ask Jira")
	force := fs.Bool("force", false, "")
	scope := fs.String("scope", "issues", "")
	query := fs.String("query", "", "")
	row := fs.String("row", "", "")
	issueType := fs.String("type", "", "the issue's type, as last seen")
	status := fs.String("status", "", "the issue's status, as last seen")
	var branches stringList
	fs.Var(&branches, "branch", "")
	pos, err := parseInterleaved(fs, rest)
	if err != nil {
		return nil, usageErr("%s: %v", op, err)
	}
	mode := "auto"
	if *cached {
		mode = "cached"
	} else if *refresh {
		mode = "refresh"
	}
	key := func() (string, error) {
		if len(pos) < 1 || !keyShapedRE.MatchString(pos[0]) {
			return "", usageErr("%s needs an issue key", op)
		}
		return strings.ToUpper(pos[0]), nil
	}

	switch op {
	case "version":
		return map[string]any{"version": pluginVersion, "api": apiVersion}, nil
	case "session":
		return apiSession(), nil
	case "login":
		return apiStartLogin(*force)
	case "login-worker":
		runLoginWorker(ctx)
		return nil, nil
	case "logout":
		return apiLogout()
	case "me":
		return apiMe(ctx, src, *refresh)
	case "issues":
		return apiIssues(ctx, src, mode)
	case "epics":
		return apiEpics(ctx, src, mode)
	case "search":
		sc := scopeIssues
		if *scope == "epics" {
			sc = scopeEpics
		}
		hits, err := src.searchScoped(ctx, *query, sc)
		if err != nil {
			return nil, err
		}
		return map[string]any{"query": *query, "scope": *scope, "hits": toAPIIssues(hits)}, nil
	case "issue":
		k, err := key()
		if err != nil {
			return nil, err
		}
		return apiIssueDetail(ctx, src, k)
	case "children":
		k, err := key()
		if err != nil {
			return nil, err
		}
		kids, err := src.epicChildren(ctx, k)
		if err != nil {
			return nil, err
		}
		return map[string]any{"key": k, "children": toAPIIssues(kids)}, nil
	case "transitions":
		k, err := key()
		if err != nil {
			return nil, err
		}
		ts, err := transitionsFor(ctx, src, k, *issueType, *status, *refresh)
		if err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(ts))
		for _, t := range ts {
			out = append(out, map[string]any{"id": t.ID, "name": t.Name, "to": t.ToStatusName, "category": t.ToStatusCategory, "hasScreen": t.HasScreen})
		}
		return map[string]any{"key": k, "transitions": out}, nil
	case "transition":
		k, err := key()
		if err != nil {
			return nil, err
		}
		if len(pos) < 2 || pos[1] == "" {
			return nil, usageErr("transition needs a transition id")
		}
		return apiTransition(ctx, src, k, pos[1])
	case "assign":
		k, err := key()
		if err != nil {
			return nil, err
		}
		return apiAssign(ctx, src, k)
	case "star":
		k, err := key()
		if err != nil {
			return nil, err
		}
		return apiStar(ctx, src, k, *row)
	case "tickets":
		return apiTickets(ctx, src, branches, *force)
	}
	return nil, usageErr("unknown op %q", op)
}

// ---- session and sign-in ---------------------------------------------------

// loginState is login.json: the background sign-in's progress, so the mod
// (whose programs may run for at most 30 seconds) can start a sign-in and
// then poll for it.
type loginState struct {
	State     string    `json:"state"` // waiting, done or error
	URL       string    `json:"url,omitempty"`
	PID       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	Error     *apiError `json:"error,omitempty"`
}

func loginStatePath() string { return filepath.Join(stateDir(), "login.json") }

func readLoginState() (loginState, bool) {
	data, err := os.ReadFile(loginStatePath())
	if err != nil {
		return loginState{}, false
	}
	var st loginState
	if json.Unmarshal(data, &st) != nil {
		return loginState{}, false
	}
	return st, true
}

func writeLoginState(st loginState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(loginStatePath(), ".login-*.json.tmp", data)
}

// loginInProgress reports whether st is a sign-in still waiting on the
// browser: its worker is alive and has not outlived the consent timeout.
func loginInProgress(st loginState) bool {
	age := nowFn().Sub(st.StartedAt)
	if st.State != "waiting" || age > signInTimeout+15*time.Second {
		return false
	}
	if st.PID == 0 {
		// The worker records its PID as it starts; one that hasn't by now
		// never ran.
		return age < workerStartGrace
	}
	return processAlive(st.PID)
}

// workerStartGrace is how long a started sign-in may go without its worker
// recording its PID.
const workerStartGrace = 10 * time.Second

// startDetachedFn and executableFn are seams for tests.
var (
	startDetachedFn = startDetached
	executableFn    = os.Executable
)

func apiSession() map[string]any {
	out := map[string]any{"signedIn": false, "version": pluginVersion, "api": apiVersion}
	if demoMode {
		out["signedIn"], out["siteURL"], out["cloudId"], out["demo"] = true, demoSiteURL, demoCloudID, true
		return out
	}
	if creds, err := loadCredentials(); err == nil && creds.RefreshToken != "" {
		out["signedIn"] = true
		out["siteURL"] = creds.SiteURL
		out["cloudId"] = creds.CloudID
	}
	if st, ok := readLoginState(); ok {
		if st.State == "waiting" && !loginInProgress(st) {
			st.State, st.Error = "error", &apiError{Code: "timeout", Message: "Sign-in timed out. Try again."}
		}
		out["login"] = st
	}
	return out
}

// apiStartLogin starts a sign-in in a detached worker and returns at once.
// Without force, an account that is already signed in is left alone.
func apiStartLogin(force bool) (any, error) {
	if demoMode {
		return loginState{State: "done"}, nil
	}
	if !force {
		if creds, err := loadCredentials(); err == nil && creds.RefreshToken != "" {
			return loginState{State: "done"}, nil
		}
	}
	if st, ok := readLoginState(); ok && loginInProgress(st) {
		return st, nil
	}
	exe, err := executableFn()
	if err != nil {
		return nil, err
	}
	st := loginState{State: "waiting", StartedAt: nowFn()}
	if err := writeLoginState(st); err != nil {
		return nil, err
	}
	pid, err := startDetachedFn(exe, []string{"api", "login-worker"}, filepath.Join(stateDir(), "login.log"))
	if err != nil {
		st.State, st.Error = "error", classifyAPIError(err)
		_ = writeLoginState(st)
		return st, nil
	}
	// From here on only the worker writes login.json (its PID first), so a
	// worker that fails at once can't have its error overwritten here.
	st.PID = pid
	return st, nil
}

// runLoginWorker is the detached sign-in: it records the consent URL as soon
// as it has one, then the outcome.
func runLoginWorker(ctx context.Context) {
	started := nowFn()
	if st, ok := readLoginState(); ok && !st.StartedAt.IsZero() {
		started = st.StartedAt
	}
	_ = writeLoginState(loginState{State: "waiting", PID: os.Getpid(), StartedAt: started})
	err := signInWith(ctx, func(authURL string) {
		_ = writeLoginState(loginState{State: "waiting", URL: authURL, PID: os.Getpid(), StartedAt: started})
		_ = openBrowser(authURL)
	})
	st := loginState{State: "done", StartedAt: started}
	if err != nil {
		st.State, st.Error = "error", classifyAPIError(err)
	}
	_ = writeLoginState(st)
}

// apiLogout forgets the stored sign-in and the caches that belong to it. A
// sign-in still waiting on the browser is stopped first, so it can't save
// new credentials after this.
func apiLogout() (any, error) {
	if demoMode {
		// Signing out of the demo site resets it. enterDemo points the state
		// folder at <state>/demo; anything else is the real one.
		dir := stateDir()
		if filepath.Base(dir) != "demo" {
			return nil, fmt.Errorf("demo sign-out refused: %s is not the demo state folder", dir)
		}
		return map[string]any{"signedIn": true, "demo": true}, os.RemoveAll(dir)
	}
	if st, ok := readLoginState(); ok && loginInProgress(st) && st.PID > 0 {
		stopLoginWorker(st.PID)
	}
	err := withCredentialsLock(func(credentials, error) (credentials, error) {
		if err := os.Remove(credentialsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return credentials{}, err
		}
		return credentials{}, errLoggedOut
	})
	if err != nil && !errors.Is(err, errLoggedOut) {
		return nil, err
	}
	for _, p := range []string{issuesCachePath(), legacyIssuesCachePath(), epicsCachePath(), transitionsCachePath(), appLabelsPath(), mePath(), loginStatePath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return map[string]any{"signedIn": false}, nil
}

// errLoggedOut aborts withCredentialsLock without saving, after logout has
// removed the file under the lock.
var errLoggedOut = errors.New("logged out")

// ---- me ---------------------------------------------------------------------

type meCache struct {
	CloudID   string    `json:"cloudId"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	AccountID string    `json:"accountId"`
	FetchedAt time.Time `json:"fetchedAt"`
}

func mePath() string { return filepath.Join(stateDir(), "me.json") }

const meTTL = 24 * time.Hour

func apiMe(ctx context.Context, src apiSource, refresh bool) (any, error) {
	cloudID := cloudIDFn()
	if !refresh {
		if data, err := os.ReadFile(mePath()); err == nil {
			var c meCache
			if json.Unmarshal(data, &c) == nil && c.CloudID == cloudID && nowFn().Sub(c.FetchedAt) < meTTL && c.Name != "" {
				return c, nil
			}
		}
	}
	info, err := src.userInfo(ctx)
	if err != nil {
		return nil, err
	}
	c := meCache{CloudID: cloudIDFn(), Name: info.Name, Email: info.Email, AccountID: info.AccountID, FetchedAt: nowFn()}
	if data, err := json.Marshal(c); err == nil {
		_ = writeFileAtomic(mePath(), ".me-*.json.tmp", data)
	}
	return c, nil
}

// ---- lists ------------------------------------------------------------------

// apiIssues answers the Issues tab. mode "cached" never calls Jira; "auto"
// calls it only when the cache is older than its TTL; "refresh" always does.
func apiIssues(ctx context.Context, src apiSource, mode string) (any, error) {
	c, hasCache := readIssuesCache()
	if hasCache && !sameSite(c.CloudID, cloudIDFn()) {
		c, hasCache = issuesCache{}, false // another site's cache
	}
	fresh := hasCache && c.fresh()
	if mode == "cached" || (mode == "auto" && fresh) {
		issues, subtasks := splitCached(c.Rows)
		return issuesReply(issues, subtasks, len(issues) >= myIssuesMaxResults, len(subtasks) >= myIssuesMaxResults, c.FetchedAt, fresh, hasCache), nil
	}
	started := nowFn()
	issues, subtasks, issuesMore, subtasksMore, err := src.myIssues(ctx)
	if err != nil {
		return nil, err
	}
	issues, subtasks = storeFetchedIssues(started, issues, subtasks)
	return issuesReply(issues, subtasks, issuesMore, subtasksMore, nowFn(), true, true), nil
}

func issuesReply(issues, subtasks []ticketStatus, issuesMore, subtasksMore bool, fetchedAt time.Time, fresh, cached bool) map[string]any {
	out := map[string]any{
		"issues": toAPIIssues(issues), "subtasks": toAPIIssues(subtasks),
		"issuesMore": issuesMore, "subtasksMore": subtasksMore,
		"fresh": fresh, "cached": cached, "ttlSeconds": int(issuesCacheTTL.Seconds()),
	}
	if !fetchedAt.IsZero() {
		out["fetchedAt"] = fetchedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// apiEpics answers the Epics tab: your epics and your starred ones, with the
// same modes as apiIssues.
func apiEpics(ctx context.Context, src apiSource, mode string) (any, error) {
	c, ok := readEpicsCache()
	if ok && !sameSite(c.CloudID, cloudIDFn()) {
		ok = false // another site's cache
	}
	if mode == "cached" || (mode == "auto" && ok && c.fresh()) {
		if !ok {
			return epicsReply(epicsCache{}, false, false), nil
		}
		// The star file is the truth for which keys are starred (the TUI may
		// have changed it since); the cache only holds their last status.
		c.StarredKeys = starredKeys(c.CloudID)
		return epicsReply(c, c.fresh(), true), nil
	}
	started := nowFn()
	epics, more, err := src.myEpics(ctx)
	if err != nil {
		return nil, err
	}
	cloudID := cloudIDFn()
	keys := starredKeys(cloudID)
	var starred []ticketStatus
	if len(keys) > 0 {
		found, err := src.search(ctx, keys)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if t, ok := found[strings.ToUpper(k)]; ok {
				starred = append(starred, t)
			}
		}
	}
	c = storeFetchedEpics(started, epicsCache{FetchedAt: nowFn(), Epics: epics, More: more, Starred: starred, StarredKeys: keys, CloudID: cloudID})
	return epicsReply(c, true, true), nil
}

func epicsReply(c epicsCache, fresh, cached bool) map[string]any {
	keys := c.StarredKeys
	if keys == nil {
		keys = []string{}
	}
	out := map[string]any{
		"epics": toAPIIssues(c.Epics), "more": c.More, "starred": toAPIIssues(c.Starred),
		"starredKeys": keys, "fresh": fresh, "cached": cached, "ttlSeconds": int(epicsCacheTTL.Seconds()),
	}
	if !c.FetchedAt.IsZero() {
		out["fetchedAt"] = c.FetchedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// ---- one issue -------------------------------------------------------------

func apiIssueDetail(ctx context.Context, src apiSource, key string) (any, error) {
	full, err := src.getFull(ctx, key)
	if err != nil {
		return nil, err
	}
	me, _ := src.accountID(ctx) // only for "mine"; a failure just leaves it false
	starred := false
	for _, k := range starredKeys(cloudIDFn()) {
		if strings.EqualFold(k, full.Key) {
			starred = true
			break
		}
	}
	comments := make([]map[string]any, 0, len(full.Comments))
	for _, c := range full.Comments {
		comments = append(comments, map[string]any{"author": c.Author, "created": c.Created, "body": c.Body})
	}
	labels := full.Labels
	if labels == nil {
		labels = []string{}
	}
	return map[string]any{
		"key": full.Key, "summary": full.Summary, "status": full.StatusName, "category": full.StatusCategory,
		"type": full.IssueType, "level": full.HierarchyLevel, "priority": full.Priority,
		"assignee": full.AssigneeName, "assigneeId": full.AssigneeAccountID,
		"mine": me != "" && me == full.AssigneeAccountID, "reporter": full.ReporterName,
		"created": full.Created, "updated": full.Updated, "labels": labels,
		"project": full.ProjectKey, "projectName": full.ProjectName,
		"parent": full.ParentKey, "parentSummary": full.ParentSummary, "parentType": full.ParentType,
		"description": full.Description, "subtasks": toAPIIssues(full.Subtasks), "comments": comments,
		"url": full.URL, "starred": starred,
	}, nil
}

// ---- writes -----------------------------------------------------------------

// apiTransition moves key through transition id, then re-reads it so the
// caches (and the reply) show the new status at once.
func apiTransition(ctx context.Context, src apiSource, key, id string) (any, error) {
	if err := transitionIssue(ctx, src, key, id); err != nil {
		return nil, err
	}
	invalidateLabelCache(key)
	invalidateAppLabel(key)
	t, ok := readBack(ctx, src, key)
	if !ok {
		return map[string]any{"key": key}, nil // moved; the lists catch up on refresh
	}
	recordPatch(cachePatch{Op: patchStatus, Row: t})
	return map[string]any{"key": key, "issue": toAPIIssue(t)}, nil
}

// apiAssign assigns key to the signed-in user and adds it to the cached lists.
func apiAssign(ctx context.Context, src apiSource, key string) (any, error) {
	accountID, err := src.accountID(ctx)
	if err != nil {
		return nil, err
	}
	if err := src.assign(ctx, key, accountID); err != nil {
		return nil, err
	}
	t, ok := readBack(ctx, src, key)
	if !ok {
		return map[string]any{"key": key}, nil
	}
	recordPatch(cachePatch{Op: patchMine, Row: t})
	return map[string]any{"key": key, "issue": toAPIIssue(t)}, nil
}

// readBack reads key as it is just after a write. It asks for the issue
// itself rather than searching: Jira's search index can trail a write by
// seconds and would hand back the old status.
func readBack(ctx context.Context, src apiSource, key string) (ticketStatus, bool) {
	full, err := src.getFull(ctx, key)
	if err != nil || !strings.EqualFold(full.Key, key) {
		return ticketStatus{}, false
	}
	t := full.row()
	t.Key = key
	return t, true
}

// apiStar stars or unstars an epic. rowJSON, when the mod has the row at hand,
// saves a lookup; without it the epic is looked up so the Starred lane can
// show its status.
func apiStar(ctx context.Context, src apiSource, key, rowJSON string) (any, error) {
	var t ticketStatus
	if rowJSON != "" {
		var a apiIssue
		if err := json.Unmarshal([]byte(rowJSON), &a); err != nil {
			return nil, usageErr("star: bad --row: %v", err)
		}
		t = fromAPIIssue(a)
		t.Key = key
	}
	cloudID := cloudIDFn()
	if cloudID == "" {
		id, err := ensureCloudIDFn(ctx)
		if err != nil {
			return nil, err
		}
		cloudID = id
	}
	if t.Summary == "" && !isStarred(cloudID, key) {
		found, err := src.search(ctx, []string{key})
		if err != nil {
			return nil, err
		}
		got, ok := found[key]
		if !ok {
			return nil, fmt.Errorf("%s was not found", key)
		}
		t = got
	}
	t.Key = key
	starred, err := toggleStar(cloudID, t)
	if err != nil {
		return nil, err
	}
	updateEpicsCache(cloudID, func(c *epicsCache) bool {
		c.StarredKeys = starredKeys(cloudID)
		kept := c.Starred[:0:0]
		for _, s := range c.Starred {
			if s.Key != key {
				kept = append(kept, s)
			}
		}
		if starred && t.StatusName != "" {
			kept = append(kept, t)
		}
		c.Starred = kept
		return true
	})
	return map[string]any{"key": key, "starred": starred}, nil
}

// ensureCloudIDFn resolves the site's cloud ID over the network; a seam for tests.
var ensureCloudIDFn = func(ctx context.Context) (string, error) {
	return ensureCloudID(ctx, newHTTPClient())
}

func isStarred(cloudID, key string) bool {
	for _, k := range starredKeys(cloudID) {
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// ---- branch tickets ---------------------------------------------------------

// app_labels.json is the app's own copy of labels.json: the last known
// status per confirmed ticket key, so the TUI tick (which prunes keys its
// spaces no longer use) and the app never undo each other.
func appLabelsPath() string { return filepath.Join(stateDir(), "app_labels.json") }

func readAppLabels() labelState {
	var st labelState
	if data, err := os.ReadFile(appLabelsPath()); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Cache == nil {
		st.Cache = map[string]cacheEntry{}
	}
	return st
}

func writeAppLabels(st labelState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(appLabelsPath(), ".app_labels-*.json.tmp", data)
}

func invalidateAppLabel(key string) {
	st := readAppLabels()
	if _, ok := st.Cache[key]; ok {
		delete(st.Cache, key)
		_ = writeAppLabels(st)
	}
}

// appLabelMaxAge is how long an unused key's status is kept.
const appLabelMaxAge = 24 * time.Hour

// apiTickets resolves branch names to their Jira tickets, the app's version
// of tick: candidates in each branch are confirmed with Jira (keycache.go),
// and each confirmed key's status is re-read once it is older than
// label_refresh_seconds. A Jira failure falls back to the last known answers,
// reported as a warning; being signed out is an error.
func apiTickets(ctx context.Context, src apiSource, branches []string, force bool) (any, error) {
	cfg, _ := loadConfig()
	st := readAppLabels()
	now := nowFn()

	branchKey := map[string]string{}
	branchCands := map[string][]string{}
	var all []string
	for _, b := range branches {
		if b == "" {
			continue
		}
		if cfg.KeyPattern != "" {
			// A custom pattern can match anything; only key-shaped text goes
			// into the JQL lookup.
			if k, ok := extractKey(b, cfg.KeyPattern); ok && keyShapedRE.MatchString(k) {
				branchKey[b] = k
			}
			continue
		}
		if c := candidateKeys(b); len(c) > 0 {
			branchCands[b] = c
			all = append(all, c...)
		}
	}

	var warn error
	seeded := map[string]bool{}
	if len(all) > 0 {
		confirmed, statuses, err := lookupKeys(ctx, src, all, force)
		if err != nil {
			if errors.Is(err, errSignedOut) {
				return nil, err
			}
			warn = err
			for b, cands := range branchCands {
				for _, c := range cands {
					if _, ok := st.Cache[c]; ok {
						branchKey[b] = c
						break
					}
				}
			}
		} else {
			ok := map[string]bool{}
			for _, k := range confirmed {
				ok[k] = true
			}
			for b, cands := range branchCands {
				for _, c := range cands {
					if ok[c] {
						branchKey[b] = c
						break
					}
				}
			}
			for k, s := range statuses {
				st.Cache[k] = cacheEntry{Fetched: now, Status: s}
				seeded[k] = true
			}
		}
	}

	fresh := time.Duration(cfg.LabelRefreshSeconds) * time.Second
	var due []string
	dueSet := map[string]bool{}
	for _, k := range branchKey {
		if seeded[k] || dueSet[k] {
			continue
		}
		if e, ok := st.Cache[k]; force || !ok || now.Sub(e.Fetched) >= fresh {
			due = append(due, k)
			dueSet[k] = true
		}
	}
	if len(due) > 0 {
		found, err := src.search(ctx, due)
		if err != nil {
			if errors.Is(err, errSignedOut) {
				return nil, err
			}
			warn = err
		} else {
			for _, k := range due {
				if s, ok := found[k]; ok {
					st.Cache[k] = cacheEntry{Fetched: now, Status: s}
				} else {
					delete(st.Cache, k)
				}
			}
		}
	}

	tickets := map[string]any{}
	for _, b := range branches {
		if k, ok := branchKey[b]; ok {
			if e, ok := st.Cache[k]; ok {
				tickets[b] = toAPIIssue(e.Status)
				continue
			}
		}
		tickets[b] = nil
	}
	for k, e := range st.Cache {
		if now.Sub(e.Fetched) > appLabelMaxAge {
			delete(st.Cache, k)
		}
	}
	_ = writeAppLabels(st)
	out := map[string]any{"tickets": tickets}
	if warn != nil {
		out["warning"] = classifyAPIError(warn)
	}
	return out, nil
}
