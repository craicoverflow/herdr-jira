package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// searchDebounce is how long the search box waits after the last keystroke
// before querying Jira, so fast typing doesn't fire a request per rune.
const searchDebounce = 300 * time.Millisecond

// branchStatusMessageLifetime overrides bubbles/list's 1s default so the
// [b]/[c] shortcuts' confirmation ("Copied X", "Created and switched to
// X, …") stays readable instead of flashing by.
const branchStatusMessageLifetime = 4 * time.Second

// issueItem adapts ticketStatus to bubbles/list.Item.
type issueItem struct{ ticketStatus }

func (i issueItem) Title() string       { return i.Summary }
func (i issueItem) Description() string { return i.Key + "  " + i.StatusName }
func (i issueItem) FilterValue() string { return i.Key + " " + i.Summary }

// yankSelectedKey copies the selected issue's key to the clipboard (e.g. for
// pasting into a new branch name) and surfaces the result in the list's own
// status message.
func yankSelectedKey(l *list.Model) tea.Cmd {
	item, ok := l.SelectedItem().(issueItem)
	if !ok {
		return nil
	}
	if err := clipboard.WriteAll(item.Key); err != nil {
		return l.NewStatusMessage(fmt.Sprintf("Couldn't copy %s: %v", item.Key, err))
	}
	return l.NewStatusMessage("Copied " + item.Key)
}

// jiraBrowseURL builds an issue's browser link from the signed-in site's
// URL, resolved via the OAuth accessible-resources call during sign-in and
// cached in credentials — so this works against whatever Jira site the user
// authenticated against, with no base URL to configure.
func jiraBrowseURL(key string) (string, error) {
	creds, err := loadCredentials()
	if err != nil {
		return "", err
	}
	if creds.SiteURL == "" {
		return "", errors.New("not signed in yet")
	}
	return creds.SiteURL + "/browse/" + key, nil
}

// openSelectedInBrowser opens the selected issue's Jira page in the default
// browser, surfacing failures through the list's own status message the
// same way yankSelectedKey does.
func openSelectedInBrowser(l *list.Model) tea.Cmd {
	item, ok := l.SelectedItem().(issueItem)
	if !ok {
		return nil
	}
	u, err := jiraBrowseURL(item.Key)
	if err != nil {
		return l.NewStatusMessage(fmt.Sprintf("Couldn't open %s: %v", item.Key, err))
	}
	if err := openBrowser(u); err != nil {
		return l.NewStatusMessage(fmt.Sprintf("Couldn't open %s: %v", item.Key, err))
	}
	return l.NewStatusMessage("Opened " + item.Key)
}

// transitionItem adapts transitionOption to bubbles/list.Item.
type transitionItem struct{ transitionOption }

func (i transitionItem) Title() string       { return i.Name }
func (i transitionItem) Description() string { return "→ " + i.ToStatusName }
func (i transitionItem) FilterValue() string { return i.Name }

type pickerPhase int

const (
	phaseLoading pickerPhase = iota
	phaseReady
	phaseDetailLoading
	phaseDetail
	phaseTransitionsLoading
	phaseTransitions
	phaseApplyingTransition
	phaseError
	phaseSearch
	phaseEpicsLoading
	phaseEpics
	phaseEpicChildrenLoading
	phaseEpicChildren
	phaseEpicSearch
	phaseAssigning

	// The [b] "new branch" wizard: prep (fetch assignee/transitions) ->
	// suffix entry -> create the branch -> up to two yes/no follow-ups
	// (assign to me, transition to In Progress) -> apply them.
	phaseBranchPrep
	phaseBranchSuffix
	phaseBranchCreating
	phaseBranchConfirm
	phaseBranchApplying
)

// branchConfirmKind picks which of the two post-branch questions
// phaseBranchConfirm is currently asking.
type branchConfirmKind int

const (
	branchConfirmAssign branchConfirmKind = iota
	branchConfirmTransition
)

// pickerModel is the popup's bubbletea model.
type pickerModel struct {
	src  jiraSource
	list list.Model

	phase          pickerPhase
	err            error
	errReturnPhase pickerPhase // where [r]/[esc] goes after an error

	detail            *issueDetail
	detailReturnPhase pickerPhase // where [esc]/[b] from the detail screen goes back to
	transitionList    list.Model

	// Epics tab: myEpics feeds epicList; selecting one loads its children
	// (via parent = <epicKey>, confirmed live)
	// into epicChildrenList. [esc] from a child's detail goes back to
	// phaseEpicChildren via detailReturnPhase, the same mechanism the
	// search feature uses.
	epicList         list.Model
	epicChildrenList list.Model

	// Starred epics: a second, persisted list alongside epicList for team
	// epics you don't own but contribute to. epicsOnStarred tracks which of
	// the two stacked lists [tab] currently routes keys to; [s] stars (from
	// epicSearchList) or unstars (from starredEpicList) — never from
	// epicList, since an owned epic is already "pinned" by ownership.
	starredEpicList list.Model
	epicsOnStarred  bool

	// Epic search ([ctrl+f] from phaseEpics): the same debounced live-query
	// shape as searchInput/searchList/searchSeq below, kept entirely
	// separate so a stale epic-search response can never land in the issue
	// search list (or vice versa).
	epicSearchInput  textinput.Model
	epicSearchList   list.Model
	epicSearchSeq    int
	epicSearching    bool
	epicSearchErr    error
	epicSearchOnList bool // true once [tab] has moved focus to the results list

	// Search mode: a textinput drives a debounced, server-side query (any
	// issue, not just yours — unlike list's built-in local filter). searchSeq
	// tags each keystroke's debounce/query pair so a stale response (an
	// earlier query that answers after a newer one) is dropped rather than
	// clobbering fresher results.
	searchInput textinput.Model
	searchList  list.Model
	searchSeq   int
	searching   bool
	searchErr   error

	// initialKey and noKeyBranch drive the "issue" action: open straight
	// on this agent's ticket (branch-resolved before the popup started),
	// or note that the branch had none.
	initialKeys []string
	noKeyBranch string
	notice      string // shown above the list, e.g. "no ticket in branch X"

	// cwd is the invoking space's checkout path (HERDR_JIRA_CWD), resolved
	// by the action before the popup started — the same env-plumbing
	// pattern initialKey/noKeyBranch use. Empty if unknown (e.g. opened
	// from a context herdr couldn't resolve a cwd for).
	cwd string

	// The [b] "new branch" wizard's state. branchReturnPhase is which list
	// (and which selected issue, via branchKey) started it, so [esc]/done
	// lands back in the right place with the right list's status message.
	branchKey               string
	branchReturnPhase       pickerPhase
	branchSuffixInput       textinput.Model
	branchName              string
	branchPendingAssign     bool // still need to ask "assign to you?"
	branchPendingTransition bool // still need to ask "transition to In Progress?"
	branchWantAssign        bool
	branchWantTransition    bool
	branchTransitionID      string
	branchConfirmKind       branchConfirmKind

	quitting bool
}

