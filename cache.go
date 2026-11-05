package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// issuesCacheFresh reports whether the Issues cache was written within
// issuesCacheTTL. The cache file is a bare array with no timestamp of its
// own, so its mtime is the fetch time (every write is a fresh temp file
// renamed into place).
func issuesCacheFresh() bool {
	fi, err := os.Stat(pickerCachePath())
	return err == nil && nowFn().Sub(fi.ModTime()) < issuesCacheTTL
}

// epicsCache is the Epics tab's cached data. FetchedAt is kept explicitly
// because starring an epic rewrites this file without refetching from Jira.
type epicsCache struct {
	FetchedAt   time.Time
	Epics       []ticketStatus
	More        bool
	Starred     []ticketStatus
	StarredKeys []string
	CloudID     string
}

func (c epicsCache) fresh() bool { return nowFn().Sub(c.FetchedAt) < epicsCacheTTL }

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

// pickerCachePath is where the last "your issues" list is cached, so the
// popup can paint instantly before refreshing from Jira.
func pickerCachePath() string { return filepath.Join(stateDir(), "picker_cache.json") }

func readPickerCache() []ticketStatus {
	data, err := os.ReadFile(pickerCachePath())
	if err != nil {
		return nil
	}
	var out []ticketStatus
	_ = json.Unmarshal(data, &out)
	return out
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

// writePickerCache stores the Issues lane: issues followed by sub-tasks. It
// never receives epics or search hits.
func writePickerCache(issues, subtasks []ticketStatus) error {
	all := make([]ticketStatus, 0, len(issues)+len(subtasks))
	all = append(append(all, issues...), subtasks...)
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return writeFileAtomic(pickerCachePath(), ".picker_cache-*.json.tmp", data)
}
