# herdr-jira

## Problem

How might we give someone working in `herdr` the same at-a-glance,
act-without-leaving-the-terminal relationship with their Jira tickets that
`herdr-github` gives them with their PRs — without an LLM in the loop and
without a personal API token they don't have?

## What it is

A herdr plugin with three parts:

- **A sidebar label.** `tick` sets the `$ticket`, `$ticket_status` and
  `$ticket_badge` tokens for each space from its branch's issue.
- **A tabbed popup** (`open`): **Issues** and **Epics**, each one scrolling
  list divided into headed lanes.
- **A "this agent's ticket" action** (`issue`): the branch's ticket, confirmed
  with Jira, straight to its detail screen.

It is a standalone MCP client. On first use it performs OAuth dynamic client
registration against `mcp.atlassian.com` from the compiled binary, stores its
own refresh token under `~/.config/herdr/plugins/config/herdr-jira/`, and
makes narrow JSON-RPC calls (`searchJiraIssuesUsingJql`, `getJiraIssue`,
`getTransitionsForJiraIssue`, `transitionJiraIssue`, `editJiraIssue`). No LLM
is in the path, so there is no token cost regardless of call volume.

Transitions are workflow- and project-specific, so the plugin always calls
`getTransitionsForJiraIssue` before offering a next-state picker; it never
hardcodes a status list.

## Current behaviour

### Lanes and queries

Lane queries use `statusCategory != Done`, and `assignee = currentUser()`
unless noted. They sort by `updated DESC` and are capped at one page of 50; a
lane that hit the cap shows `50+` (Jira reports `isLast: false`; there is no
total count).

| Tab | Lane | Query |
|---|---|---|
| Issues | Assigned to me | `issuetype in standardIssueTypes() AND issuetype != Epic`, plus `issuetype in subTaskIssueTypes()` for sub-tasks (shown indented under their parent) |
| Epics | Assigned to me | `issuetype = Epic` |
| Epics | Starred | `key in (<starred keys>)`, then rows whose status category is Done are dropped |
| both | Search results | remote search scoped to the tab, not Done, any assignee |

`standardIssueTypes()` also returns Epics (Stage 0 finding), hence the
explicit `issuetype != Epic` and a drop of any row above hierarchy level 0.
An epic that is both assigned and starred shows once, under Assigned to me.
A starred key Jira no longer returns is hidden; its star stays in the file.

`buildRows` (`rows.go`) is a pure function from tab, fetched data, filter and
search hits to a flat list of rows (heading, issue, sub-task, epic, note). It
does no I/O, so lane order, de-duplication, counts and filtering are tested
without a terminal. `listview.go` owns the cursor (which skips headings),
scrolling and mouse hit-testing.

### Search

`/` enters search mode. Typing filters the lanes locally at once; after 300 ms
without a keystroke one remote search runs, scoped to the tab. A newer query
makes older results stale and they are dropped. A key-shaped query matches on
key prefix (`key ~ "PROJ-12*"`); anything else on summary
(`summary ~ "text*"`). Hits appear in a Search results lane below the lanes,
never repeating a row already shown, and are never written to the on-disk
cache. `Esc` clears the search and restores the cursor.

### Keys

One table (`hotkeys.go`) drives the key handlers, the footer legend, the `?`
list and the README key table, so they cannot disagree. There are two input
modes: in browse mode letters are commands, in search mode they type. `Esc` is
the only back key.

### Branch ticket detection

A generic key pattern finds candidates in the branch name; one batched
`key in (...)` lookup keeps only the ones Jira knows, and results are cached
per key (`keys.json`, per cloud ID). A non-empty `key_pattern` in config is
used as given with no Jira check.

### Stars

`s` stars an epic. Stars are local plugin state in `starred_epics.json`, grouped
by cloud ID, and never written to Jira. The pre-0.4 shape (a top-level `epics`
list) is read as the current site's and moved under it on the first write.

### Sign-in and sites

One Jira Cloud site per account. No site, several sites, a non-Cloud URL, a
sign-in blocked by organisation policy, and a renamed MCP tool each get a
distinct message.

### Distribution

A release workflow builds macOS and Linux binaries (arm64 and amd64) and
publishes them with `checksums.txt`. `scripts/build.sh` builds from source if
Go is present, otherwise downloads the binary and installs it only if its
SHA-256 matches.

## Decision log

Statements below replace earlier ones in this document's history.

- **Superseded: starred Done epics stay visible.** The Epics tab now shows only
  epics that are not Done; a starred epic that finishes disappears.
- **Superseded: `s` is unbound on owned epics.** `s` works on any epic row.
  Ownership no longer implies "already pinned": an assigned-and-starred epic
  just shows once.
- **Superseded: search is unscoped.** Search is scoped to the tab's types and
  excludes Done items, so it never contradicts the lanes.
- **Superseded: the split epics view.** Your epics and starred epics were two
  stacked lists with `Tab` moving focus; they are now two lanes in one list.
  `e`, `Ctrl+f` and `Tab` as list focus are removed with no alias.
- **Superseded: a hardcoded `TICKET-` branch pattern.** An empty `key_pattern`
  now means "auto".
- **`b` no longer means "back".** `Esc` is the only back key, which removes
  the old clash with "branch".
- **Custom row model over `bubbles/list`.** The library counts headings as
  items and has no mouse hit-testing; a pure row builder is simpler to test.
  The epic children screen is the one remaining `bubbles/list`.

## Not doing (and why)

- **Config-based auth-method swap.** Dropped once the MCP-as-standalone-client
  design proved to have no token-cost penalty.
- **Full issue CRUD (comments, field edits).** This is a status-and-transition
  tool first; broader editing dilutes the glance-and-act value.
- **Shared public Atlassian OAuth app.** An Atlassian Developer Console
  registration and maintenance commitment that should not block personal use.
- **Push notifications (webhook relay).** Needs hosting outside the plugin's
  poll/tick model.
- **Jira Server / Data Center, multiple sites, multiple accounts.** The
  plugin signs in through the Atlassian MCP server, which is Cloud only.
- **Starring non-epic issues; Jira's own stars and filters.**
- **Configurable JQL, lanes, icons or hotkeys.** Added only if someone asks.

## Open questions

- **Multi-site accounts.** Today a clear error names the limitation; a lane
  per site or a site picker would be the next step.
- **Mouse in a herdr popup.** Mouse reporting is enabled, but whether herdr
  passes mouse events to a popup process is unverified; the keyboard path does
  not depend on it.
- **Sign-in from another organisation.** Whether admin policy can block the
  OAuth flow is handled with a distinct message but unverified end to end.

## Spike results

See [`stage0-findings.md`](stage0-findings.md) for the recorded results of the
Jira calls, JQL functions and paging behaviour this design relies on. The
design that produced the tabs and lanes is in
[`designs/generic-jira-cloud-tabs.md`](designs/generic-jira-cloud-tabs.md).