// Messages produced by this model's commands.
type (
	issuesLoadedMsg struct {
		issues []ticketStatus
		err    error
	}
	signedInMsg      struct{ err error }
	initialDetailMsg struct {
		detail issueDetail
		key    string // set on failure, for the fallback notice
		err    error
	}
	initialNoKeyMsg struct{ branch string }
	detailLoadedMsg struct {
		detail issueDetail
		err    error
	}
	transitionsLoadedMsg struct {
		transitions []transitionOption
		err         error
	}
	transitionAppliedMsg struct {
		detail issueDetail
		err    error
	}
	// debounceFireMsg fires searchDebounce after a keystroke; seq must still
	// match m.searchSeq (no newer keystroke since) for the query to run.
	debounceFireMsg struct {
		seq   int
		query string
	}
	// searchResultsMsg is deliberately distinct from issuesLoadedMsg: its
	// handler must never call writePickerCache, which is reserved for "your
	// issues." seq is checked the same way as debounceFireMsg's.
	searchResultsMsg struct {
		seq    int
		issues []ticketStatus
		err    error
	}
	// epicsLoadedMsg and epicChildrenLoadedMsg are deliberately distinct
	// from issuesLoadedMsg, for the same reason searchResultsMsg is: neither
	// handler writes the "your issues" picker cache.
	epicsLoadedMsg struct {
		epics []ticketStatus
		err   error
	}
	epicChildrenLoadedMsg struct {
		epicKey string
		issues  []ticketStatus
		err     error
	}
	// epicSearchDebounceFireMsg/epicSearchResultsMsg mirror
	// debounceFireMsg/searchResultsMsg exactly, but for the epics tab's own
	// search box — a distinct type so a response can never be misrouted
	// into the issue search list.
	epicSearchDebounceFireMsg struct {
		seq   int
		query string
	}
	epicSearchResultsMsg struct {
		seq    int
		issues []ticketStatus
		err    error
	}
	// starredEpicsRefreshedMsg answers a background re-fetch (via the
	// batched search(keys) call) of every starred epic's status, fired
	// after the epics tab loads. A failure here is best-effort — cached
	// statuses keep showing rather than interrupting with phaseError.
	starredEpicsRefreshedMsg struct {
		statuses map[string]ticketStatus
		err      error
	}
	assignAppliedMsg struct {
		detail issueDetail
		err    error
	}
	// branchPrepLoadedMsg answers the [b] wizard's prep fetch: the issue's
	// current assignee/status (to decide which follow-up questions are
	// worth asking) plus its live transitions (to find one that lands on
	// "In Progress", since workflow transitions are board-specific).
	branchPrepLoadedMsg struct {
		detail      issueDetail
		transitions []transitionOption
		accountID   string
		err         error
	}
	branchCreatedMsg struct {
		name string
		err  error
	}
	branchFollowupMsg struct {
		err error
	}
)

func newPickerModel(src jiraSource) pickerModel {
	return newPickerModelFor(src, nil, "")
}

// newPickerModelFor backs the "issue" action: initialKeys (candidates from
// the branch, in branch order) are confirmed with Jira and the first real
// one opens straight on that ticket's detail view; noKeyBranch (if set, and
// none is confirmed) notes that the current branch had no usable key.
func newPickerModelFor(src jiraSource, initialKeys []string, noKeyBranch string) pickerModel {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Your Jira issues"
	l.SetShowHelp(true)
	l.StatusMessageLifetime = branchStatusMessageLifetime
	cached := readPickerCache()
	phase := phaseLoading
	if len(cached) > 0 {
		setListIssues(&l, cached)
		phase = phaseReady
	}
	tl := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	tl.Title = "Transition to…"

	si := textinput.New()
	si.Placeholder = "key (e.g. PROJ-1234) or text…"
	sl := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	sl.Title = "Search all issues"
	sl.SetShowHelp(true)

	el := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	el.Title = "Your epics"
	// The two lists below are stacked in a tight vertical split (phaseEpics
	// View()), so their own built-in help/status bars would waste scarce
	// lines duplicating what the shared footer already says.
	el.SetShowHelp(false)
	el.SetShowStatusBar(false)
	el.StatusMessageLifetime = branchStatusMessageLifetime
	ecl := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	ecl.SetShowHelp(true)
	ecl.StatusMessageLifetime = branchStatusMessageLifetime

	sel := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	sel.Title = "Starred epics"
	sel.SetShowHelp(false)
	sel.SetShowStatusBar(false)
	sel.StatusMessageLifetime = branchStatusMessageLifetime

	esi := textinput.New()
	esi.Placeholder = "epic key (e.g. PROJ-1234) or text…"
	esl := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	esl.Title = "Search epics"
	esl.SetShowHelp(true)
	esl.StatusMessageLifetime = branchStatusMessageLifetime

	return pickerModel{
		src: src, list: l, transitionList: tl, phase: phase,
		detailReturnPhase: phaseReady,
		searchInput:       si,
		searchList:        sl,
		epicList:          el,
		epicChildrenList:  ecl,
		starredEpicList:   sel,
		epicSearchInput:   esi,
		epicSearchList:    esl,
		initialKeys:       initialKeys, noKeyBranch: noKeyBranch,
	}
}

func setListIssues(l *list.Model, issues []ticketStatus) {
	items := make([]list.Item, len(issues))
	for i, s := range issues {
		items[i] = issueItem{s}
	}
	l.SetItems(items)
}

