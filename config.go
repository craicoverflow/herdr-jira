package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// genericKeyPattern finds candidate Jira keys in a branch name. It is
// deliberately loose: its matches are only candidates until Jira confirms
// them (keycache.go), because it also matches PR numbers and step counters
// such as "pr-4567". It is case-insensitive and matches anywhere in the
// name, since keys appear as a bare prefix, with a trailing suffix, or
// nested after a path segment like "alice/PROJ-2416-...".
const genericKeyPattern = `(?i)\b([A-Z][A-Z0-9]+-[0-9]+)\b`

// config is herdr-jira's config.json.
type config struct {
	// KeyPattern is an optional regex used to find a Jira key in a branch
	// name. Empty means "auto": candidates come from genericKeyPattern and
	// Jira confirms them. A non-empty value is used as given, with no Jira
	// check. It must have no capture groups, or exactly one capturing the
	// key itself.
	KeyPattern string `json:"key_pattern"`
	// LabelRefreshSeconds is how old a ticket's cached status may get
	// before a tick asks Jira about it again.
	LabelRefreshSeconds int `json:"label_refresh_seconds"`
}

func withConfigDefaults(cfg config) config {
	if cfg.LabelRefreshSeconds <= 0 {
		cfg.LabelRefreshSeconds = 60
	}
	return cfg
}

// loadConfig reads config.json, returning defaults for anything unset (or
// if the file doesn't exist). A malformed file still yields defaults, so
// tick can keep working quietly while the user fixes it.
func loadConfig() (config, error) {
	data, err := os.ReadFile(filepath.Join(configDir(), "config.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return withConfigDefaults(config{}), nil
		}
		return withConfigDefaults(config{}), err
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return withConfigDefaults(config{}), errors.New("config.json: " + err.Error())
	}
	return withConfigDefaults(cfg), nil
}

func pluginID() string {
	if id := os.Getenv("HERDR_PLUGIN_ID"); id != "" {
		return id
	}
	return "herdr-jira"
}

func xdgDir(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

func configDir() string {
	if d := os.Getenv("HERDR_PLUGIN_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "herdr", "plugins", "config", pluginID())
}

func stateDir() string {
	if d := os.Getenv("HERDR_PLUGIN_STATE_DIR"); d != "" {
		return d
	}
	return filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"), "herdr", "plugins", pluginID())
}
