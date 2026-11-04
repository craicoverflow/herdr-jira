package main

import (
	"context"
	"testing"
	"time"
)

func withFakeClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	orig := nowFn
	nowFn = func() time.Time { return now }
	t.Cleanup(func() { nowFn = orig })
	return &now
}

func TestTickCooldownSkipsSearchWithinAFewSeconds(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	dir := gitRepo(t, "PROJ-1-a")
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", dir)}
	now := withFakeClock(t, time.Now())

	src := &fakeJiraSource{results: map[string]ticketStatus{"PROJ-1": {Key: "PROJ-1", StatusName: "Open"}}}
	cfg := withConfigDefaults(config{})

	if err := tick(context.Background(), cfg, false, src); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(src.calls) != 1 {
		t.Fatalf("after first tick: %d search calls, want 1", len(src.calls))
	}

	*now = now.Add(1 * time.Second) // well within tickCooldown
	if err := tick(context.Background(), cfg, false, src); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(src.calls) != 1 {
		t.Errorf("after a tick within the cooldown: %d search calls, want still 1 (no-op)", len(src.calls))
	}
	if len(herdr.reports) != 1 {
		t.Errorf("after a tick within the cooldown: %d report calls, want still 1 (no-op)", len(herdr.reports))
	}
}

func TestTickForceBypassesCooldown(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	dir := gitRepo(t, "PROJ-1-a")
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", dir)}
	now := withFakeClock(t, time.Now())

	src := &fakeJiraSource{results: map[string]ticketStatus{"PROJ-1": {Key: "PROJ-1", StatusName: "Open"}}}
	cfg := withConfigDefaults(config{})

	if err := tick(context.Background(), cfg, false, src); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	*now = now.Add(1 * time.Second)
	if err := tick(context.Background(), cfg, true, src); err != nil {
		t.Fatalf("forced tick: %v", err)
	}
	// Forced bypasses the cooldown; the cache is still fresh (TTL not up),
	// so no *additional* search call is expected, but the report must
	// still have been sent again (not skipped as a no-op).
	if len(herdr.reports) != 2 {
		t.Errorf("after a forced tick within the cooldown: %d report calls, want 2 (not a no-op)", len(herdr.reports))
	}
}

func TestTickOnlyAsksJiraForStaleCacheEntries(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	dir := gitRepo(t, "PROJ-1-a")
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", dir)}
	now := withFakeClock(t, time.Now())

	src := &fakeJiraSource{results: map[string]ticketStatus{"PROJ-1": {Key: "PROJ-1", StatusName: "Open"}}}
	cfg := withConfigDefaults(config{})
	cfg.LabelRefreshSeconds = 60

	if err := tick(context.Background(), cfg, false, src); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(src.calls) != 1 {
		t.Fatalf("after first tick: %d search calls, want 1", len(src.calls))
	}

	// Past the cooldown but well within the 60s TTL: no new search call.
	*now = now.Add(10 * time.Second)
	if err := tick(context.Background(), cfg, false, src); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(src.calls) != 1 {
		t.Errorf("after a tick within the TTL: %d search calls, want still 1", len(src.calls))
	}

	// Past the TTL: the cache entry is due again.
	*now = now.Add(60 * time.Second)
	if err := tick(context.Background(), cfg, false, src); err != nil {
		t.Fatalf("third tick: %v", err)
	}
	if len(src.calls) != 2 {
		t.Errorf("after the TTL elapsed: %d search calls, want 2", len(src.calls))
	}
}

func TestTickFallsBackToCacheOnSignedOutAndExitsCleanly(t *testing.T) {
	withTempStateDir(t)
	herdr := withFakeHerdr(t)
	dir := gitRepo(t, "PROJ-1-a")
	herdr.workspaces = []workspaceInfo{workspaceAt("ws-1", dir)}
	now := withFakeClock(t, time.Now())

	src := &fakeJiraSource{results: map[string]ticketStatus{"PROJ-1": {Key: "PROJ-1", StatusName: "Open"}}}
	cfg := withConfigDefaults(config{})

	if err := tick(context.Background(), cfg, false, src); err != nil {
		t.Fatalf("first tick: %v", err)
	}

	// Now Jira is unreachable (signed out) and the TTL has elapsed.
	src.searchErr = errSignedOut
	*now = now.Add(time.Duration(cfg.LabelRefreshSeconds+1) * time.Second)
	err := tick(context.Background(), cfg, false, src)
	if err != errSignedOut {
		t.Fatalf("tick returned %v, want errSignedOut (caller decides this is quiet)", err)
	}
	if !isQuietTickErr(err) {
		t.Fatal("errSignedOut must be a quiet tick error, so the command still exits 0")
	}

	// The last known tokens must still have been reported, not cleared.
	last := herdr.reports[len(herdr.reports)-1].params["tokens"].(map[string]any)
	if last["ticket"] != "PROJ-1" {
		t.Errorf("ticket token after a signed-out failure = %v, want the last known value PROJ-1", last["ticket"])
	}
}
