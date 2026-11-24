package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// views.go is the Herdr app's tabs. config.json's optional "views" list
// defines them; each entry is a built-in ({"builtin": "my-work"} or
// {"builtin": "epics"}), custom JQL ({"name": …, "jql": …}) or a saved Jira
// filter ({"name": …, "filter": 10234}, which runs as `filter = 10234`).
// Bad entries are skipped and reported as notes, never as a failure.

const (
	viewMyWork = "my-work"
	viewEpics  = "epics"
	viewJQL    = "jql" // custom JQL, or a saved filter
)

const (
	groupByStatus = "status"
	groupByNone   = "none"
)

// myWorkJQL is the My work built-in: everything assigned to the signed-in
// user that isn't Done, of any type (epics show as ordinary rows).
const myWorkJQL = "assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC"

// viewMaxResults bounds one view to a page; the tab shows "50+" past it.
const viewMaxResults = 50

// viewDef is one resolved tab.
type viewDef struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	JQL     string `json:"jql,omitempty"`
	GroupBy string `json:"groupBy"`
}

// viewEntry is one config.json "views" entry as written.
type viewEntry struct {
	Builtin string          `json:"builtin"`
	Name    string          `json:"name"`
	JQL     string          `json:"jql"`
	Filter  json.RawMessage `json:"filter"`
	GroupBy string          `json:"group_by"`
}

// defaultViews is what the app shows when config.json has no usable views.
func defaultViews() []viewDef {
	return []viewDef{{ID: viewMyWork, Name: "My work", Kind: viewMyWork, JQL: myWorkJQL, GroupBy: groupByStatus}}
}

// resolveViews turns the raw "views" value into tabs plus a note for every
// entry it skipped. Unset, empty or wholly invalid yields the default.
func resolveViews(raw json.RawMessage) ([]viewDef, []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return defaultViews(), nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return defaultViews(), []string{`config.json: "views" must be a list; showing My work`}
	}
	var out []viewDef
	var notes []string
	seen := map[string]bool{}
	for i, e := range entries {
		v, err := parseViewEntry(e, len(out)+1)
		if err == nil && v.Kind != viewJQL && seen[v.Kind] {
			err = fmt.Errorf("%q is already a tab", v.Kind)
		}
		if err != nil {
			notes = append(notes, fmt.Sprintf("views[%d] skipped: %v", i, err))
			continue
		}
		seen[v.Kind] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		if len(entries) > 0 {
			notes = append(notes, "no valid views; showing My work")
		}
		return defaultViews(), notes
	}
	return out, notes
}

// parseViewEntry validates one entry. pos is its 1-based place among the
// valid ones, which names a custom view's id.
func parseViewEntry(raw json.RawMessage, pos int) (viewDef, error) {
	var e viewEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return viewDef{}, fmt.Errorf("it must be an object with builtin, jql or filter (check the field types)")
	}
	e.Builtin, e.Name, e.JQL, e.GroupBy = strings.TrimSpace(e.Builtin), strings.TrimSpace(e.Name), strings.TrimSpace(e.JQL), strings.TrimSpace(e.GroupBy)
	hasFilter := len(e.Filter) > 0 && string(e.Filter) != "null"
	kinds := 0
	for _, set := range []bool{e.Builtin != "", e.JQL != "", hasFilter} {
		if set {
			kinds++
		}
	}
	if kinds != 1 {
		return viewDef{}, fmt.Errorf("it needs exactly one of builtin, jql or filter")
	}
	groupBy := groupByStatus
	switch strings.ToLower(e.GroupBy) {
	case "":
	case groupByStatus, groupByNone:
		groupBy = strings.ToLower(e.GroupBy)
	default:
		return viewDef{}, fmt.Errorf(`group_by %q must be "status" or "none"`, e.GroupBy)
	}
	switch {
	case e.Builtin != "":
		name := e.Name
		switch e.Builtin {
		case viewMyWork:
			if name == "" {
				name = "My work"
			}
			return viewDef{ID: viewMyWork, Name: name, Kind: viewMyWork, JQL: myWorkJQL, GroupBy: groupBy}, nil
		case viewEpics:
			if name == "" {
				name = "Epics"
			}
			return viewDef{ID: viewEpics, Name: name, Kind: viewEpics, GroupBy: groupByStatus}, nil
		}
		return viewDef{}, fmt.Errorf(`unknown builtin %q (use "my-work" or "epics")`, e.Builtin)
	case hasFilter:
		id, err := filterID(e.Filter)
		if err != nil {
			return viewDef{}, err
		}
		name := e.Name
		if name == "" {
			name = "Filter " + id
		}
		return viewDef{ID: fmt.Sprintf("view-%d", pos), Name: name, Kind: viewJQL, JQL: "filter = " + id, GroupBy: groupBy}, nil
	}
	if e.Name == "" {
		return viewDef{}, fmt.Errorf("a jql view needs a name")
	}
	return viewDef{ID: fmt.Sprintf("view-%d", pos), Name: e.Name, Kind: viewJQL, JQL: e.JQL, GroupBy: groupBy}, nil
}

