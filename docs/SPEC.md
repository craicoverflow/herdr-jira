# herdr-jira

## Problem Statement

How might we give someone working in `herdr` the same at-a-glance,
act-without-leaving-the-terminal relationship with their Jira tickets that
`herdr-github` gives them with their PRs — without an LLM in the loop and
without a personal API token they don't have?

## Recommended Direction

Port the `herdr-github` shape onto Jira: a `tick`-driven sidebar label (per
pane/agent, showing the linked ticket's status), a `herdr-recap`-style popup
for the full issue list, and a "this agent's ticket" action resolved from the
current branch name (`TICKET-1234` pattern), falling back to a manual picker
when no key is found.

The auth path is the one real architectural decision: `herdr-jira` is its own
standalone MCP client, not something routed through Claude or any other LLM.
On first use it performs OAuth dynamic client registration against
`mcp.atlassian.com` directly from the compiled binary, stores its own refresh
token in `~/.config/herdr/plugins/config/herdr-jira/`, and from then on makes
narrow, direct JSON-RPC calls (`searchJiraIssuesUsingJql`, `getJiraIssue`,
`getTransitionsForJiraIssue`, `transitionJiraIssue`) on `tick` and on action
invocation. No LLM is ever in this path, so there's no token cost regardless
of call volume — a config-level auth-method feature flag was considered and
dropped once this architecture made the original "wastes tokens" concern moot.

One piece of API discipline worth calling out: transitions are workflow- and
project-specific. The plugin must always call `getTransitionsForJiraIssue`
before offering a next-state picker — it can't hardcode a status list, or it
will offer transitions that don't exist on a given board.

## Key Assumptions to Validate

- [x] `mcp.atlassian.com` supports OAuth dynamic client registration (DCR)
      for a third-party native client outside Claude Code — confirmed by
      attempting DCR against its `/.well-known/oauth-authorization-server`
      metadata before writing any plugin code.
- [x] A branch name reliably contains a parseable Jira key (e.g. `TICKET-1234`) — confirmed, conditionally: true
      when the key pattern is scoped to known project prefixes rather than
      a bare generic regex (which produces false positives like PR numbers).
- [ ] The MCP tool surface (`searchJiraIssuesUsingJql`,
      `getTransitionsForJiraIssue`, `transitionJiraIssue`, etc.) is stable
      and sufficient for sidebar + popup + transition needs — confirm field
      availability (status, assignee, summary) comes back in a single
      `search` call to avoid N+1 lookups per ticket.

## MVP Scope

**In:** OAuth bootstrap (`herdr-jira login`), `tick` on `pane.focused` /
`pane.agent_status_changed` updating a sidebar label, `open` action
(Recap-style popup, your assigned issues), `issue` action (this agent's
ticket via branch detection, picker fallback), transition picker fetched live
per-ticket via `getTransitionsForJiraIssue`.

**Out (for MVP):** comments, assignment changes, field edits, creating
issues, team/board-wide views, webhook-based push notifications, any
config-level auth-method flag.

## Added Post-MVP, By Request

- **Search any issue, not just yours** (2026-10-22) — the `open` popup's
  `Ctrl+f` mode queries Jira directly instead of only locally filtering
  "your issues." This is a deliberate reach past the MVP's assigned-to-you
  scope; see the README's "Keybindings" section for the key-prefix-vs-text
  search behavior it settles for. A generalized "which screen does `Esc`
  return to" mechanism (`detailReturnPhase`) was introduced to support this
  cleanly, replacing a prior hardcoded assumption that the detail screen's
  back button always meant "your issues."
- **Epics tab, drill-down, and assign-to-me** (2026-10-22) — the `open`
  popup's `[e]` key opens a second list of every epic assigned to you;
  selecting one drills into its children (`parent = <epicKey>`, confirmed
  live); a
  child's detail screen gets a new `[a] assign to me` key alongside `[t]
  transition`. This is a second deliberate reach past the MVP's
  assignment-changes exclusion, reusing `detailReturnPhase` the same way
  the search feature did so `Esc` from a child's detail comes back to the
  children list rather than "your issues."

- **Star/pin team epics you don't own (2026-10-27)**—the epics tab
  (`phaseEpics`) becomes a vertical split: your own epics (`myEpics`, as
  today) on top, and epics you've starred below, each its own
  `list.Model` (`epicList` / `starredEpicList`) with independent sizing
  and scroll state; `tab` moves focus between the two and routes keys to
  whichever is focused. A new `ctrl+f`-triggered search mode, scoped to
  `issuetype = Epic` and reusing the existing key-prefix-vs-text query
  split from the issue search (see "Search any issue, not just yours"
  above), is how you find an epic you don't own in the first place—it
  queries Jira live rather than filtering `myEpics`, since by definition
  these epics aren't in that list. `s` toggles star: pressing it on a row
  in the search results adds the epic's key to a local starred set and
  persists it immediately; pressing it on a row in the starred list
  (bottom) removes it. `s` is not bound on the owned list (top)—those
  epics are already "pinned" by ownership, so toggling there would be
  meaningless. Starring is **local-only plugin state**, not a Jira-side
  mutation (no watcher, flag, or label is written to the issue)—it lives
  in a new `starred_epics.json` state file following the existing
  `labelstate.go` pattern (atomic temp-file-then-rename write,
  best-effort read). On every epics-tab load the starred keys are
  resolved to fresh `ticketStatus` via the existing batched
  `jiraSource.search` call (no new epic-specific fetch method needed),
  the same way sidebar ticks batch lookups. `enter` on a starred row
  drills into its children exactly like an owned epic does today
  (`epicChildren`, `detailReturnPhase` back to the starred list). A
  starred epic that's later deleted or made inaccessible in Jira keeps
  showing with its last-known cached status rather than disappearing or
  erroring out the whole batch—confirmed live against
  `acme.atlassian.net`: `key in (<real>, <bogus>)` simply omits the
  bogus key from its results instead of failing the call, so one bad key
  can't blank the whole starred list, and `starred_epics.json` is never
  mutated by a failed or partial lookup (only an explicit unstar removes
  an entry). The two open design questions this raised were settled, not
  left open: the epic search stays a bare, unscoped query (no
  project-prefix narrowing like branch detection uses), and a starred
  epic stays in the list regardless of status rather than dropping out on
  Done—its current status (`Done`, `In Progress`, …) is always shown next
  to its key, since that status line is already `issueItem`'s default
  rendering and needed no extra work.

## Not Doing (and Why)

- **Config-based auth-method swap (API token / direct OAuth as
  alternatives)** — dropped once the MCP-as-standalone-client design proved
  to have no token-cost penalty; a second auth path would be speculative
  complexity for a problem that no longer exists.
- **Full issue CRUD (comments, assignment, field edits)** — this is a
  status-and-transition tool first; broader editing is a distinct, larger
  surface that dilutes the "glance and act" value prop herdr-github
  established.
- **Shared public Atlassian OAuth app for frictionless team install** — real
  for a "shareable plugin" goal, but it's an Atlassian Developer Console
  registration/maintenance commitment that shouldn't block getting personal
  use working first.
- **Push notifications on ticket changes (webhook relay)** — interesting but
  requires hosting infrastructure outside the plugin's existing poll/tick
  model; out of scope until the pull-based version proves useful.

## Open Questions

- Does `mcp.atlassian.com` actually support DCR for a non-Claude native
  client, or does its OAuth flow assume a Claude-brokered session? This needs
  to be tested before any implementation time is spent.
- What's the actual branch-naming convention in use — is `TICKET-1234`-style
  prefixing consistent enough to make branch detection the primary (not just
  fallback) path?

## Phase 0 Spike Results

All three assumptions above are confirmed:

- [x] DCR, PKCE, refresh rotation, and direct `tools/call` all work against
      `mcp.atlassian.com` for a native client (Task 0a). One operational
      gotcha: Atlassian's Cloudflare edge blocks default HTTP client
      User-Agents — the plugin must set a browser-like UA on every request.
- [x] Branch detection works as the primary path, conditional on scoping
      the key pattern to known project prefixes (`TICKET`) rather than a
      bare generic regex, to avoid false positives like PR numbers.
- [x] The MCP tool surface returns everything needed (summary, status +
      category, assignee) in one batched `search` call, and transitions are
      confirmed live/project-specific (Task 0c).
