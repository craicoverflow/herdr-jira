package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// starState is starred_epics.json: epics the user has pinned that they
// don't own (found via the epics tab's [ctrl+f] search), so the starred
// list survives restarts. Epics is ordered star-order, oldest first, and
// that order is preserved across add/remove so the list doesn't reshuffle
// on every load.
type starState struct {
	Epics []starredEpic `json:"epics"`
}

// starredEpic keeps the last known status alongside the key, mirroring
// labelState's cacheEntry — so one epic that's since been deleted or made
// inaccessible falls back to showing what was last seen for that row
// instead of a failed batch lookup blanking the whole starred list.
type starredEpic struct {
	Key    string       `json:"key"`
	Status ticketStatus `json:"status"`
}

func starStatePath() string { return filepath.Join(stateDir(), "starred_epics.json") }

func readStarState() starState {
	var st starState
	if data, err := os.ReadFile(starStatePath()); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

// writeStarState writes starred_epics.json atomically (temp file, then
// rename) — the same approach labelstate.go uses. This is a local pin
// list, not a secret, so it doesn't need the 0600 + flock treatment
// credentials.json gets.
func writeStarState(st starState) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".starred-epics-*.json.tmp")
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
	return os.Rename(tmpPath, starStatePath())
}

func (st starState) index(key string) int {
	for i, e := range st.Epics {
		if e.Key == key {
			return i
		}
	}
	return -1
}

func (st starState) has(key string) bool { return st.index(key) >= 0 }

// add pins status's key. Returns false (no change made) if it was already
// starred, so a caller driving a toggle key can fall back to unstarring.
func (st *starState) add(status ticketStatus) bool {
	if st.has(status.Key) {
		return false
	}
	st.Epics = append(st.Epics, starredEpic{Key: status.Key, Status: status})
	return true
}

// remove unpins key. Returns false if it wasn't starred.
func (st *starState) remove(key string) bool {
	i := st.index(key)
	if i < 0 {
		return false
	}
	st.Epics = append(st.Epics[:i], st.Epics[i+1:]...)
	return true
}

// updateStatus refreshes a starred key's cached status in place. A no-op
// if status's key isn't starred (e.g. it was unstarred while a background
// refresh was in flight).
func (st *starState) updateStatus(status ticketStatus) {
	if i := st.index(status.Key); i >= 0 {
		st.Epics[i].Status = status
	}
}

// statuses returns the starred epics in star order, ready for
// setListIssues.
func (st starState) statuses() []ticketStatus {
	out := make([]ticketStatus, len(st.Epics))
	for i, e := range st.Epics {
		out[i] = e.Status
	}
	return out
}
