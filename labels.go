package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

// tokenNames are every sidebar token this plugin owns; a space whose
// ticket went away has all of them cleared (reported as nil) rather than
// just left unset, so a stale value never lingers.
var tokenNames = []string{"ticket", "ticket_status", "ticket_badge"}

// tickCooldown makes a tick within a few seconds of the last one a no-op:
// herdr fires tick on many events in quick succession (pane focus, agent
// status changes), and there is nothing new to say that fast.
const tickCooldown = 3 * time.Second

// nowFn is a seam for tests.
var nowFn = time.Now

// ticketTokens is what a space/pane shows for one ticket.
func ticketTokens(s ticketStatus) map[string]string {
	return map[string]string{
		"ticket":        s.Key,
		"ticket_status": s.StatusName,
		"ticket_badge":  s.Key + " " + s.StatusName,
	}
}

// tokenParams turns tokens (nil meaning "clear everything") into
// report_metadata's map: every token name we own, with null for the ones
// not set, so a stale one is cleared rather than left behind.
func tokenParams(tokens map[string]string) map[string]any {
	out := map[string]any{}
	for _, n := range tokenNames {
		if v, ok := tokens[n]; ok {
			out[n] = v
		} else {
			out[n] = nil
		}
	}
	return out
}

func reportTokens(kind, id string, tokens map[string]string) error {
	params := map[string]any{"source": "herdr-jira", "tokens": tokenParams(tokens)}
	method := "workspace.report_metadata"
	if kind == "pane" {
		method, params["pane_id"] = "pane.report_metadata", id
	} else {
		params["workspace_id"] = id
	}
	return herdrCall(method, params, nil)
}

// isQuietTickErr says whether a tick failure is expected (no Jira
// sign-in, offline) and not worth failing the whole tick over: the last
// known tokens stay in place, and tick still exits 0.
func isQuietTickErr(err error) bool {
	return errors.Is(err, errSignedOut) || errors.Is(err, context.DeadlineExceeded)
}

// tick refreshes the sidebar tokens for every space: it resolves each
// space's branch key (branch.go), fetches every distinct key whose cached
// answer is older than cfg's refresh interval in one batch
// (jiraSource.search), and reports the result to the space and its agent
// panes — or clears the tokens when a space has no key.
//
// It's cheap when nothing is due: a tick within tickCooldown of the last
// one is a no-op unless force is set, and Jira is only asked about a key
// whose cached answer has gone stale. A Jira failure (signed out, offline)
// is logged once and falls back to the last known answer rather than
// failing the tick.
func tick(ctx context.Context, cfg config, force bool, src jiraSource) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(stateDir(), "tick.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !force {
			return nil // another tick is already running; nothing new to add
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			return err
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	st := readLabelState()
	now := nowFn()
	if !force && now.Sub(st.LastTick) < tickCooldown {
		return nil
	}
	st.LastTick = now

	workspaces, err := listWorkspaces()
	if err != nil {
		return err
	}
	panes, err := listPanes("")
	if err != nil {
		return err
	}

	spaceKeys := map[string]string{} // workspace id -> ticket key
	keySet := map[string]bool{}
	for _, w := range workspaces {
		if w.Worktree == nil || w.Worktree.CheckoutPath == "" {
			continue
		}
		key, ok := resolveTicketKey(w.Worktree.CheckoutPath, cfg)
		if !ok {
			continue
		}
		spaceKeys[w.WorkspaceID] = key
		keySet[key] = true
	}

	fresh := time.Duration(cfg.LabelRefreshSeconds) * time.Second
	var due []string
	for key := range keySet {
		e, ok := st.Cache[key]
		if force || !ok || now.Sub(e.Fetched) >= fresh {
			due = append(due, key)
		}
	}
	sort.Strings(due) // deterministic batching

	var searchErr error
	if len(due) > 0 {
		found, err := src.search(ctx, due)
		if err != nil {
			searchErr = err
			if !isQuietTickErr(err) {
				return err
			}
			fmt.Fprintln(os.Stderr, "herdr-jira: tick: ignoring:", err)
			// Keep showing what we knew; don't touch Fetched, so the next
			// tick tries Jira again instead of waiting out the TTL.
		} else {
			for _, key := range due {
				if s, ok := found[key]; ok {
					st.Cache[key] = cacheEntry{Fetched: now, Status: s}
				} else {
					delete(st.Cache, key) // Jira answered and this key doesn't exist
				}
			}
		}
	}

	panesByWorkspace := map[string][]paneInfo{}
	for _, p := range panes {
		panesByWorkspace[p.WorkspaceID] = append(panesByWorkspace[p.WorkspaceID], p)
	}

	for _, w := range workspaces {
		var tokens map[string]string
		if key, ok := spaceKeys[w.WorkspaceID]; ok {
			if e, ok := st.Cache[key]; ok {
				tokens = ticketTokens(e.Status)
			}
		}
		if err := reportTokens("workspace", w.WorkspaceID, tokens); err != nil {
			return err
		}
		for _, p := range panesByWorkspace[w.WorkspaceID] {
			if p.Agent == "" {
				continue // only agent panes are labelled; a shell keeps its own
			}
			if err := reportTokens("pane", p.PaneID, tokens); err != nil {
				return err
			}
		}
	}

	// Forget cached keys nobody's space is on any more.
	for key := range st.Cache {
		if !keySet[key] {
			delete(st.Cache, key)
		}
	}
	if err := writeLabelState(st); err != nil {
		return err
	}
	return searchErr
}
