package main

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// demojql.go is a small JQL evaluator for the demo site, enough for the
// demo views, a user's likely edits to them, and view-scoped search:
// AND / OR / NOT-free clauses, parentheses, `field = / != / ~ / in / not in`
// on assignee, project, type, status, statusCategory, priority, key, parent,
// labels and summary, `filter = N` (a few canned filters) and ORDER BY
// updated, created or key. A clause it doesn't know matches everything.

// demoFilters are the demo site's saved filters, by id.
var demoFilters = map[string]string{
	"10234": "project = PLAT AND statusCategory != Done ORDER BY updated DESC",
}

func (d demoSource) runJQL(_ context.Context, jql string, max int) ([]ticketStatus, bool, error) {
	all := d.load()
	cond, order := splitOrderBy(expandDemoFilters(jql, 0))
	match := func(is demoIssue) bool { return demoEval(cond, is) }
	var out []ticketStatus
	for _, is := range all {
		if match(is) {
			out = append(out, d.status(is, all))
		}
	}
	sortDemo(out, order)
	more := false
	if max > 0 && len(out) > max {
		out, more = out[:max], true
	}
	return out, more, nil
}

var filterRefRE = regexp.MustCompile(`(?i)\bfilter\s*=\s*"?(\d+)"?`)

// expandDemoFilters replaces `filter = N` with the filter's own JQL, so it
// can be nested in parentheses like the rest.
func expandDemoFilters(jql string, depth int) string {
	if depth > 3 {
		return jql
	}
	return filterRefRE.ReplaceAllStringFunc(jql, func(m string) string {
		id := filterRefRE.FindStringSubmatch(m)[1]
		body, ok := demoFilters[id]
		if !ok {
			return "key = NONE-0"
		}
		cond, _ := splitOrderBy(expandDemoFilters(body, depth+1))
		return "(" + cond + ")"
	})
}

func sortDemo(out []ticketStatus, order string) {
	o := strings.ToLower(order)
	desc := !strings.Contains(o, "asc")
	field := "updated"
	for _, f := range []string{"key", "created", "updated"} {
		if strings.Contains(o, f) {
			field = f
		}
	}
	less := func(a, b ticketStatus) bool { return a.Updated < b.Updated }
	if field == "key" {
		less = func(a, b ticketStatus) bool { return a.Key < b.Key }
	}
	if o == "" {
		desc = true
	}
	sort.SliceStable(out, func(i, j int) bool {
		if desc {
			return less(out[j], out[i])
		}
		return less(out[i], out[j])
	})
}

// splitTop splits s at the keyword (" and " / " or ") outside quotes and
// parentheses.
func splitTop(s, word string) []string {
	var parts []string
	depth, start := 0, 0
	inQuote := false
	lower := strings.ToLower(s)
	sep := " " + word + " "
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && inQuote:
			i++
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(lower[i:], sep):
			parts = append(parts, s[start:i])
			start = i + len(sep)
			i += len(sep) - 1
		}
	}
	return append(parts, s[start:])
}

func stripParens(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		return s, false
	}
	depth, inQuote := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && inQuote:
			i++
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 && i != len(s)-1 {
				return s, false
			}
		}
	}
	return strings.TrimSpace(s[1 : len(s)-1]), true
}

func demoEval(expr string, is demoIssue) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true
	}
	if ors := splitTop(expr, "or"); len(ors) > 1 {
		for _, p := range ors {
			if demoEval(p, is) {
				return true
			}
		}
		return false
	}
	if ands := splitTop(expr, "and"); len(ands) > 1 {
		for _, p := range ands {
			if !demoEval(p, is) {
				return false
			}
		}
		return true
	}
	if inner, ok := stripParens(expr); ok {
		return demoEval(inner, is)
	}
	return demoClause(expr, is)
}

var clauseRE = regexp.MustCompile(`(?is)^\s*([A-Za-z]+)\s*(!=|=|~|\bnot\s+in\b|\bin\b)\s*(.+?)\s*$`)

func demoValues(v string) []string {
	v = strings.TrimSpace(v)
	if inner, ok := stripParens(v); ok {
		v = inner
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		p = strings.ReplaceAll(p, `\"`, `"`)
		out = append(out, p)
	}
	return out
}

func demoClause(c string, is demoIssue) bool {
	m := clauseRE.FindStringSubmatch(c)
	if m == nil {
		return true
	}
	field, op := strings.ToLower(m[1]), strings.ToLower(strings.Join(strings.Fields(m[2]), " "))
	vals := demoValues(m[3])
	var have []string
	switch field {
	case "assignee":
		have = []string{is.Assignee}
		for i, v := range vals {
			if strings.EqualFold(v, "currentUser()") {
				vals[i] = demoUser
			}
		}
	case "project":
		have = []string{strings.SplitN(is.Key, "-", 2)[0]}
	case "type", "issuetype":
		have = []string{is.Type}
	case "status":
		have = []string{is.Status}
	case "statuscategory":
		have = []string{demoCategoryName(demoCategory(is.Status))}
	case "priority":
		have = []string{is.Priority}
	case "key":
		have = []string{is.Key}
	case "parent":
		have = []string{is.Parent}
	case "labels", "label":
		have = is.Labels
	case "summary", "text":
		have = []string{is.Summary}
	default:
		return true
	}
	any := func(match func(have, want string) bool) bool {
		for _, h := range have {
			for _, w := range vals {
				if match(h, w) {
					return true
				}
			}
		}
		return false
	}
	eq := func(h, w string) bool { return strings.EqualFold(h, w) }
	switch op {
	case "=", "in":
		return any(eq)
	case "!=", "not in":
		return !any(eq)
	case "~":
		return any(func(h, w string) bool {
			h, w = strings.ToLower(h), strings.ToLower(w)
			if field == "key" {
				return strings.HasPrefix(h, strings.TrimSuffix(w, "*"))
			}
			return strings.Contains(h, strings.TrimSuffix(w, "*"))
		})
	}
	return true
}

// demoCategoryName is the category as Jira's JQL names it.
func demoCategoryName(key string) string {
	switch key {
	case "new":
		return "To Do"
	case "indeterminate":
		return "In Progress"
	}
	return "Done"
}
