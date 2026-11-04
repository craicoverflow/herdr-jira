package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithConfigDefaultsLeavesKeyPatternEmpty(t *testing.T) {
	if got := withConfigDefaults(config{}).KeyPattern; got != "" {
		t.Errorf("KeyPattern = %q, want empty (auto)", got)
	}
}

func TestWithConfigDefaultsKeepsSetKeyPattern(t *testing.T) {
	got := withConfigDefaults(config{KeyPattern: `(ACME-[0-9]+)`}).KeyPattern
	if got != `(ACME-[0-9]+)` {
		t.Errorf("KeyPattern = %q, want it kept as given", got)
	}
}

func TestLabelRefreshSecondsDefaults(t *testing.T) {
	for _, in := range []int{0, -5} {
		if got := withConfigDefaults(config{LabelRefreshSeconds: in}).LabelRefreshSeconds; got != 60 {
			t.Errorf("LabelRefreshSeconds(%d) = %d, want 60", in, got)
		}
	}
	if got := withConfigDefaults(config{LabelRefreshSeconds: 15}).LabelRefreshSeconds; got != 15 {
		t.Errorf("LabelRefreshSeconds = %d, want 15 kept", got)
	}
}

func TestLoadConfigMalformedFileReturnsDefaultsAndError(t *testing.T) {
	dir := withTempConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err == nil {
		t.Error("expected an error for a malformed config")
	}
	if cfg.KeyPattern != "" || cfg.LabelRefreshSeconds != 60 {
		t.Errorf("cfg = %+v, want defaults", cfg)
	}
}

func TestGenericKeyPatternHasOneCaptureGroup(t *testing.T) {
	if got := candidateKeys("x PROJ-1 y"); len(got) != 1 || got[0] != "PROJ-1" {
		t.Errorf("candidateKeys = %v", got)
	}
}
