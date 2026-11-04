package main

import "testing"

func TestInvalidateLabelCacheRemovesEntry(t *testing.T) {
	withTempStateDir(t)
	st := readLabelState()
	st.Cache["TICKET-1"] = cacheEntry{Status: ticketStatus{Key: "TICKET-1", StatusName: "Open"}}
	if err := writeLabelState(st); err != nil {
		t.Fatalf("writeLabelState: %v", err)
	}

	invalidateLabelCache("TICKET-1")

	got := readLabelState()
	if _, ok := got.Cache["TICKET-1"]; ok {
		t.Error("expected TICKET-1 to be removed from the cache")
	}
}

func TestInvalidateLabelCacheOnMissingStateIsANoop(t *testing.T) {
	withTempStateDir(t)
	invalidateLabelCache("TICKET-1") // must not panic or error visibly
}