func setTransitionItems(l *list.Model, transitions []transitionOption) {
	items := make([]list.Item, len(transitions))
	for i, t := range transitions {
		items[i] = transitionItem{t}
	}
	l.SetItems(items)
}

func (m pickerModel) Init() tea.Cmd {
	return checkSignInThenStart(m.src, m.initialKeys, m.noKeyBranch)
}

// checkSignInThenStart signs in first if there are no credentials, then
// either loads this agent's ticket directly (the first of initialKeys that
// Jira confirms) or the issue list, noting a keyless branch (noKeyBranch)
// if that's why there's no confirmed key. The popup drives sign-in itself on first use; there is no
// separate login step for the user.
func checkSignInThenStart(src jiraSource, initialKeys []string, noKeyBranch string) tea.Cmd {
	return func() tea.Msg {
		if _, err := loadCredentials(); err != nil {
			if err := signIn(context.Background()); err != nil {
				return signedInMsg{err: err}
			}
		}
		if len(initialKeys) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), initialLookupTimeout)
			confirmed, _, err := lookupKeys(ctx, src, initialKeys, false)
			cancel()
			if err == nil && len(confirmed) > 0 {
				detail, err := src.get(context.Background(), confirmed[0])
				if err != nil {
					return initialDetailMsg{key: confirmed[0], err: err}
				}
				return initialDetailMsg{detail: detail}
			}
		}
		return initialNoKeyMsg{branch: noKeyBranch}
	}
}

// initialLookupTimeout bounds the picker's branch-key confirmation, so a
// slow Jira falls back to the list instead of a blank popup.
const initialLookupTimeout = 5 * time.Second

// checkSignInThenLoad signs in first if there are no credentials, then
// loads the issue list. The popup drives sign-in itself on first use;
// there is no separate login step for the user.
func checkSignInThenLoad(src jiraSource) tea.Cmd {
	return func() tea.Msg {
		if _, err := loadCredentials(); err != nil {
			if err := signIn(context.Background()); err != nil {
				return signedInMsg{err: err}
			}
		}
		issues, err := src.myIssues(context.Background())
		return issuesLoadedMsg{issues: issues, err: err}
	}
}

func loadIssues(src jiraSource) tea.Cmd {
	return func() tea.Msg {
		issues, err := src.myIssues(context.Background())
		return issuesLoadedMsg{issues: issues, err: err}
	}
}

func loadDetail(src jiraSource, key string) tea.Cmd {
	return func() tea.Msg {
		detail, err := src.get(context.Background(), key)
		return detailLoadedMsg{detail: detail, err: err}
	}
}

func loadTransitions(src jiraSource, key string) tea.Cmd {
	return func() tea.Msg {
		transitions, err := src.transitions(context.Background(), key)
		return transitionsLoadedMsg{transitions: transitions, err: err}
	}
}

func applyTransition(src jiraSource, key, transitionID string) tea.Cmd {
	return func() tea.Msg {
		if err := src.transition(context.Background(), key, transitionID); err != nil {
			return transitionAppliedMsg{err: err}
		}
		// Invalidate tick's label cache for this key so the sidebar picks
		// up the new status on the next tick instead of waiting out the TTL.
		invalidateLabelCache(key)
		detail, err := src.get(context.Background(), key)
		return transitionAppliedMsg{detail: detail, err: err}
	}
}

// debounceKeystroke waits searchDebounce before asking whether seq is still
// current. query travels with it so the fired command doesn't need to read
// back a (possibly already-changed) textinput value.
func debounceKeystroke(seq int, query string) tea.Cmd {
	return tea.Tick(searchDebounce, func(time.Time) tea.Msg {
		return debounceFireMsg{seq: seq, query: query}
	})
}

func runSearchText(src jiraSource, query string, seq int) tea.Cmd {
	return func() tea.Msg {
		issues, err := src.searchText(context.Background(), query)
		return searchResultsMsg{seq: seq, issues: issues, err: err}
	}
}

func loadEpics(src jiraSource) tea.Cmd {
	return func() tea.Msg {
		epics, err := src.myEpics(context.Background())
		return epicsLoadedMsg{epics: epics, err: err}
	}
}

// refreshStarredEpics re-fetches every starred epic's status in one batched
// search(keys) call, the same call the sidebar tick uses — so a starred
// epic's status line stays roughly current without a dedicated epic-status
// endpoint. nil if there's nothing starred yet.
func refreshStarredEpics(src jiraSource, keys []string) tea.Cmd {
	if len(keys) == 0 {
		return nil
	}
	return func() tea.Msg {
		statuses, err := src.search(context.Background(), keys)
		return starredEpicsRefreshedMsg{statuses: statuses, err: err}
	}
}

func debounceEpicSearchKeystroke(seq int, query string) tea.Cmd {
	return tea.Tick(searchDebounce, func(time.Time) tea.Msg {
		return epicSearchDebounceFireMsg{seq: seq, query: query}
	})
}

func runSearchEpics(src jiraSource, query string, seq int) tea.Cmd {
	return func() tea.Msg {
		epics, err := src.searchEpics(context.Background(), query)
		return epicSearchResultsMsg{seq: seq, issues: epics, err: err}
	}
}

// focusedEpicList is whichever of epicList/starredEpicList [tab] last
// selected — the one phaseEpics routes keys and list.Update to.
func (m *pickerModel) focusedEpicList() *list.Model {
	if m.epicsOnStarred {
		return &m.starredEpicList
	}
	return &m.epicList
}

// unstarEpic removes key from the persisted starred set and refreshes
// starredEpicList to match.
func unstarEpic(m pickerModel, key string) (pickerModel, tea.Cmd) {
	st := readStarState()
	if !st.remove(key) {
		return m, m.starredEpicList.NewStatusMessage("Not starred: " + key)
	}
	if err := writeStarState(st); err != nil {
		return m, m.starredEpicList.NewStatusMessage(fmt.Sprintf("Couldn't save: %v", err))
	}
	// ResetFilter first: SetItems' own filteredItems-recompute cmd would
	// otherwise be discarded by setListIssues, leaving a filtered list
	// showing stale (possibly now-removed) items — see epicsLoadedMsg's
	// identical fix for the same bubbles/list quirk.
	m.starredEpicList.ResetFilter()
	setListIssues(&m.starredEpicList, st.statuses())
	return m, m.starredEpicList.NewStatusMessage("Unstarred " + key)
}

