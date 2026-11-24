package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// setup.go is the first-run setup in the Herdr app: which site, the default
// project and the tabs. The mod never writes config.json itself; it calls
// these ops, which merge into the file and leave everything else in it alone.
//
//	sites                   the Jira sites the account reaches, and the chosen one
//	site-select <id>        choose one (the same cloud_id/site_url cache every op reads)
//	jql-check --query JQL   run a JQL once, to validate a tab
//	setup-save --json {…}   merge default_project and/or views into config.json
//	setup-skip              mark setup done without changing anything
//
// Saved filters can't be listed: the Atlassian MCP has no filter tool (see
// testdata/stage0/tools_list.json) and its token isn't accepted by Jira's REST
// API, so a "tab from a filter" is a JQL tab the person writes (or a hand-written
// {"filter": id} entry in config.json).

// ---- the config merge-write ------------------------------------------------

// configEntry is one top-level key of config.json with its value untouched.
type configEntry struct {
	Key string
	Val json.RawMessage
}

// parseConfigObject reads a JSON object's top-level entries in file order,
// keeping each value as written. An empty file is an empty object; anything
// that isn't one JSON object is an error (so a hand-broken file is never
// overwritten).
func parseConfigObject(data []byte) ([]configEntry, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("it isn't a JSON object")
	}
	var out []configEntry
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		replaced := false
		for i := range out {
			if out[i].Key == key { // a repeated key: the last one wins, as in Go and JS
				out[i].Val, replaced = raw, true
			}
		}
		if !replaced {
			out = append(out, configEntry{key, raw})
		}
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("there is text after the JSON object")
	}
	return out, nil
}

