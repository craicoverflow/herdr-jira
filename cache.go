package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

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
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(all)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".picker_cache-*.json.tmp")
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
	return os.Rename(tmpPath, pickerCachePath())
}