// toggleStarEpic stars or unstars status.Key from the epic search list.
// Starring your own epic (already in epicList) is a no-op with a status
// message, since it's already pinned by ownership.
func toggleStarEpic(m pickerModel, status ticketStatus) (pickerModel, tea.Cmd) {
	for _, item := range m.epicList.Items() {
		if issue, ok := item.(issueItem); ok && issue.Key == status.Key {
			return m, m.epicSearchList.NewStatusMessage(status.Key + " is already yours")
		}
	}
	st := readStarState()
	var msg string
	if st.remove(status.Key) {
		msg = "Unstarred " + status.Key
	} else {
		st.add(status)
		msg = "Starred " + status.Key
	}
	if err := writeStarState(st); err != nil {
		return m, m.epicSearchList.NewStatusMessage(fmt.Sprintf("Couldn't save: %v", err))
	}
	m.starredEpicList.ResetFilter()
	setListIssues(&m.starredEpicList, st.statuses())
	return m, m.epicSearchList.NewStatusMessage(msg)
}

func loadEpicChildren(src jiraSource, epicKey string) tea.Cmd {
	return func() tea.Msg {
		issues, err := src.epicChildren(context.Background(), epicKey)
		return epicChildrenLoadedMsg{epicKey: epicKey, issues: issues, err: err}
	}
}

// applyAssign assigns key to the signed-in user, mirroring applyTransition's
// invalidate-then-refetch pattern so the detail screen shows the new
// assignee and the sidebar picks it up on the next tick.
func applyAssign(src jiraSource, key string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		accountID, err := src.accountID(ctx)
		if err != nil {
			return assignAppliedMsg{err: err}
		}
		if err := src.assign(ctx, key, accountID); err != nil {
			return assignAppliedMsg{err: err}
		}
		invalidateLabelCache(key)
		detail, err := src.get(ctx, key)
		return assignAppliedMsg{detail: detail, err: err}
	}
}

// beginBranch starts the [b] "new branch" wizard for key, selected from the
// list currently shown in phase from.
func beginBranch(m pickerModel, key string, from pickerPhase) (pickerModel, tea.Cmd) {
	m.branchKey = key
	m.branchReturnPhase = from
	m.branchWantAssign, m.branchWantTransition = false, false
	m.phase = phaseBranchPrep
	return m, loadBranchPrep(m.src, key)
}

// newBranchSuffixInput's Prompt is the issue key itself, so the box always
// shows the full branch name ("PROJ-2815-start-spec") as it's typed,
// without the model needing to re-render a separate preview line.
func newBranchSuffixInput(key string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = key + "-"
	ti.Placeholder = "start-spec"
	ti.Focus()
	return ti
}

// loadBranchPrep fetches what the wizard needs to decide which follow-up
// questions are worth asking: the issue's current assignee/status, its
// live transitions (board-specific, never hardcoded — same rule
// applyTransition follows), and the signed-in user's own accountId.
func loadBranchPrep(src jiraSource, key string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		detail, err := src.get(ctx, key)
		if err != nil {
			return branchPrepLoadedMsg{err: err}
		}
		transitions, err := src.transitions(ctx, key)
		if err != nil {
			return branchPrepLoadedMsg{err: err}
		}
		accountID, err := src.accountID(ctx)
		if err != nil {
			return branchPrepLoadedMsg{err: err}
		}
		return branchPrepLoadedMsg{detail: detail, transitions: transitions, accountID: accountID}
	}
}

// createBranchCmd runs the actual `git checkout -b` (branch.go), off the
// UI goroutine like every other Jira/git call in this file.
func createBranchCmd(cwd, key, suffix string) tea.Cmd {
	return func() tea.Msg {
		name, err := createBranch(cwd, key, suffix)
		return branchCreatedMsg{name: name, err: err}
	}
}

// nextBranchStep asks the next pending follow-up question, or finishes the
// wizard once both have been answered (or were never applicable).
func nextBranchStep(m pickerModel) (pickerModel, tea.Cmd) {
	if m.branchPendingAssign {
		m.branchPendingAssign = false
		m.branchConfirmKind = branchConfirmAssign
		m.phase = phaseBranchConfirm
		return m, nil
	}
	if m.branchPendingTransition {
		m.branchPendingTransition = false
		m.branchConfirmKind = branchConfirmTransition
		m.phase = phaseBranchConfirm
		return m, nil
	}
	return finishBranch(m)
}

// finishBranch applies whichever follow-ups were accepted, if any, then
// lands back on the originating list.
func finishBranch(m pickerModel) (pickerModel, tea.Cmd) {
	if !m.branchWantAssign && !m.branchWantTransition {
		return landBranchDone(m)
	}
	m.phase = phaseBranchApplying
	return m, applyBranchFollowup(m.src, m.branchKey, m.branchName, m.branchWantAssign, m.branchWantTransition, m.branchTransitionID)
}

// applyBranchFollowup assigns and/or transitions key, mirroring
// applyAssign/applyTransition's invalidate-the-label-cache-after-each-write
// pattern so the sidebar picks up both changes on the next tick. branchName
// and whichever follow-up already landed are folded into any error, since
// by this point the branch itself (and possibly the assign) already
// happened — a bare Jira error would otherwise read as if nothing did.
func applyBranchFollowup(src jiraSource, key, branchName string, wantAssign, wantTransition bool, transitionID string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		done := "created and switched to " + branchName
		if wantAssign {
			accountID, err := src.accountID(ctx)
			if err != nil {
				return branchFollowupMsg{err: fmt.Errorf("%s, but couldn't assign it to you: %w", done, err)}
			}
			if err := src.assign(ctx, key, accountID); err != nil {
				return branchFollowupMsg{err: fmt.Errorf("%s, but couldn't assign it to you: %w", done, err)}
			}
			invalidateLabelCache(key)
			done += ", assigned to you"
		}
		if wantTransition {
			if err := src.transition(ctx, key, transitionID); err != nil {
				return branchFollowupMsg{err: fmt.Errorf("%s, but couldn't transition it to In Progress: %w", done, err)}
			}
			invalidateLabelCache(key)
		}
		return branchFollowupMsg{}
	}
}

