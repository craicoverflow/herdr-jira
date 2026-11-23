package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// How long a cached list is trusted before opening the popup refetches it.
// Within the TTL the popup paints from disk and makes no API call; past it the
// cache still paints at once and is refreshed in the background. [r] always
// refetches.
const (
	issuesCacheTTL = 30 * time.Minute
	epicsCacheTTL  = 3 * time.Hour
)

// patchGrace is how long a local change to a cached list (a transition, an
// assign) outranks a list fetched from Jira. A fetch that was already running
// when the change was made returns the old row, and Jira's search index can
// trail a write by a few seconds, so a fetch made just after it can too.
const patchGrace = 30 * time.Second

// The kinds of local change a cachePatch replays.
const (
	patchStatus = "status" // Row is the issue as it is now, after a transition
	patchMine   = "mine"   // Row was just assigned to the user
)

// cachePatch is one local change to the cached lists. The caches keep the
// recent ones so that a list fetched around the change gets it replayed
// instead of undoing it.
type cachePatch struct {
	At  time.Time
	Op  string
	Row ticketStatus
}

// livePatches is the patches made less than patchGrace before since, the
// start of a fetch: the ones that fetch may not have seen.
func livePatches(ps []cachePatch, since time.Time) []cachePatch {
	cutoff := since.Add(-patchGrace)
	var out []cachePatch
	for _, p := range ps {
		if p.At.After(cutoff) {
			out = append(out, p)
		}
	}
	return out
}

// sameSite reports whether a cache written for cached belongs to the site
// signed in now. An empty ID on either side (a cache from before IDs were
// kept, or no sign-in on record) is taken as a match.
func sameSite(cached, current string) bool {
	return cached == "" || current == "" || cached == current
}

// cacheLockName guards every read-modify-write of the list caches and the
// transitions cache, which the TUI popup, the app mod and a second app window
// all make.
const cacheLockName = "cache.lock"

// withStateLock runs fn holding an exclusive lock on the file name in the
// state folder.
func withStateLock(name string, fn func() error) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// ---- Issues -------------------------------------------------------------------

// issuesCache is the Issues tab's cached data: issues followed by sub-tasks
// in one list. FetchedAt is kept explicitly (as epicsCache does) because
// local changes rewrite the file without refetching from Jira.
type issuesCache struct {
	FetchedAt time.Time
	CloudID   string `json:",omitempty"`
	Rows      []ticketStatus
	Patches   []cachePatch `json:",omitempty"`
}

func (c issuesCache) fresh() bool { return nowFn().Sub(c.FetchedAt) < issuesCacheTTL }

// apply makes one local change to the rows.
func (c *issuesCache) apply(p cachePatch) {
	switch p.Op {
	case patchStatus:
		c.Rows = patchRows(c.Rows, p.Row, true)
	case patchMine:
		if p.Row.HierarchyLevel <= 0 && !isDone(p.Row) {
			c.Rows = putFirst(c.Rows, p.Row)
		}
	}
}

func issuesCachePath() string { return filepath.Join(stateDir(), "issues_cache.json") }

// legacyIssuesCachePath is the Issues cache as it was before it held its own
// fetch time: a bare array whose mtime was the fetch time. It is read until
// the first fetch writes issues_cache.json, and never written, so an older
// helper that still uses it can't misread the new format.
func legacyIssuesCachePath() string { return filepath.Join(stateDir(), "picker_cache.json") }

func readIssuesCache() (issuesCache, bool) {
	if data, err := os.ReadFile(issuesCachePath()); err == nil {
		var c issuesCache
		if json.Unmarshal(data, &c) != nil || c.FetchedAt.IsZero() {
			return issuesCache{}, false
		}
		return c, true
	}
	fi, err := os.Stat(legacyIssuesCachePath())
	if err != nil {
		return issuesCache{}, false
	}
	data, err := os.ReadFile(legacyIssuesCachePath())
	if err != nil {
		return issuesCache{}, false
	}
	var rows []ticketStatus
	if json.Unmarshal(data, &rows) != nil {
		return issuesCache{}, false
	}
	return issuesCache{FetchedAt: fi.ModTime(), Rows: rows}, true
}

func writeIssuesCache(c issuesCache) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(issuesCachePath(), ".issues_cache-*.json.tmp", data)
}

// storeFetchedIssues caches the Issues lane as fetched from Jira by a fetch
// that began at started. Local changes made since then (less patchGrace) are
// replayed over it, and the result, which is what callers should show, is
// returned. It never receives epics or search hits.
func storeFetchedIssues(started time.Time, issues, subtasks []ticketStatus) (outIssues, outSubtasks []ticketStatus) {
	rows := make([]ticketStatus, 0, len(issues)+len(subtasks))
	rows = append(append(rows, issues...), subtasks...)
	next := issuesCache{FetchedAt: nowFn(), CloudID: cloudIDFn(), Rows: rows}
	_ = withStateLock(cacheLockName, func() error {
		if old, ok := readIssuesCache(); ok && sameSite(old.CloudID, next.CloudID) {
			next.Patches = livePatches(old.Patches, started)
			for _, p := range next.Patches {
				next.apply(p)
			}
		}
		return writeIssuesCache(next)
	})
	return splitCached(next.Rows)
}

