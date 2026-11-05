package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// searchDebounce is how long the search box waits after the last keystroke
// before querying Jira, so fast typing doesn't fire a request per rune.
const searchDebounce = 300 * time.Millisecond

// branchStatusMessageLifetime overrides bubbles/list's 1s default so the
// confirmation on the epic children screen stays readable.
const branchStatusMessageLifetime = 4 * time.Second

// issueItem adapts ticketStatus to bubbles/list.Item (the epic children screen
// is the one remaining bubbles/list).
type issueItem struct{ ticketStatus }

func (i issueItem) Title() string       { return i.Summary }
func (i issueItem) Description() string { return i.Key + "  " + i.StatusName }
func (i issueItem) FilterValue() string { return i.Key + " " + i.Summary }

// yankKey copies an issue key to the clipboard (e.g. for pasting into a new
// branch name) and returns the message to show.
func yankKey(key string) string {
	if err := clipboard.WriteAll(key); err != nil {
		return fmt.Sprintf("Couldn't copy %s: %v", key, err)
	}
	return "Copied " + key
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

// openKeyInBrowser opens an issue's Jira page in the default browser and
// returns the message to show.
func openKeyInBrowser(key string) string {
	u, err := jiraBrowseURL(key)
	if err != nil {
		return fmt.Sprintf("Couldn't open %s: %v", key, err)
	}
	if err := openBrowser(u); err != nil {
		return fmt.Sprintf("Couldn't open %s: %v", key, err)
	}
	return "Opened " + key
}

// transitionItem adapts transitionOption to bubbles/list.Item.
type transitionItem struct{ transitionOption }

func (i transitionItem) Title() string       { return i.Name }
func (i transitionItem) Description() string { return "→ " + i.ToStatusName }
func (i transitionItem) FilterValue() string { return i.Name }

type pickerPhase int

const (
	phaseLoading pickerPhase = iota
	phaseReady               // the tabbed lanes view
	phaseDetailLoading
	phaseDetail
	phaseTransitionsLoading
	phaseTransitions
	phaseApplyingTransition
	phaseError
	phaseEpicChildrenLoading
	phaseEpicChildren
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
	src jiraSource
	th  theme

	width, height int

	// The tabbed lanes view. data holds what was fetched; buildRows turns it
	// (plus the filter and search hits) into the rows view displays.
	tab       tab
	tabCursor [2]string // the cursor's issue key per tab, restored on switching back
	data      laneData
	view      listView
	mode      inputMode
	showHelp  bool
	status    string // transient one-line message, cleared on the next key
	cloudID   string // the site the starred epics belong to
	// epicsFetchedAt is when the Epics data was last fetched from Jira.
	epicsFetchedAt time.Time
	dirty          bool // an issue was changed; refresh the tab when returning to it
	tabLoading     [2]bool
	tabLoaded      [2]bool
	tabErr         [2]error
	// tabFresh: the tab's data came from a cache still inside its TTL (or a
	// fetch this session), so opening or switching to it needs no API call.
	tabFresh [2]bool

	// Search mode: a textinput drives a local filter at once and, after
	// searchDebounce, one remote search whose hits fill the Results lane.
	// searchSeq tags each keystroke's debounce/query pair so a stale response
	// is dropped rather than clobbering fresher results.
	searchInput  textinput.Model
	searchSeq    int
	searching    bool
	searchErr    error
	hits         searchHits
	preSearchKey string // the row the cursor was on, restored when search is cleared

	phase          pickerPhase
	err            error
	errReturnPhase pickerPhase // where [r]/[esc] goes after an error

	detail            *issueDetail
	detailReturnPhase pickerPhase // where [esc] from the detail screen goes back to
	pendingTransition bool        // after the detail loads, go straight to the transition list
	transitionList    list.Model

	// Epic children: Enter on an epic loads its children into this list.
	epicChildrenList list.Model
	childrenEpicKey  string

	// initialKeys and noKeyBranch drive the "issue" action: open straight
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

	// The [b] "new branch" wizard's state. branchReturnPhase is which screen
	// (and which selected issue, via branchKey) started it, so [esc]/done
	// lands back in the right place.
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
	// issuesLoadedMsg carries the Issues tab: both lane queries.
	issuesLoadedMsg struct {
		issues, subtasks         []ticketStatus
		issuesMore, subtasksMore bool
		err                      error
	}
	// epicsLoadedMsg carries the Epics tab: assigned epics, plus the starred
	// epics as Jira returned them and every starred key.
	epicsLoadedMsg struct {
		epics       []ticketStatus
		more        bool
		starred     []ticketStatus
		starredKeys []string
		cloudID     string
		err         error
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
		scope  searchScope
		issues []ticketStatus
		err    error
	}
	epicChildrenLoadedMsg struct {
		epicKey string
		issues  []ticketStatus
		err     error
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
	th := newTheme(terminalIsPlain())
	tl := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	tl.Title = "Transition to…"

	ecl := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	ecl.SetShowHelp(true)
	ecl.StatusMessageLifetime = branchStatusMessageLifetime

	si := textinput.New()
	si.Prompt = "/ "
	si.Placeholder = "filter, or key (PROJ-1234) / text to search Jira…"

	m := pickerModel{
		src: src, th: th, width: 80, height: 24,
		view:              newListView(th),
		searchInput:       si,
		transitionList:    tl,
		epicChildrenList:  ecl,
		phase:             phaseLoading,
		detailReturnPhase: phaseReady,
		initialKeys:       initialKeys, noKeyBranch: noKeyBranch,
	}
	if cached := readPickerCache(); len(cached) > 0 {
		m.data.Issues, m.data.Subtasks = splitCached(cached)
		m.tabLoaded[tabIssues] = true
		m.tabFresh[tabIssues] = issuesCacheFresh()
		m.phase = phaseReady
	}
	if c, ok := readEpicsCache(); ok {
		m.applyEpics(c)
		m.tabLoaded[tabEpics] = true
		m.tabFresh[tabEpics] = c.fresh()
	}
	m.rebuild()
	m.layoutList()
	return m
}

func setTransitionItems(l *list.Model, transitions []transitionOption) {
	items := make([]list.Item, len(transitions))
	for i, t := range transitions {
		items[i] = transitionItem{t}
	}
	l.SetItems(items)
}

func setListIssues(l *list.Model, issues []ticketStatus) {
	items := make([]list.Item, len(issues))
	for i, s := range issues {
		items[i] = issueItem{s}
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
		return fetchIssues(src)
	}
}

func fetchIssues(src jiraSource) issuesLoadedMsg {
	issues, subtasks, issuesMore, subtasksMore, err := src.myIssues(context.Background())
	return issuesLoadedMsg{issues: issues, subtasks: subtasks, issuesMore: issuesMore, subtasksMore: subtasksMore, err: err}
}

func loadIssues(src jiraSource) tea.Cmd {
	return func() tea.Msg { return fetchIssues(src) }
}

// loadEpicsTab loads the Epics tab in one command: the epics assigned to the
// user, then the starred epics in one batched lookup by key. The star file is
// read after myEpics so the cloud ID has been resolved and stored.
func loadEpicsTab(src jiraSource) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		epics, more, err := src.myEpics(ctx)
		if err != nil {
			return epicsLoadedMsg{err: err}
		}
		cloudID := cloudIDFn()
		keys := starredKeys(cloudID)
		var starred []ticketStatus
		if len(keys) > 0 {
			found, err := src.search(ctx, keys)
			if err != nil {
				return epicsLoadedMsg{err: err}
			}
			for _, k := range keys {
				if t, ok := found[strings.ToUpper(k)]; ok {
					starred = append(starred, t)
				}
			}
		}
		return epicsLoadedMsg{epics: epics, more: more, starred: starred, starredKeys: keys, cloudID: cloudID}
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

func runSearchScoped(src jiraSource, query string, scope searchScope, seq int) tea.Cmd {
	return func() tea.Msg {
		issues, err := src.searchScoped(context.Background(), query, scope)
		return searchResultsMsg{seq: seq, scope: scope, issues: issues, err: err}
	}
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

// landBranchDone surfaces a summary of what the wizard did on the screen it
// started from, and resets the wizard's transient state. If it changed the
// issue, the tab is refreshed so the list shows the new status.
func landBranchDone(m pickerModel) (pickerModel, tea.Cmd) {
	summary := "Created and switched to " + m.branchName
	if m.branchWantAssign {
		summary += ", assigned to you"
	}
	if m.branchWantTransition {
		summary += ", moved to In Progress"
	}
	var cmd tea.Cmd
	if m.branchReturnPhase == phaseEpicChildren {
		cmd = m.epicChildrenList.NewStatusMessage(summary)
	} else {
		m.status = summary
		if m.branchWantAssign || m.branchWantTransition {
			cmd = m.refreshCurrentTab()
		}
	}
	m.phase = m.branchReturnPhase
	m.branchKey, m.branchName = "", ""
	m.branchWantAssign, m.branchWantTransition = false, false
	return m, cmd
}

// ---- tabbed view ----------------------------------------------------------

// headerLines is how many lines sit above the list: the tab bar, the search
// box in search mode, and the notice if there is one.
func (m *pickerModel) headerLines() int {
	n := 1
	if m.mode == modeSearch {
		n++
	}
	if m.notice != "" {
		n++
	}
	return n
}

// layoutList gives the list the lines left after the header and the footer.
func (m *pickerModel) layoutList() {
	m.view.setHeight(m.height - m.headerLines() - 1)
}

// activeFilter is the local filter: the search box's text while searching.
func (m *pickerModel) activeFilter() string {
	if m.mode != modeSearch {
		return ""
	}
	return strings.TrimSpace(m.searchInput.Value())
}

// rebuild recomputes the rows from the fetched data, the filter and the
// search hits, keeping the cursor on the same key.
func (m *pickerModel) rebuild() {
	filter := m.activeFilter()
	var hits searchHits
	if filter != "" {
		hits = m.hits
	}
	rows := buildRows(m.tab, m.data, filter, hits)
	if filter != "" && m.searchErr != nil {
		rows = append(rows,
			row{Kind: rowHeading, Lane: laneResults},
			row{Kind: rowNote, Text: "Search failed: " + m.searchErr.Error()})
	}
	m.view.setRows(rows)
}

// refreshCurrentTab reloads the tab being shown.
func (m *pickerModel) refreshCurrentTab() tea.Cmd {
	m.tabErr[m.tab] = nil
	m.tabLoading[m.tab] = true
	if m.tab == tabEpics {
		return loadEpicsTab(m.src)
	}
	return loadIssues(m.src)
}

// switchTab shows tab t, loading it the first time. Any search in flight
// belongs to the old tab, so it is invalidated and, if there is a query,
// restarted for the new one.
func (m *pickerModel) switchTab(t tab) tea.Cmd {
	if t == m.tab {
		return nil
	}
	if r, ok := m.view.selected(); ok {
		m.tabCursor[m.tab] = r.Issue.Key
	}
	m.tab = t
	m.searchSeq++
	m.hits, m.searching, m.searchErr = nil, false, nil
	m.rebuild()
	if key := m.tabCursor[t]; key != "" {
		m.view.selectKey(key)
	}
	var cmds []tea.Cmd
	if (!m.tabLoaded[t] || !m.tabFresh[t]) && !m.tabLoading[t] {
		m.tabLoading[t] = true
		if t == tabEpics {
			cmds = append(cmds, loadEpicsTab(m.src))
		} else {
			cmds = append(cmds, loadIssues(m.src))
		}
	}
	if q := m.activeFilter(); q != "" {
		cmds = append(cmds, debounceKeystroke(m.searchSeq, q))
	}
	return tea.Batch(cmds...)
}

// enterSearch switches to search mode, remembering the cursor's row so
// clearing the search puts it back.
func (m *pickerModel) enterSearch() tea.Cmd {
	m.preSearchKey = ""
	if r, ok := m.view.selected(); ok {
		m.preSearchKey = r.Issue.Key
	}
	m.mode = modeSearch
	m.searchInput.SetValue("")
	m.searchInput.Focus()
	m.searchSeq++
	m.hits, m.searching, m.searchErr = nil, false, nil
	return textinput.Blink
}

// exitSearch clears the query and returns to browse mode with the cursor back
// on the row it was on.
func (m *pickerModel) exitSearch() {
	m.mode = modeBrowse
	m.searchInput.SetValue("")
	m.searchInput.Blur()
	m.searchSeq++ // invalidate any pending debounce/query
	m.hits, m.searching, m.searchErr = nil, false, nil
	m.rebuild()
	if m.preSearchKey != "" {
		m.view.selectKey(m.preSearchKey)
	}
}

// onQueryChanged reacts to an edited search box: filter locally at once and
// schedule one remote search after the debounce.
func (m *pickerModel) onQueryChanged(inputCmd tea.Cmd) tea.Cmd {
	m.searchSeq++
	m.hits, m.searching, m.searchErr = nil, false, nil
	m.rebuild()
	q := m.activeFilter()
	if q == "" {
		return inputCmd
	}
	return tea.Batch(inputCmd, debounceKeystroke(m.searchSeq, q))
}

// applyEpics loads an Epics-tab snapshot (fetched or cached) into the model.
func (m *pickerModel) applyEpics(c epicsCache) {
	m.cloudID = c.CloudID
	m.epicsFetchedAt = c.FetchedAt
	m.data.Epics, m.data.EpicsMore = c.Epics, c.More
	m.data.Starred = c.Starred
	m.data.StarredKeys = map[string]bool{}
	for _, k := range c.StarredKeys {
		m.data.StarredKeys[k] = true
	}
}

// saveEpicsCache persists the current Epics data after a local change (a
// star toggle), keeping the original fetch time so the TTL isn't extended.
func (m *pickerModel) saveEpicsCache() {
	if m.epicsFetchedAt.IsZero() {
		return // nothing was ever fetched; the star file alone holds the change
	}
	keys := make([]string, 0, len(m.data.StarredKeys))
	for k := range m.data.StarredKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	_ = writeEpicsCache(epicsCache{
		FetchedAt: m.epicsFetchedAt, Epics: m.data.Epics, More: m.data.EpicsMore,
		Starred: m.data.Starred, StarredKeys: keys, CloudID: m.cloudID,
	})
}

// toggleStarRow stars or unstars the epic under the cursor.
func (m *pickerModel) toggleStarRow(r row) {
	starred, err := toggleStar(m.cloudID, r.Issue)
	if err != nil {
		m.status = fmt.Sprintf("Couldn't save: %v", err)
		return
	}
	if m.data.StarredKeys == nil {
		m.data.StarredKeys = map[string]bool{}
	}
	if starred {
		m.data.StarredKeys[r.Issue.Key] = true
		m.data.Starred = append(m.data.Starred, r.Issue)
		m.status = "Starred " + r.Issue.Key
	} else {
		delete(m.data.StarredKeys, r.Issue.Key)
		kept := m.data.Starred[:0:0]
		for _, s := range m.data.Starred {
			if s.Key != r.Issue.Key {
				kept = append(kept, s)
			}
		}
		m.data.Starred = kept
		m.status = "Unstarred " + r.Issue.Key
	}
	m.saveEpicsCache()
	m.rebuild()
}

func init() {
	quit := func(m *pickerModel, _ row) tea.Cmd {
		m.quitting = true
		return tea.Quit
	}
	handlers["quit"] = quit
	handlers["back"] = quit
	handlers["help"] = func(m *pickerModel, _ row) tea.Cmd { m.showHelp = true; return nil }
	handlers["down"] = func(m *pickerModel, _ row) tea.Cmd { m.view.moveCursor(1); return nil }
	handlers["up"] = func(m *pickerModel, _ row) tea.Cmd { m.view.moveCursor(-1); return nil }
	handlers["next_tab"] = func(m *pickerModel, _ row) tea.Cmd { return m.switchTab((m.tab + 1) % 2) }
	handlers["prev_tab"] = func(m *pickerModel, _ row) tea.Cmd { return m.switchTab((m.tab + 1) % 2) }
	handlers["tab_issues"] = func(m *pickerModel, _ row) tea.Cmd { return m.switchTab(tabIssues) }
	handlers["tab_epics"] = func(m *pickerModel, _ row) tea.Cmd { return m.switchTab(tabEpics) }
	handlers["refresh"] = func(m *pickerModel, _ row) tea.Cmd { return m.refreshCurrentTab() }
	handlers["search"] = func(m *pickerModel, _ row) tea.Cmd { return m.enterSearch() }
	handlers["clear_search"] = func(m *pickerModel, _ row) tea.Cmd { m.exitSearch(); return nil }
	handlers["star"] = func(m *pickerModel, r row) tea.Cmd { m.toggleStarRow(r); return nil }
	handlers["copy"] = func(m *pickerModel, r row) tea.Cmd { m.status = yankKey(r.Issue.Key); return nil }
	handlers["browser"] = func(m *pickerModel, r row) tea.Cmd { m.status = openKeyInBrowser(r.Issue.Key); return nil }
	handlers["open"] = func(m *pickerModel, r row) tea.Cmd {
		if r.Kind == rowEpic {
			m.childrenEpicKey = r.Issue.Key
			m.phase = phaseEpicChildrenLoading
			return loadEpicChildren(m.src, r.Issue.Key)
		}
		m.detailReturnPhase = phaseReady
		m.phase = phaseDetailLoading
		return loadDetail(m.src, r.Issue.Key)
	}
	handlers["transition"] = func(m *pickerModel, r row) tea.Cmd {
		// Load the detail first: the transition list lives under the detail
		// screen, and its [esc] returns there.
		m.detailReturnPhase = phaseReady
		m.pendingTransition = true
		m.phase = phaseDetailLoading
		return loadDetail(m.src, r.Issue.Key)
	}
	handlers["assign"] = func(m *pickerModel, r row) tea.Cmd {
		m.detailReturnPhase = phaseReady
		m.phase = phaseAssigning
		return applyAssign(m.src, r.Issue.Key)
	}
	handlers["branch"] = func(m *pickerModel, r row) tea.Cmd {
		next, cmd := beginBranch(*m, r.Issue.Key, phaseReady)
		*m = next
		return cmd
	}
}

// updateReadyKey routes a key on the tabbed view through the hotkey table.
func (m pickerModel) updateReadyKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	if m.showHelp {
		m.showHelp = false
		return m, nil
	}
	r, hasRow := m.view.selected()
	if h, ok := lookupHotkey(m.mode, key, r, hasRow); ok {
		cmd := handlers[h.Action](&m, r)
		return m, cmd
	}
	if m.mode == modeSearch {
		prev := m.searchInput.Value()
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		if m.searchInput.Value() != prev {
			return m, m.onQueryChanged(cmd)
		}
		return m, cmd
	}
	return m, nil
}

// handleMouse: click a tab, click a row (a second click on the selected row
// opens it), wheel to scroll. Clicks on headings do nothing.
func (m pickerModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.phase != phaseReady || m.showHelp || msg.Action != tea.MouseActionPress {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.view.scrollBy(-wheelStep)
	case tea.MouseButtonWheelDown:
		m.view.scrollBy(wheelStep)
	case tea.MouseButtonLeft:
		if msg.Y == 0 {
			if t, ok := tabAt(msg.X); ok {
				return m, m.switchTab(t)
			}
			return m, nil
		}
		idx := m.view.hitTest(msg.Y - m.headerLines())
		if idx < 0 {
			return m, nil
		}
		if idx == m.view.cursor {
			r, _ := m.view.selected()
			return m, handlers["open"](&m, r)
		}
		m.view.cursor = idx
		m.view.scrollToCursor()
	}
	return m, nil
}

// ---- Update ---------------------------------------------------------------

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	pm := next.(pickerModel)
	pm.layoutList()
	return pm, cmd
}

func (m pickerModel) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.transitionList.SetSize(msg.Width-4, msg.Height-2)
		m.epicChildrenList.SetSize(msg.Width-4, msg.Height-3)
		m.searchInput.Width = max(msg.Width-8, 10)
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case debounceFireMsg:
		if msg.seq != m.searchSeq {
			return m, nil // a newer keystroke has already superseded this one
		}
		m.searching = true
		return m, runSearchScoped(m.src, msg.query, m.tab.searchScope(), msg.seq)

	case searchResultsMsg:
		if msg.seq != m.searchSeq || msg.scope != m.tab.searchScope() {
			return m, nil // an older query answering after a newer one
		}
		m.searching = false
		m.searchErr = msg.err
		m.hits = nil
		if msg.err == nil {
			m.hits = append(searchHits{}, msg.issues...)
		}
		m.rebuild()
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
		if m.tabFresh[tabIssues] {
			return m, nil
		}
		return m, loadIssues(m.src)

	case issuesLoadedMsg:
		m.tabLoading[tabIssues] = false
		if msg.err != nil {
			if m.phase == phaseLoading {
				m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseLoading
				return m, nil
			}
			m.tabErr[tabIssues] = msg.err
			if m.tabLoaded[tabIssues] && m.phase == phaseReady {
				m.status = fmt.Sprintf("Couldn't refresh: %v", msg.err)
			}
			return m, nil
		}
		m.tabErr[tabIssues] = nil
		m.tabLoaded[tabIssues] = true
		m.tabFresh[tabIssues] = true
		m.data.Issues, m.data.Subtasks = msg.issues, msg.subtasks
		m.data.IssuesMore, m.data.SubtasksMore = msg.issuesMore, msg.subtasksMore
		_ = writePickerCache(msg.issues, msg.subtasks)
		if m.phase == phaseLoading {
			m.phase = phaseReady
		}
		m.rebuild()
		return m, nil

	case epicsLoadedMsg:
		m.tabLoading[tabEpics] = false
		if msg.err != nil {
			m.tabErr[tabEpics] = msg.err
			if m.tabLoaded[tabEpics] && m.phase == phaseReady {
				m.status = fmt.Sprintf("Couldn't refresh: %v", msg.err)
			}
			return m, nil
		}
		m.tabErr[tabEpics] = nil
		m.tabLoaded[tabEpics] = true
		m.tabFresh[tabEpics] = true
		c := epicsCache{
			FetchedAt: nowFn(), Epics: msg.epics, More: msg.more,
			Starred: msg.starred, StarredKeys: msg.starredKeys, CloudID: msg.cloudID,
		}
		m.applyEpics(c)
		m.epicsFetchedAt = c.FetchedAt
		_ = writeEpicsCache(c)
		m.rebuild()
		return m, nil

	case detailLoadedMsg:
		if msg.err != nil {
			m.pendingTransition = false
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, m.detailReturnPhase
			return m, nil
		}
		d := msg.detail
		m.detail = &d
		if m.pendingTransition {
			m.pendingTransition = false
			m.phase = phaseTransitionsLoading
			return m, loadTransitions(m.src, d.Key)
		}
		m.phase = phaseDetail
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
		m.detail, m.phase, m.dirty = &d, phaseDetail, true
		return m, nil

	case epicChildrenLoadedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, phaseReady
			return m, nil
		}
		// A filter left applied from a previous visit would otherwise leave
		// SetItems' filteredItems stale (bubbles/list only recomputes them
		// via the tea.Cmd this discards), showing an empty list.
		m.epicChildrenList.ResetFilter()
		m.epicChildrenList.ResetSelected()
		setListIssues(&m.epicChildrenList, msg.issues)
		m.epicChildrenList.Title = "Children of " + msg.epicKey
		m.childrenEpicKey = msg.epicKey
		m.phase = phaseEpicChildren
		return m, nil

	case assignAppliedMsg:
		if msg.err != nil {
			m.phase, m.err, m.errReturnPhase = phaseError, msg.err, m.detailReturnPhase
			return m, nil
		}
		d := msg.detail
		m.detail, m.phase, m.dirty = &d, phaseDetail, true
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
		m.status = ""
		switch m.phase {
		case phaseReady:
			return m.updateReadyKey(msg)

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
			case "esc":
				ret := m.detailReturnPhase
				m.phase, m.detail = ret, nil
				if ret == phaseReady && m.dirty {
					m.dirty = false
					return m, m.refreshCurrentTab()
				}
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

		case phaseEpicChildren:
			l := &m.epicChildrenList
			filtering := l.FilterState() == list.Filtering
			selected, hasSelected := l.SelectedItem().(issueItem)
			switch msg.String() {
			case "esc":
				if filtering {
					break
				}
				m.phase = phaseReady
				m.view.selectKey(m.childrenEpicKey) // the cursor stays on that epic
				return m, nil
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "q":
				if !filtering {
					m.quitting = true
					return m, tea.Quit
				}
			case "i": // the epic's own detail
				if !filtering {
					m.detailReturnPhase = phaseEpicChildren
					m.phase = phaseDetailLoading
					return m, loadDetail(m.src, m.childrenEpicKey)
				}
			case "enter":
				if !filtering && hasSelected {
					m.detailReturnPhase = phaseEpicChildren
					m.phase = phaseDetailLoading
					return m, loadDetail(m.src, selected.Key)
				}
			case "t":
				if !filtering && hasSelected {
					m.detailReturnPhase = phaseEpicChildren
					m.pendingTransition = true
					m.phase = phaseDetailLoading
					return m, loadDetail(m.src, selected.Key)
				}
			case "a":
				if !filtering && hasSelected {
					m.detailReturnPhase = phaseEpicChildren
					m.phase = phaseAssigning
					return m, applyAssign(m.src, selected.Key)
				}
			case "c":
				if !filtering && hasSelected {
					return m, l.NewStatusMessage(yankKey(selected.Key))
				}
			case "o":
				if !filtering && hasSelected {
					return m, l.NewStatusMessage(openKeyInBrowser(selected.Key))
				}
			case "b":
				if !filtering && hasSelected {
					return beginBranch(m, selected.Key, phaseEpicChildren)
				}
			}

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
	case phaseTransitions:
		m.transitionList, cmd = m.transitionList.Update(msg)
	case phaseEpicChildren:
		m.epicChildrenList, cmd = m.epicChildrenList.Update(msg)
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

// ---- View -----------------------------------------------------------------

// tabSpans gives each tab label's x range [start, end) on the tab bar, shared
// by drawing and mouse hit-testing so they cannot disagree.
func tabSpans() [2][2]int {
	var spans [2][2]int
	x := 1
	for i, t := range []tab{tabIssues, tabEpics} {
		w := len(t.String()) + 2
		spans[i] = [2]int{x, x + w}
		x += w + 1
	}
	return spans
}

// tabAt returns the tab whose label covers column x.
func tabAt(x int) (tab, bool) {
	for i, s := range tabSpans() {
		if x >= s[0] && x < s[1] {
			return tab(i), true
		}
	}
	return 0, false
}

func (m pickerModel) tabBar() string {
	var b strings.Builder
	b.WriteString(" ")
	for i, t := range []tab{tabIssues, tabEpics} {
		active := t == m.tab
		label := " " + t.String() + " "
		switch {
		case active && m.th.plain:
			label = "[" + t.String() + "]"
		case active:
			label = m.th.tabActive.Render(label)
		default:
			label = m.th.tabInactive.Render(label)
		}
		b.WriteString(label)
		if i == 0 {
			b.WriteString(" ")
		}
	}
	return b.String()
}

func padLines(s string, n int) string {
	var lines []string
	if s != "" {
		lines = strings.Split(s, "\n")
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func (m pickerModel) listBody() string {
	if !m.tabLoaded[m.tab] {
		if err := m.tabErr[m.tab]; err != nil {
			return m.th.errLine.Render("  "+err.Error()) + "\n\n  [r] retry"
		}
		return m.th.dim.Render("  Loading " + strings.ToLower(m.tab.String()) + "…")
	}
	return m.view.render(m.width)
}

func (m pickerModel) readyView() string {
	if m.showHelp {
		return padLines(helpText(), m.height)
	}
	lines := []string{m.tabBar()}
	if m.mode == modeSearch {
		l := "  " + m.searchInput.View()
		if m.searching {
			l += m.th.dim.Render("  searching…")
		}
		lines = append(lines, l)
	}
	if m.notice != "" {
		lines = append(lines, "  "+m.notice)
	}
	r, hasRow := m.view.selected()
	footer := m.status
	if footer == "" {
		footer = footerLegend(m.mode, r, hasRow, m.width)
	}
	footer = m.th.dim.Render("  " + footer)
	return strings.Join(lines, "\n") + "\n" + padLines(m.listBody(), m.view.height) + "\n" + footer
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
	case phaseEpicChildren:
		return m.epicChildrenList.View() + "\n  [enter] open  [i] epic detail  [t] transition  [a] assign  [b] branch  [c] copy  [o] browser  [esc] back"
	}
	return m.readyView()
}

// runPicker runs the popup program to completion.
func runPicker(ctx context.Context, src jiraSource) error {
	_, err := tea.NewProgram(newPickerModel(src), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

// runPickerFor is runPicker for the "issue" action: initialKeys/noKeyBranch
// come from the invoking space's branch, resolved by the action before the
// popup started (herdr.go's openPopup env). cwd is that same space's
// checkout path, used only by the [b] "new branch" shortcut.
func runPickerFor(ctx context.Context, src jiraSource, initialKeys []string, noKeyBranch, cwd string) error {
	m := newPickerModelFor(src, initialKeys, noKeyBranch)
	m.cwd = cwd
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}
