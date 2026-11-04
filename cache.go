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

func writePickerCache(issues []ticketStatus) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(issues)
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