// renderConfigObject writes entries as an indented JSON object.
func renderConfigObject(entries []configEntry) ([]byte, error) {
	if len(entries) == 0 {
		return []byte("{}\n"), nil
	}
	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, e := range entries {
		k, err := json.Marshal(e.Key)
		if err != nil {
			return nil, err
		}
		buf.WriteString("  ")
		buf.Write(k)
		buf.WriteString(": ")
		if err := json.Indent(&buf, e.Val, "  ", "  "); err != nil {
			return nil, err
		}
		if i < len(entries)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// mergeConfigEntries sets each update on entries: an existing key keeps its
// place, a new one goes last, and a nil value removes the key.
func mergeConfigEntries(entries []configEntry, updates []configEntry) []configEntry {
	for _, u := range updates {
		at := -1
		for i := range entries {
			if entries[i].Key == u.Key {
				at = i
			}
		}
		switch {
		case u.Val == nil && at >= 0:
			entries = append(entries[:at], entries[at+1:]...)
		case u.Val == nil:
		case at >= 0:
			entries[at].Val = u.Val
		default:
			entries = append(entries, u)
		}
	}
	return entries
}

// mergeConfigFile applies updates to the config file at path. Every other key
// is kept as written; a file that isn't a JSON object is left alone and
// reported. The new file is written whole beside the old and renamed over it,
// so a reader never sees half of it.
func mergeConfigFile(path string, updates []configEntry) error {
	data, err := os.ReadFile(path)
	mode := os.FileMode(0o644)
	switch {
	case err == nil:
		if st, serr := os.Stat(path); serr == nil {
			mode = st.Mode().Perm()
		}
	case errors.Is(err, os.ErrNotExist):
		data = nil
	default:
		return err
	}
	entries, err := parseConfigObject(data)
	if err != nil {
		return fmt.Errorf("config.json isn't valid JSON (%v); fix it or remove it, then try again", err)
	}
	out, err := renderConfigObject(mergeConfigEntries(entries, updates))
	if err != nil {
		return err
	}
	if err := writeFileAtomic(path, ".config-*.json.tmp", out); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// ---- what setup may write --------------------------------------------------

var projectKeyRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)

// setupPayload is `setup-save --json`: only these two keys are managed.
type setupPayload struct {
	DefaultProject *string         `json:"default_project"`
	Views          json.RawMessage `json:"views"`
}

// setupUpdates validates a payload and turns it into config updates. The views
// list must be wholly valid (the config reader would skip bad entries; setup
// never writes them) and always starts with My work.
func setupUpdates(payload string) ([]configEntry, error) {
	var p setupPayload
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, usageErr("setup-save: %v", err)
	}
	var out []configEntry
	if p.DefaultProject != nil {
		key := strings.ToUpper(strings.TrimSpace(*p.DefaultProject))
		switch {
		case key == "":
			out = append(out, configEntry{Key: "default_project"}) // nil: remove it
		case !projectKeyRE.MatchString(key):
			return nil, &apiError{Code: "invalid", Message: fmt.Sprintf("%q isn't a Jira project key.", *p.DefaultProject)}
		default:
			out = append(out, configEntry{Key: "default_project", Val: rawJSON(key)})
		}
	}
	if len(p.Views) > 0 && string(p.Views) != "null" {
		var entries []json.RawMessage
		if err := json.Unmarshal(p.Views, &entries); err != nil {
			return nil, &apiError{Code: "invalid", Message: "views must be a list."}
		}
		if len(entries) == 0 {
			entries = []json.RawMessage{json.RawMessage(`{"builtin":"my-work"}`)}
			p.Views = json.RawMessage(`[{"builtin":"my-work"}]`)
		}
		views, notes := resolveViews(p.Views)
		if len(notes) > 0 || len(views) != len(entries) {
			msg := "a tab isn't valid"
			if len(notes) > 0 {
				msg = notes[0]
			}
			return nil, &apiError{Code: "invalid", Message: msg}
		}
		hasMyWork := false
		for _, v := range views {
			hasMyWork = hasMyWork || v.Kind == viewMyWork
		}
		if !hasMyWork { // My work is always on
			entries = append([]json.RawMessage{json.RawMessage(`{"builtin":"my-work"}`)}, entries...)
		}
		list, err := json.Marshal(entries)
		if err != nil {
			return nil, err
		}
		out = append(out, configEntry{Key: "views", Val: list})
	}
	return out, nil
}

func rawJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// configuredDefaultProject is default_project from config.json, or "".
func configuredDefaultProject() string {
	cfg, _ := loadConfig()
	return strings.ToUpper(strings.TrimSpace(cfg.DefaultProject))
}

// ---- setup's own state -----------------------------------------------------

// setupState is setup.json in the state folder: setup has been run or
// skipped, so it isn't offered again by itself.
type setupState struct {
	Done bool      `json:"done"`
	At   time.Time `json:"at"`
}

func setupStatePath() string { return filepath.Join(stateDir(), "setup.json") }

func setupDone() bool {
	var st setupState
	return readJSONFile(setupStatePath(), &st) && st.Done
}

func markSetupDone() error {
	return writeJSONFile(setupStatePath(), setupState{Done: true, At: nowFn()})
}

// setupNeeded is whether the app should offer setup by itself: it hasn't been
// run or skipped, and config.json doesn't already say how the person wants
// things (views or a default project). A config.json that can't be read is
// left to the person, since setup couldn't write it anyway.
func setupNeeded() bool {
	if setupDone() {
		return false
	}
	cfg, err := loadConfig()
	if err != nil {
		return false
	}
	return len(cfg.Views) == 0 && cfg.DefaultProject == ""
}

// apiSetupInfo is session's "setup" block.
func apiSetupInfo() map[string]any {
	return map[string]any{"needed": setupNeeded(), "defaultProject": configuredDefaultProject()}
}

// apiSetupSave is `setup-save`: validate, merge into config.json, mark setup
// done. In the demo the file is the demo state's own.
func apiSetupSave(payload string) (any, error) {
	updates, err := setupUpdates(payload)
	if err != nil {
		return nil, err
	}
	path := configPathFn()
	if demoMode && filepath.Base(filepath.Dir(path)) != "demo" {
		return nil, fmt.Errorf("demo setup refused: %s is not in the demo state folder", path)
	}
	if len(updates) > 0 {
		if err := mergeConfigFile(path, updates); err != nil {
			return nil, &apiError{Code: "config", Message: err.Error()}
		}
	}
	if err := markSetupDone(); err != nil {
		return nil, err
	}
	views, notes := viewsFn()
	return map[string]any{"saved": len(updates), "views": views, "viewNotes": nonNil(notes), "defaultProject": configuredDefaultProject()}, nil
}

// apiSetupSkip is `setup-skip`.
func apiSetupSkip() (any, error) {
	if err := markSetupDone(); err != nil {
		return nil, err
	}
	return map[string]any{"saved": 0}, nil
}

// ---- sites -----------------------------------------------------------------

// apiSites is `sites`: every site the account reaches, flagging the one in
// use and the ones that aren't Jira Cloud (which can't be chosen).
func apiSites(ctx context.Context, src apiSource) (any, error) {
	sites, err := src.sites(ctx)
	if err != nil {
		return nil, err
	}
	current := ""
	if demoMode {
		current = demoCloudID
	} else if creds, err := loadCredentials(); err == nil {
		current = creds.CloudID
	}
	out := make([]map[string]any, 0, len(sites))
	for _, s := range sites {
		host := ""
		if u, err := url.Parse(s.URL); err == nil {
			host = u.Host
		}
		name := s.Name
		if name == "" {
			name = host
		}
		out = append(out, map[string]any{"id": s.ID, "name": name, "url": s.URL, "host": host, "cloud": isCloudSite(s.URL), "current": s.ID == current})
	}
	return map[string]any{"sites": out, "current": current}, nil
}

// apiSiteSelect is `site-select <id>`: it stores the chosen site's cloud id
// and URL in the credentials, the same cache ensureCloudID fills on a
// single-site account, so every op uses it from then on. Caches are per site,
// so another site's lists are never shown.
func apiSiteSelect(ctx context.Context, src apiSource, id string) (any, error) {
	if demoMode {
		return nil, usageErr("the demo has one site")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, usageErr("site-select needs a site id")
	}
	sites, err := src.sites(ctx)
	if err != nil {
		return nil, err
	}
	var chosen *accessibleResource
	for i := range sites {
		if sites[i].ID == id {
			chosen = &sites[i]
		}
	}
	if chosen == nil {
		return nil, &apiError{Code: "invalid", Message: "That site isn't one this account can reach."}
	}
	if !isCloudSite(chosen.URL) {
		return nil, &apiError{Code: "not_cloud", Message: errNotCloud.Error()}
	}
	if err := withCredentialsLock(func(creds credentials, loadErr error) (credentials, error) {
		if loadErr != nil {
			return credentials{}, loadErr
		}
		creds.CloudID, creds.SiteURL = chosen.ID, chosen.URL
		return creds, nil
	}); err != nil {
		return nil, err
	}
	return map[string]any{"cloudId": chosen.ID, "siteURL": chosen.URL}, nil
}

// ---- JQL check -------------------------------------------------------------

// jqlSyntaxProblem catches the mistakes Jira would only report after a round
// trip: nothing typed, or a quote or bracket left open.
func jqlSyntaxProblem(jql string) string {
	jql = strings.TrimSpace(jql)
	if jql == "" {
		return "Type a JQL query."
	}
	depth := 0
	var quote byte
	for i := 0; i < len(jql); i++ {
		c := jql[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return "There is a ) with no ( before it."
			}
		}
	}
	if quote != 0 {
		return "A quote is never closed."
	}
	if depth > 0 {
		return "A ( is never closed."
	}
	return ""
}

// apiJQLCheck is `jql-check --query JQL`: run it once, as a tab would, and say
// what it matched. Jira's own complaint comes back as invalid_jql; a sign-in or
// network failure keeps its own code so the mod doesn't call the JQL wrong.
func apiJQLCheck(ctx context.Context, src apiSource, jql string) (any, error) {
	jql = strings.TrimSpace(jql)
	if msg := jqlSyntaxProblem(jql); msg != "" {
		return nil, &apiError{Code: "invalid_jql", Message: msg}
	}
	rows, more, err := src.runJQL(ctx, jql, viewMaxResults)
	if err != nil {
		ae := classifyAPIError(err)
		if ae.Code != "error" {
			return nil, err
		}
		msg := strings.TrimSpace(strings.TrimPrefix(ae.Message, "tool call failed:"))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, &apiError{Code: "invalid_jql", Message: msg}
	}
	return map[string]any{"valid": true, "count": len(rows), "more": more}, nil
}
