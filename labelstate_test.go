package main

import "testing"

func TestInvalidateLabelCacheRemovesEntry(t *testing.T) {
	withTempStateDir(t)
	st := readLabelState()
	st.Cache["PROJ-1"] = cacheEntry{Status: ticketStatus{Key: "PROJ-1", StatusName: "Open"}}
	if err := writeLabelState(st); err != nil {
		t.Fatalf("writeLabelState: %v", err)
	}

	invalidateLabelCache("PROJ-1")

	got := readLabelState()
	if _, ok := got.Cache["PROJ-1"]; ok {
		t.Error("expected PROJ-1 to be removed from the cache")
	}
}

func TestInvalidateLabelCacheOnMissingStateIsANoop(t *testing.T) {
	withTempStateDir(t)
	invalidateLabelCache("PROJ-1") // must not panic or error visibly
}