// splitCached separates a cached list back into the two Issues-lane queries.
// The file is one flat array: sub-tasks are the rows with a negative
// hierarchy level. Caches written before that field existed hold every row
// as a normal issue, so sub-tasks show at top level until the first refresh.
func splitCached(cached []ticketStatus) (issues, subtasks []ticketStatus) {
	for _, t := range cached {
		if t.HierarchyLevel < 0 {
			subtasks = append(subtasks, t)
		} else {
			issues = append(issues, t)
		}
	}
	return issues, subtasks
}

// ---- Epics --------------------------------------------------------------------

// epicsCache is the Epics tab's cached data. FetchedAt is kept explicitly
// because starring an epic rewrites this file without refetching from Jira.
type epicsCache struct {
	FetchedAt   time.Time
	Epics       []ticketStatus
	More        bool
	Starred     []ticketStatus
	StarredKeys []string
	CloudID     string
	Patches     []cachePatch `json:",omitempty"`
}

func (c epicsCache) fresh() bool { return nowFn().Sub(c.FetchedAt) < epicsCacheTTL }

// apply makes one local change to the lanes. A Done epic leaves the assigned
// lane but stays starred.
func (c *epicsCache) apply(p cachePatch) {
	switch p.Op {
	case patchStatus:
		c.Epics = patchRows(c.Epics, p.Row, true)
		c.Starred = patchRows(c.Starred, p.Row, false)
	case patchMine:
		if p.Row.HierarchyLevel > 0 && p.Row.IssueType != "" && !isDone(p.Row) {
			c.Epics = putFirst(c.Epics, p.Row)
		}
	}
}

func epicsCachePath() string { return filepath.Join(stateDir(), "epics_cache.json") }

func readEpicsCache() (epicsCache, bool) {
	data, err := os.ReadFile(epicsCachePath())
	if err != nil {
		return epicsCache{}, false
	}
	var c epicsCache
	if json.Unmarshal(data, &c) != nil || c.FetchedAt.IsZero() {
		return epicsCache{}, false
	}
	return c, true
}

func writeEpicsCache(c epicsCache) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeFileAtomic(epicsCachePath(), ".epics_cache-*.json.tmp", data)
}

// storeFetchedEpics caches the Epics tab as fetched by a fetch that began at
// started, replaying local changes made since, and returns what was stored.
func storeFetchedEpics(started time.Time, c epicsCache) epicsCache {
	_ = withStateLock(cacheLockName, func() error {
		if old, ok := readEpicsCache(); ok && sameSite(old.CloudID, c.CloudID) {
			c.Patches = livePatches(old.Patches, started)
			for _, p := range c.Patches {
				c.apply(p)
			}
		}
		return writeEpicsCache(c)
	})
	return c
}

// updateEpicsCache changes the cached Epics tab in place under the cache
// lock, for a local change (a star). fn reports whether it changed anything;
// nothing is written when there is no cache, or it belongs to another site.
func updateEpicsCache(cloudID string, fn func(c *epicsCache) bool) {
	_ = withStateLock(cacheLockName, func() error {
		c, ok := readEpicsCache()
		if !ok || !sameSite(c.CloudID, cloudID) || !fn(&c) {
			return nil
		}
		return writeEpicsCache(c)
	})
}

// ---- local changes ----------------------------------------------------------

// recordPatch makes a local change to both cached lists and keeps it for
// fetches still running to replay. Each cache keeps its own fetch time, so a
// local change never extends its TTL.
func recordPatch(p cachePatch) {
	if p.At.IsZero() {
		p.At = nowFn()
	}
	cloudID := cloudIDFn()
	_ = withStateLock(cacheLockName, func() error {
		if c, ok := readIssuesCache(); ok && sameSite(c.CloudID, cloudID) {
			c.apply(p)
			c.Patches = append(livePatches(c.Patches, p.At), p)
			_ = writeIssuesCache(c)
		}
		if c, ok := readEpicsCache(); ok && sameSite(c.CloudID, cloudID) {
			c.apply(p)
			c.Patches = append(livePatches(c.Patches, p.At), p)
			_ = writeEpicsCache(c)
		}
		return nil
	})
}

// patchRows replaces t's row in rows (dropping it when dropDone and t is now
// Done), keeping display fields t lacks from the old row.
func patchRows(rows []ticketStatus, t ticketStatus, dropDone bool) []ticketStatus {
	out := rows[:0:0]
	for _, r := range rows {
		if r.Key == t.Key {
			if dropDone && isDone(t) {
				continue
			}
			r = mergeDisplay(t, r)
		}
		out = append(out, r)
	}
	return out
}

// putFirst moves t (or adds it) to the top of rows.
func putFirst(rows []ticketStatus, t ticketStatus) []ticketStatus {
	out := []ticketStatus{t}
	for _, r := range rows {
		if r.Key != t.Key {
			out = append(out, r)
		}
	}
	return out
}

// mergeDisplay is t with any display field it lacks taken from old.
func mergeDisplay(t, old ticketStatus) ticketStatus {
	if t.ParentSummary == "" {
		t.ParentSummary = old.ParentSummary
	}
	if t.Priority == "" {
		t.Priority = old.Priority
	}
	if t.AssigneeName == "" {
		t.AssigneeName = old.AssigneeName
	}
	return t
}

// writeFileAtomic writes data to path via a temp file in the same directory.
func writeFileAtomic(path, tmpPattern string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, tmpPattern)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
