package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// transitioncache.go keeps the transitions Jira offers from each step of a
// workflow, so the status menu opens without a call. Which transitions an
// issue has depends on its workflow (chosen by project and issue type) and on
// the status it is in, not on the issue itself, so one fetch serves every
// issue at that step. The exception is a transition with a condition (only
// the assignee may, every sub-task must be done): Jira hides it per issue, so
// a cached list can offer one Jira then refuses, or lack one it would allow.
// A refused transition forgets the project's lists, and refresh refetches.

// transitionsTTL is how long a step's transitions are trusted. Workflows
// change rarely, and only when an admin edits them.
const transitionsTTL = 24 * time.Hour

type transitionsEntry struct {
	FetchedAt   time.Time
	Transitions []transitionOption
}

// transitionsFile is transitions_cache.json: one site's lists, by step.
type transitionsFile struct {
	CloudID string
	Steps   map[string]transitionsEntry
}

func transitionsCachePath() string { return filepath.Join(stateDir(), "transitions_cache.json") }

// workflowStep names the step key's issue is at: its project (the key's
// prefix), issue type and status, the last two compared without case. It is
// "" when the type or status is unknown.
func workflowStep(key, issueType, status string) string {
	project, _, ok := strings.Cut(key, "-")
	if !ok || project == "" || issueType == "" || status == "" {
		return ""
	}
	return strings.ToUpper(project) + "|" + strings.ToLower(issueType) + "|" + strings.ToLower(status)
}

// readTransitionsCache returns cloudID's lists. A missing or corrupt file, or
// one kept for another site, reads as empty.
func readTransitionsCache(cloudID string) transitionsFile {
	empty := transitionsFile{CloudID: cloudID, Steps: map[string]transitionsEntry{}}
	data, err := os.ReadFile(transitionsCachePath())
	if err != nil {
		return empty
	}
	var f transitionsFile
	if json.Unmarshal(data, &f) != nil || f.CloudID != cloudID || f.Steps == nil {
		return empty
	}
	return f
}

// updateTransitionsCache changes cloudID's lists under the cache lock. fn
// reports whether it changed anything. Expired lists are dropped on write.
func updateTransitionsCache(cloudID string, fn func(f *transitionsFile) bool) {
	if cloudID == "" {
		return
	}
	_ = withStateLock(cacheLockName, func() error {
		f := readTransitionsCache(cloudID)
		if !fn(&f) {
			return nil
		}
		for step, e := range f.Steps {
			if nowFn().Sub(e.FetchedAt) >= transitionsTTL {
				delete(f.Steps, step)
			}
		}
		data, err := json.Marshal(f)
		if err != nil {
			return err
		}
		return writeFileAtomic(transitionsCachePath(), ".transitions_cache-*.json.tmp", data)
	})
}

// transitionsFor answers key's transitions. issueType and status are where
// the caller last saw the issue: when the cache holds a list for that step
// younger than transitionsTTL, it is returned without asking Jira. Otherwise,
// or with refresh, Jira is asked, and the issue is read at the same time so
// the list is kept under the step the issue is really at (a list row's
// status can be out of date, and a list kept under the wrong step would be
// offered for every issue there).
func transitionsFor(ctx context.Context, src jiraSource, key, issueType, status string, refresh bool) ([]transitionOption, error) {
	if step := workflowStep(key, issueType, status); step != "" && !refresh {
		if cloudID := cloudIDFn(); cloudID != "" {
			if e, ok := readTransitionsCache(cloudID).Steps[step]; ok && nowFn().Sub(e.FetchedAt) < transitionsTTL {
				return e.Transitions, nil
			}
		}
	}
	var at issueDetail
	var atErr error
	read := make(chan struct{})
	go func() {
		defer close(read)
		at, atErr = src.get(ctx, key)
	}()
	list, err := src.transitions(ctx, key)
	<-read
	if err != nil {
		return nil, err
	}
	// An empty list usually means you may not move this issue (a permission
	// given only to its assignee, say), which says nothing about the next one.
	if step := workflowStep(key, at.IssueType, at.StatusName); atErr == nil && step != "" && len(list) > 0 {
		updateTransitionsCache(cloudIDFn(), func(f *transitionsFile) bool {
			f.Steps[step] = transitionsEntry{FetchedAt: nowFn(), Transitions: list}
			return true
		})
	}
	return list, nil
}

// forgetTransitions drops every list kept for key's project, so the next
// look asks Jira. A refused transition calls it: the list that offered it
// may be out of date.
func forgetTransitions(key string) {
	project, _, ok := strings.Cut(key, "-")
	if !ok {
		return
	}
	prefix := strings.ToUpper(project) + "|"
	updateTransitionsCache(cloudIDFn(), func(f *transitionsFile) bool {
		changed := false
		for step := range f.Steps {
			if strings.HasPrefix(step, prefix) {
				delete(f.Steps, step)
				changed = true
			}
		}
		return changed
	})
}

// transitionIssue moves key through transition id, forgetting the project's
// cached lists when Jira refuses.
func transitionIssue(ctx context.Context, src jiraSource, key, id string) error {
	err := src.transition(ctx, key, id)
	if err != nil {
		forgetTransitions(key)
	}
	return err
}
