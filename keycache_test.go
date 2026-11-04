package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// setKeycacheEnv gives a test a temp state dir, a fixed clock and a cloud ID.
func setKeycacheEnv(t *testing.T, cloud string) *time.Time {
	t.Helper()
	withTempStateDir(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	origNow, origCloud := nowFn, cloudIDFn
	nowFn = func() time.Time { return now }
	cloudIDFn = func() string { return cloud }
	t.Cleanup(func() { nowFn, cloudIDFn = origNow, origCloud })
	return &now
}

func realKeys() map[string]ticketStatus {
	return map[string]ticketStatus{"PROJ-1": {Key: "PROJ-1", StatusName: "Open"}}
}

func TestLookupKeysFirstSightIsOneBatchedCallThenCached(t *testing.T) {
	setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{results: realKeys()}

	confirmed, statuses, err := lookupKeys(context.Background(), src, []string{"PR-4567", "PROJ-1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 1 || len(src.calls[0]) != 2 {
		t.Fatalf("calls = %v, want one batched call of 2 keys", src.calls)
	}
	if len(confirmed) != 1 || confirmed[0] != "PROJ-1" {
		t.Errorf("confirmed = %v, want [PROJ-1]", confirmed)
	}
	if statuses["PROJ-1"].StatusName != "Open" {
		t.Errorf("statuses = %v, want PROJ-1 fetched status", statuses)
	}

	confirmed, statuses, err = lookupKeys(context.Background(), src, []string{"PR-4567", "PROJ-1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 1 {
		t.Errorf("calls = %d, want no second call within the TTL", len(src.calls))
	}
	if len(confirmed) != 1 || confirmed[0] != "PROJ-1" || len(statuses) != 0 {
		t.Errorf("cached result = %v %v, want [PROJ-1] and no fetched statuses", confirmed, statuses)
	}
}

func TestLookupKeysMissingKeyNotReAskedWithinTTL(t *testing.T) {
	now := setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{results: map[string]ticketStatus{}}
	lookupKeys(context.Background(), src, []string{"PR-1"}, false)
	*now = now.Add(missingKeyTTL - time.Minute)
	lookupKeys(context.Background(), src, []string{"PR-1"}, false)
	if len(src.calls) != 1 {
		t.Fatalf("calls = %d, want 1 inside the missing TTL", len(src.calls))
	}
	*now = now.Add(2 * time.Minute)
	lookupKeys(context.Background(), src, []string{"PR-1"}, false)
	if len(src.calls) != 2 {
		t.Errorf("calls = %d, want a re-ask after the missing TTL", len(src.calls))
	}
}

func TestLookupKeysExpiredFoundEntryIsReAsked(t *testing.T) {
	now := setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{results: realKeys()}
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	*now = now.Add(foundKeyTTL + time.Minute)
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	if len(src.calls) != 2 {
		t.Errorf("calls = %d, want a re-ask after the found TTL", len(src.calls))
	}
}

func TestLookupKeysOfflineKeepsExpiredAnswer(t *testing.T) {
	now := setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{results: realKeys()}
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	*now = now.Add(foundKeyTTL + time.Minute)
	src.searchErr = errors.New("dial tcp: offline")
	confirmed, _, err := lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	if err != nil {
		t.Fatalf("err = %v, want the old answer kept without error", err)
	}
	if len(confirmed) != 1 || confirmed[0] != "PROJ-1" {
		t.Errorf("confirmed = %v, want the old answer [PROJ-1]", confirmed)
	}
}

func TestLookupKeysOfflineWithNoAnswerReturnsError(t *testing.T) {
	setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{searchErr: errors.New("dial tcp: offline")}
	if _, _, err := lookupKeys(context.Background(), src, []string{"PROJ-1"}, false); err == nil {
		t.Error("expected an error with no cached answer")
	}
}

func TestLookupKeysDifferentCloudIDDiscardsFile(t *testing.T) {
	setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{results: realKeys()}
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	cloudIDFn = func() string { return "cloud-2" }
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	if len(src.calls) != 2 {
		t.Errorf("calls = %d, want a fresh lookup for a different site", len(src.calls))
	}
	if got := readKeyCache("cloud-2"); got.CloudID != "cloud-2" || len(got.Keys) != 1 {
		t.Errorf("cache = %+v, want one entry for cloud-2", got)
	}
}

func TestLookupKeysSignedOutLeavesCacheUntouched(t *testing.T) {
	setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{searchErr: errSignedOut}
	_, _, err := lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	if !errors.Is(err, errSignedOut) {
		t.Fatalf("err = %v, want errSignedOut", err)
	}
	if _, statErr := os.Stat(keyCachePath()); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("keys.json written on a signed-out error: %v", statErr)
	}
}

func TestLookupKeysCorruptFileIsTreatedAsEmpty(t *testing.T) {
	setKeycacheEnv(t, "cloud-1")
	if err := os.WriteFile(keyCachePath(), []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &fakeJiraSource{results: realKeys()}
	confirmed, _, err := lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	if err != nil || len(confirmed) != 1 {
		t.Errorf("confirmed, err = %v, %v, want [PROJ-1], nil", confirmed, err)
	}
}

func TestLookupKeysForceBypassesCache(t *testing.T) {
	setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{results: realKeys()}
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, true)
	if len(src.calls) != 2 {
		t.Errorf("calls = %d, want force to re-ask", len(src.calls))
	}
}

func TestLookupKeysOnlyAsksAboutUnknownKeys(t *testing.T) {
	setKeycacheEnv(t, "cloud-1")
	src := &fakeJiraSource{results: realKeys()}
	lookupKeys(context.Background(), src, []string{"PROJ-1"}, false)
	lookupKeys(context.Background(), src, []string{"proj-1", "PR-2"}, false)
	if len(src.calls) != 2 || len(src.calls[1]) != 1 || src.calls[1][0] != "PR-2" {
		t.Errorf("calls = %v, want the second call to ask only about PR-2", src.calls)
	}
}

func TestWriteKeyCachePrunesOldEntries(t *testing.T) {
	now := setKeycacheEnv(t, "cloud-1")
	f := keyCacheFile{CloudID: "cloud-1", Keys: map[string]keyCacheEntry{
		"OLD-1": {Result: keyMissing, Recorded: now.Add(-25 * time.Hour)},
		"NEW-1": {Result: keyFound, Recorded: *now},
	}}
	if err := writeKeyCache(f, *now); err != nil {
		t.Fatal(err)
	}
	got := readKeyCache("cloud-1")
	if _, ok := got.Keys["OLD-1"]; ok || len(got.Keys) != 1 {
		t.Errorf("keys = %v, want only NEW-1", got.Keys)
	}
}
