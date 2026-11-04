package main

import "testing"

func TestStarStateAddPersistsAndPreservesOrder(t *testing.T) {
	withTempStateDir(t)
	st := readStarState()
	if !st.add(ticketStatus{Key: "TICKET-1", StatusName: "Open"}) {
		t.Fatal("add should report true for a new key")
	}
	if !st.add(ticketStatus{Key: "TICKET-2", StatusName: "In Progress"}) {
		t.Fatal("add should report true for a new key")
	}
	if err := writeStarState(st); err != nil {
		t.Fatalf("writeStarState: %v", err)
	}

	got := readStarState()
	keys := []string{}
	for _, e := range got.Epics {
		keys = append(keys, e.Key)
	}
	if len(keys) != 2 || keys[0] != "TICKET-1" || keys[1] != "TICKET-2" {
		t.Fatalf("keys after reload = %v, want [TICKET-1 TICKET-2] in that order", keys)
	}
}

func TestStarStateAddTwiceReportsNoChange(t *testing.T) {
	var st starState
	st.add(ticketStatus{Key: "TICKET-1"})
	if st.add(ticketStatus{Key: "TICKET-1"}) {
		t.Error("adding an already-starred key should report false")
	}
	if len(st.Epics) != 1 {
		t.Fatalf("len(Epics) = %d, want 1 (no duplicate)", len(st.Epics))
	}
}

func TestStarStateRemove(t *testing.T) {
	var st starState
	st.add(ticketStatus{Key: "TICKET-1"})
	st.add(ticketStatus{Key: "TICKET-2"})

	if !st.remove("TICKET-1") {
		t.Fatal("remove should report true for a starred key")
	}
	if st.remove("TICKET-1") {
		t.Error("removing an already-removed key should report false")
	}
	if len(st.Epics) != 1 || st.Epics[0].Key != "TICKET-2" {
		t.Fatalf("Epics after remove = %v, want only TICKET-2", st.Epics)
	}
}

func TestStarStateUpdateStatusIsNoopForUnstarredKey(t *testing.T) {
	var st starState
	st.add(ticketStatus{Key: "TICKET-1", StatusName: "Open"})
	st.updateStatus(ticketStatus{Key: "TICKET-9999", StatusName: "Done"})
	if len(st.Epics) != 1 {
		t.Fatalf("updateStatus for an unstarred key must not add it, got %v", st.Epics)
	}
}

func TestStarStateUpdateStatusRefreshesInPlace(t *testing.T) {
	var st starState
	st.add(ticketStatus{Key: "TICKET-1", StatusName: "Open"})
	st.updateStatus(ticketStatus{Key: "TICKET-1", StatusName: "Done"})
	if st.Epics[0].Status.StatusName != "Done" {
		t.Errorf("status after updateStatus = %q, want Done", st.Epics[0].Status.StatusName)
	}
}

func TestReadStarStateOnMissingFileIsEmpty(t *testing.T) {
	withTempStateDir(t)
	st := readStarState()
	if len(st.Epics) != 0 {
		t.Errorf("Epics on a missing state file = %v, want empty", st.Epics)
	}
}
