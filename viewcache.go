package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// viewcache.go caches each view's rows, one entry per viewCacheKey in one
// file, with the same TTL and local-change replay as the Issues cache.

const viewsCacheTTL = issuesCacheTTL

// maxViewCaches bounds the file: the least recently fetched entries go first.
const maxViewCaches = 24

type viewCacheEntry struct {
	FetchedAt time.Time
	CloudID   string `json:",omitempty"`
	Kind      string
	Rows      []ticketStatus
	More      bool
	Patches   []cachePatch `json:",omitempty"`
}

func (e viewCacheEntry) fresh() bool { return nowFn().Sub(e.FetchedAt) < viewsCacheTTL }

// apply makes one local change to the rows. Only My work drops a row that
// turns Done or takes one just assigned: a custom view's JQL says what
// belongs, and the next fetch settles it.
func (e *viewCacheEntry) apply(p cachePatch) {
	switch p.Op {
	case patchStatus:
		e.Rows = patchRows(e.Rows, p.Row, e.Kind == viewMyWork)
	case patchMine:
		if e.Kind == viewMyWork && !isDone(p.Row) {
			e.Rows = putFirst(e.Rows, p.Row)
		}
	}
}

func viewsCachePath() string { return filepath.Join(stateDir(), "views_cache.json") }

func readViewsCache() map[string]viewCacheEntry {
	data, err := os.ReadFile(viewsCachePath())
	if err != nil {
		return map[string]viewCacheEntry{}
	}
	var m map[string]viewCacheEntry
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string]viewCacheEntry{}
	}
	return m
}

func readViewCache(key string) (viewCacheEntry, bool) {
	e, ok := readViewsCache()[key]
	if !ok || e.FetchedAt.IsZero() {
		return viewCacheEntry{}, false
	}
	return e, true
}

func writeViewsCache(m map[string]viewCacheEntry) error {
	if len(m) > maxViewCaches {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]].FetchedAt.After(m[keys[j]].FetchedAt) })
		for _, k := range keys[maxViewCaches:] {
			delete(m, k)
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(viewsCachePath(), ".views_cache-*.json.tmp", data)
}

// storeFetchedView caches a view as fetched by a fetch that began at
// started, replays local changes made since, and returns the rows to show.
func storeFetchedView(started time.Time, key string, v viewDef, rows []ticketStatus, more bool) []ticketStatus {
	next := viewCacheEntry{FetchedAt: nowFn(), CloudID: cloudIDFn(), Kind: v.Kind, Rows: rows, More: more}
	_ = withStateLock(cacheLockName, func() error {
		m := readViewsCache()
		if old, ok := m[key]; ok && sameSite(old.CloudID, next.CloudID) {
			next.Patches = livePatches(old.Patches, started)
			for _, p := range next.Patches {
				next.apply(p)
			}
		}
		m[key] = next
		return writeViewsCache(m)
	})
	return next.Rows
}

// patchViewCaches replays a local change over every cached view. The caller
// holds the cache lock.
func patchViewCaches(p cachePatch, cloudID string) {
	m := readViewsCache()
	changed := false
	for k, e := range m {
		if !sameSite(e.CloudID, cloudID) {
			continue
		}
		e.apply(p)
		e.Patches = append(livePatches(e.Patches, p.At), p)
		m[k] = e
		changed = true
	}
	if changed {
		_ = writeViewsCache(m)
	}
}
