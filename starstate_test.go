package main

import (
	"encoding/json"
	"os"
	"testing"
)

func keysOf(stars []starEntry) []string {
	out := []string{}
	for _, s := range stars {
		out = append(out, s.Key)
	}
	return out
}

func TestToggleStarPersistsAndPreservesOrder(t *testing.T) {
	withTempStateDir(t)
	for _, k := range []string{"PROJ-1", "PROJ-2"} {
		starred, err := toggleStar("cloud-a", ticketStatus{Key: k, Summary: "s " + k})
		if err != nil || !starred {
			t.Fatalf("toggleStar(%s) = (%v, %v), want (true, nil)", k, starred, err)
		}
	}
	got := keysOf(readStars("cloud-a"))
	if len(got) != 2 || got[0] != "PROJ-1" || got[1] != "PROJ-2" {
		t.Fatalf("keys after reload = %v, want [PROJ-1 PROJ-2] in that order", got)
	}
}

func TestToggleStarTwiceUnstars(t *testing.T) {
	withTempStateDir(t)
	toggleStar("c", ticketStatus{Key: "PROJ-1"})
	toggleStar("c", ticketStatus{Key: "PROJ-2"})
	starred, err := toggleStar("c", ticketStatus{Key: "PROJ-1"})
	if err != nil || starred {
		t.Fatalf("second toggle = (%v, %v), want (false, nil)", starred, err)
	}
	if got := keysOf(readStars("c")); len(got) != 1 || got[0] != "PROJ-2" {
		t.Fatalf("keys = %v, want [PROJ-2]", got)
	}
}

func TestReadStarsOnMissingFileIsEmpty(t *testing.T) {
	withTempStateDir(t)
	if got := readStars("c"); len(got) != 0 {
		t.Errorf("stars on a missing file = %v, want empty", got)
	}
}

const oldShapeStars = `{"epics":[{"key":"OLD-1","status":{"Key":"OLD-1","Summary":"Old one","StatusName":"Open"}},{"key":"OLD-2","status":{"Key":"OLD-2"}}]}`

func TestOldShapeFileLoadsAndMigratesOnWrite(t *testing.T) {
	withTempStateDir(t)
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(starStatePath(), []byte(oldShapeStars), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readStars("cloud-a")
	if len(got) != 2 || got[0].Key != "OLD-1" || got[0].Summary != "Old one" {
		t.Fatalf("old-shape stars = %+v, want OLD-1 (with summary) and OLD-2", got)
	}

	if _, err := toggleStar("cloud-a", ticketStatus{Key: "NEW-1"}); err != nil {
		t.Fatal(err)
	}
	f := readStarFile()
	if len(f.Epics) != 0 {
		t.Errorf("legacy top-level list must be removed after the first write, got %v", f.Epics)
	}
	if keys := keysOf(f.Sites["cloud-a"].Epics); len(keys) != 3 || keys[0] != "OLD-1" || keys[2] != "NEW-1" {
		t.Errorf("migrated keys = %v, want OLD-1, OLD-2, NEW-1", keys)
	}
}

func TestSitesEntryWinsOverLegacyList(t *testing.T) {
	withTempStateDir(t)
	os.MkdirAll(stateDir(), 0o700)
	os.WriteFile(starStatePath(), []byte(`{"epics":[{"key":"OLD-1"}],"sites":{"a":{"epics":[{"key":"NEW-1"}]}}}`), 0o600)
	if got := keysOf(readStars("a")); len(got) != 1 || got[0] != "NEW-1" {
		t.Fatalf("keys = %v, want [NEW-1]", got)
	}
	toggleStar("a", ticketStatus{Key: "NEW-2"})
	if len(readStarFile().Epics) != 0 {
		t.Error("legacy list must be dropped on the next write")
	}
}

func TestStarsAreScopedToTheirSite(t *testing.T) {
	withTempStateDir(t)
	toggleStar("site-a", ticketStatus{Key: "A-1"})
	if got := readStars("site-b"); len(got) != 0 {
		t.Errorf("site B sees site A's stars: %v", got)
	}
	toggleStar("site-b", ticketStatus{Key: "B-1"})
	if got := keysOf(readStars("site-a")); len(got) != 1 || got[0] != "A-1" {
		t.Errorf("site A's stars were disturbed by a write for site B: %v", got)
	}
}

func TestStarFileKeepsDoneStarredEpic(t *testing.T) {
	// Starring is by key only; a Done epic is hidden at display time, never
	// removed from the file.
	withTempStateDir(t)
	toggleStar("c", ticketStatus{Key: "PROJ-1", StatusCategory: "done"})
	if got := keysOf(readStars("c")); len(got) != 1 {
		t.Fatalf("a Done starred epic must stay in the file, got %v", got)
	}
}

func TestNewShapeFileDecodesIntoOldStructWithNoStars(t *testing.T) {
	withTempStateDir(t)
	toggleStar("c", ticketStatus{Key: "PROJ-1"})
	data, err := os.ReadFile(starStatePath())
	if err != nil {
		t.Fatal(err)
	}
	// The previous binary's struct, verbatim.
	var old struct {
		Epics []struct {
			Key    string       `json:"key"`
			Status ticketStatus `json:"status"`
		} `json:"epics"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatalf("old binary must not fail on the new shape: %v", err)
	}
	if len(old.Epics) != 0 {
		t.Errorf("old binary should see no stars, got %v", old.Epics)
	}
}
