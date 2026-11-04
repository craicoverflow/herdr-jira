package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// starred_epics.json holds the epics the user has starred. Stars are local to
// the plugin (never written to Jira) and grouped by the Jira site (cloud ID)
// they belong to:
//
//	{"sites": {"<cloudId>": {"epics": [{"key": "PROJ-1", "summary": "…"}]}}}
//
// The previous shape was a top-level {"epics": [{"key", "status"}]} list. It
// is read as belonging to the current site and moved under it on the next
// write.

// starEntry is one starred epic. The summary is kept only so a human reading
// the file can tell what a key was; status always comes from Jira.
type starEntry struct {
	Key     string `json:"key"`
	Summary string `json:"summary,omitempty"`
}

type siteStars struct {
	Epics []starEntry `json:"epics"`
}

// legacyStar is the pre-sites entry shape, which also carried a cached status.
type legacyStar struct {
	Key    string       `json:"key"`
	Status ticketStatus `json:"status"`
}

type starFile struct {
	// Epics is the legacy top-level list. omitempty keeps it out of files
	// written by this version, so an older binary reading one sees no stars
	// instead of misreading the new shape.
	Epics []legacyStar         `json:"epics,omitempty"`
	Sites map[string]siteStars `json:"sites,omitempty"`
}

func starStatePath() string { return filepath.Join(stateDir(), "starred_epics.json") }

func readStarFile() starFile {
	var f starFile
	if data, err := os.ReadFile(starStatePath()); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	return f
}

// starsFor returns the stars for one site: its own entry if the file has one,
// else the legacy top-level list (an old file is attributed to the current
// site; Jira returns nothing for keys it does not know, so a wrong guess just
// shows no rows).
func (f starFile) starsFor(cloudID string) []starEntry {
	if s, ok := f.Sites[cloudID]; ok {
		return s.Epics
	}
	out := make([]starEntry, 0, len(f.Epics))
	for _, e := range f.Epics {
		out = append(out, starEntry{Key: e.Key, Summary: e.Status.Summary})
	}
	return out
}

// readStars returns the current site's stars in star order, oldest first.
func readStars(cloudID string) []starEntry {
	return readStarFile().starsFor(cloudID)
}

// starredKeys returns the current site's starred keys in star order.
func starredKeys(cloudID string) []string {
	stars := readStars(cloudID)
	keys := make([]string, len(stars))
	for i, s := range stars {
		keys[i] = s.Key
	}
	return keys
}

// writeStars replaces this site's stars, keeping every other site's entry. The
// legacy top-level list is dropped: it has now been moved under this site.
// There is no lock; two popups starring at once is last-writer-wins.
func writeStars(cloudID string, stars []starEntry) error {
	f := readStarFile()
	if f.Sites == nil {
		f.Sites = map[string]siteStars{}
	}
	if stars == nil {
		stars = []starEntry{}
	}
	f.Sites[cloudID] = siteStars{Epics: stars}
	f.Epics = nil
	return writeStarFile(f)
}

// toggleStar stars the epic if it is not starred, else unstars it. It reports
// whether the epic is starred afterwards.
func toggleStar(cloudID string, epic ticketStatus) (starred bool, err error) {
	stars := readStars(cloudID)
	for i, s := range stars {
		if s.Key == epic.Key {
			return false, writeStars(cloudID, append(stars[:i:i], stars[i+1:]...))
		}
	}
	return true, writeStars(cloudID, append(stars, starEntry{Key: epic.Key, Summary: epic.Summary}))
}

// writeStarFile writes starred_epics.json atomically (temp file, then
// rename) — the same approach labelstate.go uses. This is a local pin
// list, not a secret, so it doesn't need the 0600 + flock treatment
// credentials.json gets.
func writeStarFile(f starFile) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(f)
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