// filterID reads a saved filter's id, a positive whole number written as a
// number or a string.
func filterID(raw json.RawMessage) (string, error) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("filter must be a saved filter's id, such as 10234")
	}
	return strconv.FormatInt(n, 10), nil
}

// configuredViews reads the views from config.json. A malformed file yields
// the default and a note.
func configuredViews() ([]viewDef, []string) {
	cfg, err := loadConfig()
	views, notes := resolveViews(cfg.Views)
	if err != nil {
		notes = append(notes, err.Error())
	}
	return views, notes
}

// viewsFn is a seam: the demo site brings its own views.
var viewsFn = configuredViews

// findView resolves an op's view argument: a view's id, or its 0-based index.
func findView(arg string) (viewDef, error) {
	views, _ := viewsFn()
	for _, v := range views {
		if v.ID == arg {
			return v, nil
		}
	}
	if n, err := strconv.Atoi(arg); err == nil && n >= 0 && n < len(views) {
		return views[n], nil
	}
	return viewDef{}, usageErr("no view %q", arg)
}

// viewFromDef builds a view from a definition passed as JSON, as written in
// config.json.
func viewFromDef(def string) (viewDef, error) {
	v, err := parseViewEntry(json.RawMessage(def), 0)
	if err != nil {
		return viewDef{}, usageErr("view definition: %v", err)
	}
	if v.Kind == viewJQL {
		v.ID = "def-" + shortHash(v.JQL)
	}
	return v, nil
}

func shortHash(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// viewCacheKey names a view's cache entry: its kind and its query, so two
// views with the same JQL share one and an edited query starts fresh.
func viewCacheKey(v viewDef) string {
	return v.Kind + ":" + shortHash(v.JQL)
}

// splitOrderBy separates a JQL string's trailing ORDER BY, which can't sit
// inside parentheses.
func splitOrderBy(jql string) (cond, order string) {
	jql = strings.TrimSpace(jql)
	inQuote, esc := false, false
	at := -1
	lower := strings.ToLower(jql)
	for i := 0; i < len(jql); i++ {
		c := jql[i]
		switch {
		case esc:
			esc = false
		case c == '\\':
			esc = inQuote
		case c == '"':
			inQuote = !inQuote
		case !inQuote && strings.HasPrefix(lower[i:], "order by") &&
			(i == 0 || jql[i-1] == ' ' || jql[i-1] == ')') &&
			(i+8 == len(jql) || jql[i+8] == ' '):
			at = i
		}
	}
	if at < 0 {
		return jql, ""
	}
	return strings.TrimSpace(jql[:at]), strings.TrimSpace(jql[at:])
}

// searchViewJQL scopes a search to a view: the view's own JQL, then the
// query as a key-prefix or summary match.
func searchViewJQL(viewJQL, query string) string {
	query = strings.TrimSpace(query)
	var match string
	if keyShapedRE.MatchString(query) {
		match = `key ~ "` + escapeJQL(strings.ToUpper(query)) + `*"`
	} else {
		match = `summary ~ "` + escapeJQL(query) + `*"`
	}
	cond, order := splitOrderBy(viewJQL)
	jql := "(" + match + ")"
	if cond != "" {
		jql = "(" + cond + ") AND " + jql
	}
	if order != "" {
		jql += " " + order
	}
	return jql
}
