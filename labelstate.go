package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// labelState is labels.json: the last tick's time, and the most recently
// known status per ticket key, so a Jira failure (signed out, offline) can
// fall back to what was last shown instead of clearing it.
type labelState struct {
	LastTick time.Time             `json:"last_tick"`
	Cache    map[string]cacheEntry `json:"cache"` // ticket key -> last known answer
}

type cacheEntry struct {
	Fetched time.Time    `json:"fetched"`
	Status  ticketStatus `json:"status"`
}

func labelStatePath() string { return filepath.Join(stateDir(), "labels.json") }

func readLabelState() labelState {
	var st labelState
	if data, err := os.ReadFile(labelStatePath()); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Cache == nil {
		st.Cache = map[string]cacheEntry{}
	}
	return st
}

// writeLabelState writes labels.json atomically (temp file, then rename).
// This state is a cache, not a secret, so it doesn't need the 0600 +
// flock treatment credentials.json gets — a lost or corrupted write just
// means the next tick re-asks Jira.
func writeLabelState(st labelState) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".labels-*.json.tmp")
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
	return os.Rename(tmpPath, labelStatePath())
}

// invalidateLabelCache drops key's cached ticket status, so the next tick
// re-asks Jira for it instead of serving a stale answer until the TTL
// expires. Used after a transition changes a ticket the sidebar might be
// showing. Best-effort: a lock failure or missing state file just means
// there was nothing to invalidate.
func invalidateLabelCache(key string) {
	lock, err := os.OpenFile(filepath.Join(stateDir(), "tick.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	st := readLabelState()
	if _, ok := st.Cache[key]; !ok {
		return
	}
	delete(st.Cache, key)
	_ = writeLabelState(st)
}