// landBranchDone surfaces a summary of what the wizard did on whichever
// list it started from, via that list's own (longer-lived, see
// branchStatusMessageLifetime) status message, and resets the wizard's
// transient state.
func landBranchDone(m pickerModel) (pickerModel, tea.Cmd) {
	summary := "Created and switched to " + m.branchName
	if m.branchWantAssign {
		summary += ", assigned to you"
	}
	if m.branchWantTransition {
		summary += ", moved to In Progress"
	}
	var cmd tea.Cmd
	switch m.branchReturnPhase {
	case phaseEpics:
		cmd = m.focusedEpicList().NewStatusMessage(summary)
	case phaseEpicChildren:
		cmd = m.epicChildrenList.NewStatusMessage(summary)
	default:
		cmd = m.list.NewStatusMessage(summary)
	}
	m.phase = m.branchReturnPhase
	m.branchKey, m.branchName = "", ""
	m.branchWantAssign, m.branchWantTransition = false, false
	return m, cmd
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h, v := lipgloss.NewStyle().Margin(1, 2).GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
		m.transitionList.SetSize(msg.Width-h, msg.Height-v)
		m.searchList.SetSize(msg.Width-h, msg.Height-v-2) // leave room for the input box
		m.searchInput.Width = msg.Width - h
		m.epicChildrenList.SetSize(msg.Width-h, msg.Height-v)
		m.epicSearchList.SetSize(msg.Width-h, msg.Height-v-2) // leave room for the input box
		m.epicSearchInput.Width = msg.Width - h
		// phaseEpics stacks two lists, so each gets half the available
		// height; the footer line below them is accounted for the same
		// way searchList leaves room for its input box above.
		available := msg.Height - v - 1
		ownedHeight := available / 2
		starredHeight := available - ownedHeight
		if ownedHeight < 0 {
			ownedHeight = 0
		}
		if starredHeight < 0 {
			starredHeight = 0
		}
		m.epicList.SetSize(msg.Width-h, ownedHeight)
		m.starredEpicList.SetSize(msg.Width-h, starredHeight)
		return m, nil

	case debounceFireMsg:
		if msg.seq != m.searchSeq {
			return m, nil // a newer keystroke has already superseded this one
		}
		m.searching = true
		return m, runSearchText(m.src, msg.query, msg.seq)

	case searchResultsMsg:
		if msg.seq != m.searchSeq {
			return m, nil // an older query answering after a newer one
		}
		m.searching = false
		m.searchErr = msg.err
		if msg.err == nil {
			setListIssues(&m.searchList, msg.issues)
		}
		return m, nil

	case epicSearchDebounceFireMsg:
		if msg.seq != m.epicSearchSeq {
			return m, nil // a newer keystroke has already superseded this one
		}
		m.epicSearching = true
		return m, runSearchEpics(m.src, msg.query, msg.seq)

	case epicSearchResultsMsg:
		if msg.seq != m.epicSearchSeq {
			return m, nil // an older query answering after a newer one
		}
		m.epicSearching = false
		m.epicSearchErr = msg.err
		if msg.err == nil {
			setListIssues(&m.epicSearchList, msg.issues)
		}
		return m, nil

	case starredEpicsRefreshedMsg:
		if msg.err != nil {
			return m, nil // best-effort refresh; keep showing cached statuses
		}
		st := readStarState()
		for _, status := range msg.statuses {
			st.updateStatus(status)
		}
		_ = writeStarState(st)
		// Always update the in-memory list, even mid-drill-down into a
		// child's detail screen — otherwise this refresh's result is lost
		// the moment the user leaves phaseEpics/phaseEpicSearch, and esc
		// back would show stale statuses until the next full reload.
		m.starredEpicList.ResetFilter()
		setListIssues(&m.starredEpicList, st.statuses())
		return m, nil

	case signedInMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseLoading
			return m, nil
		}
		m.phase = phaseLoading
		return m, loadIssues(m.src)

	case initialDetailMsg:
		if msg.err != nil {
			// Unknown key or no permission: fall back to the list with a
			// notice, rather than treating this as a hard error.
			m.notice = fmt.Sprintf("Couldn't open %s (%v) — showing your issues instead.", msg.key, msg.err)
			return m, loadIssues(m.src)
		}
		d := msg.detail
		m.detail, m.phase = &d, phaseDetail
		return m, loadIssues(m.src) // populate the list in the background, for [esc] later

	case initialNoKeyMsg:
		if msg.branch != "" {
			m.notice = "No ticket in branch " + msg.branch + " — showing your issues instead."
		}
		return m, loadIssues(m.src)

	case issuesLoadedMsg:
		if msg.err != nil {
			if m.phase == phaseLoading {
				m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseLoading
			}
			// Loaded in the background (we're already on the detail
			// screen): a list refresh failure isn't worth interrupting for.
			return m, nil
		}
		setListIssues(&m.list, msg.issues)
		_ = writePickerCache(msg.issues)
		if m.phase == phaseLoading {
			m.phase = phaseReady
		}
		return m, nil

	case detailLoadedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, m.detailReturnPhase
			return m, nil
		}
		d := msg.detail
		m.detail, m.phase = &d, phaseDetail
		return m, nil

	case transitionsLoadedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseDetail
			return m, nil
		}
		setTransitionItems(&m.transitionList, msg.transitions)
		m.phase = phaseTransitions
		return m, nil

	case transitionAppliedMsg:
		if msg.err != nil {
			// The issue stays as it was shown; only the error changes.
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseDetail
			return m, nil
		}
		d := msg.detail
		m.detail, m.phase = &d, phaseDetail
		return m, nil

	case epicsLoadedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseReady
			return m, nil
		}
		// A filter left applied from a previous visit would otherwise leave
		// SetItems' filteredItems stale (bubbles/list only recomputes them
		// via the tea.Cmd this discards), showing an empty list.
		m.epicList.ResetFilter()
		m.epicList.ResetSelected()
		setListIssues(&m.epicList, msg.epics)
		st := readStarState()
		m.starredEpicList.ResetFilter()
		m.starredEpicList.ResetSelected()
		setListIssues(&m.starredEpicList, st.statuses())
		m.epicsOnStarred = false
		m.phase = phaseEpics
		keys := make([]string, len(st.Epics))
		for i, e := range st.Epics {
			keys[i] = e.Key
		}
		return m, refreshStarredEpics(m.src, keys)

	case epicChildrenLoadedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseEpics
			return m, nil
		}
		m.epicChildrenList.ResetFilter()
		m.epicChildrenList.ResetSelected()
		setListIssues(&m.epicChildrenList, msg.issues)
		m.epicChildrenList.Title = "Children of " + msg.epicKey
		m.phase = phaseEpicChildren
		return m, nil

	case assignAppliedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseDetail
			return m, nil
		}
		d := msg.detail
		m.detail, m.phase = &d, phaseDetail
		return m, nil

	case branchPrepLoadedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, m.branchReturnPhase
			return m, nil
		}
		m.branchPendingAssign = msg.detail.AssigneeAccountID != msg.accountID
		m.branchPendingTransition = false
		m.branchTransitionID = ""
		if !strings.EqualFold(msg.detail.StatusName, "In Progress") {
			for _, t := range msg.transitions {
				if strings.EqualFold(t.ToStatusName, "In Progress") {
					m.branchPendingTransition = true
					m.branchTransitionID = t.ID
					break
				}
			}
		}
		m.branchSuffixInput = newBranchSuffixInput(m.branchKey)
		m.phase = phaseBranchSuffix
		return m, textinput.Blink

	case branchCreatedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, m.branchReturnPhase
			return m, nil
		}
		m.branchName = msg.name
		return nextBranchStep(m)

	case branchFollowupMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, m.branchReturnPhase
			return m, nil
		}
		return landBranchDone(m)

	case tea.KeyMsg:
		switch m.phase {
		case phaseError:
			switch msg.String() {
			case "r":
				return m.retry()
			case "esc", "q", "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil

		case phaseDetail:
			switch msg.String() {
			case "t":
				m.phase = phaseTransitionsLoading
				return m, loadTransitions(m.src, m.detail.Key)
			case "a":
				m.phase = phaseAssigning
				return m, applyAssign(m.src, m.detail.Key)
			case "c":
				if err := clipboard.WriteAll(m.detail.Key); err != nil {
					m.phase, m.err, m.errReturnPhase = phaseError, err, phaseDetail
				}
				return m, nil
			case "o":
				u := m.detail.URL
				if u == "" {
					resolved, err := jiraBrowseURL(m.detail.Key)
					if err != nil {
						m.phase, m.err, m.errReturnPhase = phaseError, err, phaseDetail
						return m, nil
					}
					u = resolved
				}
				if err := openBrowser(u); err != nil {
					m.phase, m.err, m.errReturnPhase = phaseError, err, phaseDetail
				}
				return m, nil
			case "esc", "b":
				m.phase, m.detail = m.detailReturnPhase, nil
				return m, nil
			case "q", "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil

		case phaseTransitions:
			switch msg.String() {
			case "esc":
				if m.transitionList.FilterState() == list.Filtering {
					break
				}
				m.phase = phaseDetail
				return m, nil
			case "q", "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "enter":
				if m.transitionList.FilterState() != list.Filtering {
					if item, ok := m.transitionList.SelectedItem().(transitionItem); ok {
						m.phase = phaseApplyingTransition
						return m, applyTransition(m.src, m.detail.Key, item.ID)
					}
				}
			}

		case phaseReady:
			switch msg.String() {
			case "esc":
				if m.list.FilterState() == list.Filtering {
					break // let the list clear the filter first
				}
				m.quitting = true
				return m, tea.Quit
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "enter":
				if m.list.FilterState() != list.Filtering {
					if item, ok := m.list.SelectedItem().(issueItem); ok {
						m.detailReturnPhase = phaseReady
						m.phase = phaseDetailLoading
						return m, loadDetail(m.src, item.Key)
					}
				}
			case "ctrl+f":
				if m.list.FilterState() != list.Filtering {
					m.phase = phaseSearch
					m.searchInput.Focus()
					return m, textinput.Blink
				}
			case "e":
				if m.list.FilterState() != list.Filtering {
					m.phase = phaseEpicsLoading
					return m, loadEpics(m.src)
				}
			case "c":
				if m.list.FilterState() != list.Filtering {
					return m, yankSelectedKey(&m.list)
				}
			case "o":
				if m.list.FilterState() != list.Filtering {
					return m, openSelectedInBrowser(&m.list)
				}
			case "b":
				if m.list.FilterState() != list.Filtering {
					if item, ok := m.list.SelectedItem().(issueItem); ok {
						return beginBranch(m, item.Key, phaseReady)
					}
				}
			}

		case phaseEpics:
			fl := m.focusedEpicList()
			switch msg.String() {
			case "esc":
				if fl.FilterState() == list.Filtering {
					break
				}
				m.phase = phaseReady
				return m, nil
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "q":
				if fl.FilterState() != list.Filtering {
					m.quitting = true
					return m, tea.Quit
				}
			case "tab":
				if fl.FilterState() != list.Filtering {
					m.epicsOnStarred = !m.epicsOnStarred
				}
				return m, nil
			case "ctrl+f":
				if fl.FilterState() != list.Filtering {
					m.phase = phaseEpicSearch
					m.epicSearchOnList = false
					m.epicSearchInput.Focus()
					return m, textinput.Blink
				}
			case "enter":
				if fl.FilterState() != list.Filtering {
					if item, ok := fl.SelectedItem().(issueItem); ok {
						m.phase = phaseEpicChildrenLoading
						return m, loadEpicChildren(m.src, item.Key)
					}
				}
			case "s":
				if fl.FilterState() != list.Filtering {
					if !m.epicsOnStarred {
						return m, m.epicList.NewStatusMessage("Your own epics are already pinned—nothing to star")
					}
					if item, ok := m.starredEpicList.SelectedItem().(issueItem); ok {
						return unstarEpic(m, item.Key)
					}
				}
			case "c":
				if fl.FilterState() != list.Filtering {
					return m, yankSelectedKey(fl)
				}
			case "o":
				if fl.FilterState() != list.Filtering {
					return m, openSelectedInBrowser(fl)
				}
			case "b":
				if fl.FilterState() != list.Filtering {
					if item, ok := fl.SelectedItem().(issueItem); ok {
						return beginBranch(m, item.Key, phaseEpics)
					}
				}
			}

		case phaseEpicSearch:
			// Typing "s" must still work in the query box, so starring needs
			// an explicit focus handoff: [tab] moves focus from the input to
			// the results list, where [s] stars/unstars instead of typing.
			switch msg.Type {
			case tea.KeyCtrlC:
				m.quitting = true
				return m, tea.Quit
			case tea.KeyEsc:
				if m.epicSearchOnList {
					// [tab] is the dedicated way to flip back to typing;
					// [esc] from list-focus always backs out of search
					// entirely, the same one-step-back feel [esc] has
					// everywhere else in the popup.
					m.epicSearchOnList = false
					m.epicSearchInput.Blur()
					m.phase = phaseEpics
					return m, nil
				}
				if m.epicSearchInput.Value() != "" {
					m.epicSearchInput.SetValue("")
					m.epicSearchSeq++ // invalidate any pending debounce/query
					m.epicSearchErr, m.epicSearching = nil, false
					setListIssues(&m.epicSearchList, nil)
					return m, nil
				}
				m.epicSearchInput.Blur()
				m.phase = phaseEpics
				return m, nil
			case tea.KeyTab:
				m.epicSearchOnList = !m.epicSearchOnList
				if m.epicSearchOnList {
					m.epicSearchInput.Blur()
					return m, nil
				}
				m.epicSearchInput.Focus()
				return m, textinput.Blink
			case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
				var cmd tea.Cmd
				m.epicSearchList, cmd = m.epicSearchList.Update(msg)
				return m, cmd
			}
			if m.epicSearchOnList {
				if msg.String() == "s" {
					if item, ok := m.epicSearchList.SelectedItem().(issueItem); ok {
						return toggleStarEpic(m, item.ticketStatus)
					}
				}
				return m, nil // list-focus mode: don't leak other keys into the query
			}
			// Input-focus mode: everything else (characters, backspace, …)
			// is text input.
			prev := m.epicSearchInput.Value()
			var cmd tea.Cmd
			m.epicSearchInput, cmd = m.epicSearchInput.Update(msg)
			if query := m.epicSearchInput.Value(); query != prev {
				m.epicSearchSeq++
				if query == "" {
					m.epicSearchErr, m.epicSearching = nil, false
					setListIssues(&m.epicSearchList, nil)
					return m, cmd
				}
				return m, tea.Batch(cmd, debounceEpicSearchKeystroke(m.epicSearchSeq, query))
			}
			return m, cmd

		case phaseEpicChildren:
			switch msg.String() {
			case "esc":
				if m.epicChildrenList.FilterState() == list.Filtering {
					break
				}
				m.phase = phaseEpics
				return m, nil
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "q":
				if m.epicChildrenList.FilterState() != list.Filtering {
					m.quitting = true
					return m, tea.Quit
				}
			case "enter":
				if m.epicChildrenList.FilterState() != list.Filtering {
					if item, ok := m.epicChildrenList.SelectedItem().(issueItem); ok {
						m.detailReturnPhase = phaseEpicChildren
						m.phase = phaseDetailLoading
						return m, loadDetail(m.src, item.Key)
					}
				}
			case "c":
				if m.epicChildrenList.FilterState() != list.Filtering {
					return m, yankSelectedKey(&m.epicChildrenList)
				}
			case "o":
				if m.epicChildrenList.FilterState() != list.Filtering {
					return m, openSelectedInBrowser(&m.epicChildrenList)
				}
			case "b":
				if m.epicChildrenList.FilterState() != list.Filtering {
					if item, ok := m.epicChildrenList.SelectedItem().(issueItem); ok {
						return beginBranch(m, item.Key, phaseEpicChildren)
					}
				}
			}

		case phaseSearch:
			switch msg.Type {
			case tea.KeyCtrlC:
				m.quitting = true
				return m, tea.Quit
			case tea.KeyEsc:
				if m.searchInput.Value() != "" {
					m.searchInput.SetValue("")
					m.searchSeq++ // invalidate any pending debounce/query
					m.searchErr, m.searching = nil, false
					setListIssues(&m.searchList, nil)
					return m, nil
				}
				m.searchInput.Blur()
				m.phase = phaseReady
				return m, nil
			case tea.KeyEnter:
				if item, ok := m.searchList.SelectedItem().(issueItem); ok {
					m.detailReturnPhase = phaseSearch
					m.phase = phaseDetailLoading
					return m, loadDetail(m.src, item.Key)
				}
				return m, nil
			case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
				var cmd tea.Cmd
				m.searchList, cmd = m.searchList.Update(msg)
				return m, cmd
			}
			// Everything else (characters, backspace, …) is text input.
			prev := m.searchInput.Value()
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(msg)
			if query := m.searchInput.Value(); query != prev {
				m.searchSeq++
				if query == "" {
					m.searchErr, m.searching = nil, false
					setListIssues(&m.searchList, nil)
					return m, cmd
				}
				return m, tea.Batch(cmd, debounceKeystroke(m.searchSeq, query))
			}
			return m, cmd

		case phaseBranchSuffix:
			switch msg.Type {
			case tea.KeyCtrlC:
				m.quitting = true
				return m, tea.Quit
			case tea.KeyEsc:
				m.phase = m.branchReturnPhase
				return m, nil
			case tea.KeyEnter:
				suffix := strings.TrimSpace(m.branchSuffixInput.Value())
				if suffix == "" {
					return m, nil
				}
				m.phase = phaseBranchCreating
				return m, createBranchCmd(m.cwd, m.branchKey, suffix)
			}
			var cmd tea.Cmd
			m.branchSuffixInput, cmd = m.branchSuffixInput.Update(msg)
			return m, cmd

		case phaseBranchConfirm:
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "y":
				if m.branchConfirmKind == branchConfirmAssign {
					m.branchWantAssign = true
				} else {
					m.branchWantTransition = true
				}
				return nextBranchStep(m)
			case "n":
				return nextBranchStep(m)
			case "esc":
				return finishBranch(m)
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	switch m.phase {
	case phaseReady:
		m.list, cmd = m.list.Update(msg)
	case phaseTransitions:
		m.transitionList, cmd = m.transitionList.Update(msg)
	case phaseSearch:
		m.searchInput, cmd = m.searchInput.Update(msg)
	case phaseEpics:
		// Route to whichever list [tab] last focused — otherwise arrow keys
		// and "/" filtering would always land on epicList regardless of
		// which of the two stacked lists the user is looking at.
		if m.epicsOnStarred {
			m.starredEpicList, cmd = m.starredEpicList.Update(msg)
		} else {
			m.epicList, cmd = m.epicList.Update(msg)
		}
	case phaseEpicChildren:
		m.epicChildrenList, cmd = m.epicChildrenList.Update(msg)
	case phaseEpicSearch:
		if m.epicSearchOnList {
			m.epicSearchList, cmd = m.epicSearchList.Update(msg)
		} else {
			m.epicSearchInput, cmd = m.epicSearchInput.Update(msg)
		}
	}
	return m, cmd
}

// retry re-runs whatever the error interrupted.
func (m pickerModel) retry() (tea.Model, tea.Cmd) {
	switch m.errReturnPhase {
	case phaseDetail:
		if m.detail != nil {
			m.phase, m.err = phaseDetail, nil
			return m, nil
		}
	case phaseLoading:
		m.phase, m.err = phaseLoading, nil
		return m, checkSignInThenLoad(m.src)
	}
	m.phase, m.err = phaseReady, nil
	return m, nil
}

func (m pickerModel) View() string {
	if m.quitting {
		return ""
	}
	switch m.phase {
	case phaseLoading:
		return "\n  Waiting for you to sign in to Jira in the browser…\n  (opens automatically on first use)\n"
	case phaseDetailLoading:
		return "\n  Loading…\n"
	case phaseTransitionsLoading:
		return "\n  Loading transitions…\n"
	case phaseApplyingTransition:
		return "\n  Applying…\n"
	case phaseAssigning:
		return "\n  Assigning…\n"
	case phaseEpicsLoading:
		return "\n  Loading your epics…\n"
	case phaseEpicChildrenLoading:
		return "\n  Loading epic's children…\n"
	case phaseBranchPrep:
		return "\n  Loading…\n"
	case phaseBranchSuffix:
		return fmt.Sprintf("\n  New branch for %s\n\n  %s\n\n  [enter] create & switch    [esc] cancel\n", m.branchKey, m.branchSuffixInput.View())
	case phaseBranchCreating:
		return "\n  Creating branch…\n"
	case phaseBranchConfirm:
		question := "Assign " + m.branchKey + " to you?"
		if m.branchConfirmKind == branchConfirmTransition {
			question = "Transition " + m.branchKey + " to In Progress?"
		}
		return fmt.Sprintf("\n  %s\n\n  [y] yes    [n] no    [esc] skip remaining\n", question)
	case phaseBranchApplying:
		return "\n  Updating Jira…\n"
	case phaseError:
		return fmt.Sprintf("\n  %s\n\n  [r] retry    [esc] close\n", m.err)
	case phaseDetail:
		d := m.detail
		s := fmt.Sprintf("\n  %s  %s\n\n  %s\n", d.Key, d.StatusName, d.Summary)
		if d.AssigneeName != "" {
			s += fmt.Sprintf("  Assignee: %s\n", d.AssigneeName)
		}
		if d.URL != "" {
			s += fmt.Sprintf("  %s\n", d.URL)
		}
		s += "\n  [t] transition    [a] assign to me    [c] copy key    [o] open in browser    [esc] back    [q] close\n"
		return s
	case phaseTransitions:
		return m.transitionList.View()
	case phaseEpics:
		owned, starred := m.epicList, m.starredEpicList
		if m.epicsOnStarred {
			starred.Title = "▶ Starred epics"
			owned.Title = "Your epics"
		} else {
			owned.Title = "▶ Your epics"
			starred.Title = "Starred epics"
		}
		footer := "\n  [enter] children  [tab] switch  [ctrl+f] search  [s] unstar  [c] copy  [o] browser  [b] branch  [esc] back\n"
		return owned.View() + "\n" + starred.View() + footer
	case phaseEpicChildren:
		return m.epicChildrenList.View()
	case phaseEpicSearch:
		focusHint := "[tab] focus results    [esc] " + searchEscHint(m.epicSearchInput.Value())
		if m.epicSearchOnList {
			focusHint = "[tab] back to typing    [s] star/unstar    [esc] back"
		}
		status := "  " + focusHint
		if m.epicSearching {
			status = "  searching…"
		} else if m.epicSearchErr != nil {
			status = fmt.Sprintf("  %v", m.epicSearchErr)
		}
		return "  " + m.epicSearchInput.View() + "\n" + status + "\n\n" + m.epicSearchList.View()
	case phaseSearch:
		status := "  [enter] open   [esc] " + searchEscHint(m.searchInput.Value())
		if m.searching {
			status = "  searching…"
		} else if m.searchErr != nil {
			status = fmt.Sprintf("  %v", m.searchErr)
		}
		return "  " + m.searchInput.View() + "\n" + status + "\n\n" + m.searchList.View()
	default:
		s := ""
		if m.notice != "" {
			s += "  " + m.notice + "\n"
		}
		return s + m.list.View() + "\n  [ctrl+f] search all issues    [e] your epics    [c] copy key    [b] new branch\n"
	}
}

// searchEscHint matches the esc key's actual behavior in phaseSearch: clear
// the box first if there's text in it, only then leave search mode.
func searchEscHint(query string) string {
	if query == "" {
		return "back"
	}
	return "clear"
}

// runPicker runs the popup program to completion.
func runPicker(ctx context.Context, src jiraSource) error {
	_, err := tea.NewProgram(newPickerModel(src), tea.WithAltScreen()).Run()
	return err
}

// runPickerFor is runPicker for the "issue" action: initialKeys/noKeyBranch
// come from the invoking space's branch, resolved by the action before the
// popup started (herdr.go's openPopup env). cwd is that same space's
// checkout path, used only by the [b] "new branch" shortcut.
func runPickerFor(ctx context.Context, src jiraSource, initialKeys []string, noKeyBranch, cwd string) error {
	m := newPickerModelFor(src, initialKeys, noKeyBranch)
	m.cwd = cwd
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
