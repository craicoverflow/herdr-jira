# herdr-jira for everyone: tabs, lanes and zero-config Jira Cloud

*   **Author:** Enda Phelan (craicoverflow)
*   **Status:** Draft
*   **Date:** 2026-11-04

--------------------------------------------------------------------------------

## Overview

herdr-jira is a plugin for the herdr terminal workspace. It opens a popup that lists your Jira issues, lets you change an issue's status, assign it to yourself or branch from it, and puts the ticket key and status of the current branch in the herdr sidebar. It talks to Jira through the Atlassian MCP server (`mcp.atlassian.com`) with its own OAuth sign-in. No LLM is in the path.

This design makes the plugin usable by any Jira Cloud user who installs it, and replaces the popup's layout. The new layout has two horizontal tabs, **Issues** and **Epics**, in the style of herdr-recap and herdr-linear. Each tab is one scrolling list divided into headed lanes (section headings with counts).

## The Problem

The plugin was written around one person's setup. Three things stop other people from using it, and one thing makes the popup harder to use than it needs to be.

1. **Hardcoded to one workflow.**
    *   The branch-to-ticket pattern defaults to `TICKET-`.
    *   The queries exclude or include epics by the name "Epic".
    *   Docs, tests and examples use the author's project names (173 mentions of `TICKET` across 17 files).
2. **Install and sign-in are untested on anyone else.**
    *   Install builds from source and needs Go 1.26.8+. There are no prebuilt binaries.
    *   Each install registers its own OAuth client with `mcp.atlassian.com`. That has been tried with one user on one site.
    *   An organisation can block third-party MCP clients, and a person with several Atlassian sites gets one chosen for them.
3. **Epics are split across two screens.** Your epics and your starred epics sit in two stacked lists. `Tab` moves focus between them. Finding and starring a team epic needs `Ctrl+f`, then `Tab`, then `s`. A user has to remember a hidden mode to do something common.
4. **The popup looks plain and has no mouse support.** The code has almost no styling and no mouse handling.

The proposed solution:

*   Replace the hardcoded names with defaults that the plugin works out from Jira, plus a config override.
*   Ship prebuilt binaries.
*   Rebuild the popup as two tabs with headed lanes and a footer showing the keys.
*   Add mouse support and a consistent colour and icon set.

## Goals

*   **Works for a stranger on Jira Cloud.** A person with a single-site Jira Cloud account installs the plugin, signs in and sees sensible lists without editing config. This is unconfirmed until the sign-in test from a second organisation has been run.
*   **Issues tab.**
    *   Issues assigned to you that are not in the Done status category.
    *   Standard issue types (bugs, stories, tasks).
    *   Sub-tasks assigned to you, shown indented under their parent.
    *   No epics and no initiatives.
*   **Epics tab.**
    *   Epics that are not Done.
    *   First lane: epics assigned to you ("Assigned to me").
    *   Second lane: epics you have starred.
    *   An epic that is both appears once, under "Assigned to me".
    *   The not-Done rule applies to starred epics, so a starred epic that finishes disappears.
*   **Tabs and lanes.**
    *   Two horizontal tabs.
    *   One scrolling list per tab.
    *   Headed lanes with counts.
    *   The cursor never lands on a heading.
*   **Starring.** `s` stars or unstars the epic under the cursor. It does nothing on issue rows. Stars stay local to the plugin and are not written to Jira.
*   **Merged search.** Search results appear in the same list, replacing today's separate `Ctrl+f` mode. Placement and de-duplication are defined in the Technical Plan.
*   **Detection with override.** Issue types, the branch key pattern and the Jira site come from Jira. The order of precedence is config, then detected value, then shipped default.
*   **Installable without Go.** Prebuilt binaries for the supported platforms, verified by checksum.
*   **Mouse support.** Click a tab, click a row, scroll with the wheel.
*   **Colour and icons.** Colours come from the Jira status category. Icons are plain Unicode glyphs with a fallback.
*   **One hotkey table.** The same table drives the key handlers and the footer legend, so they cannot disagree.
*   **No author-specific content.** Defaults, tests and docs contain no `TICKET-` prefix or private site names. The README states that the plugin writes to Jira (status transitions, assigning to you).

## Non-Goals

*   Jira Server and Jira Data Center.
*   Accounts with more than one Atlassian site. They are not supported for now.
*   Multiple Jira accounts.
*   Starring issues that are not epics. The Issues tab has no starred lane.
*   Configurable JQL, lane layout, icons or hotkeys. These are added only when someone asks.
*   Creating issues, comments, webhooks or other Jira operations beyond what the plugin does today.
*   Jira's own stars and filters.
*   Keeping the old hotkeys (`e`, `Ctrl+f`, `Tab` as list focus) or rescuing a copied `TICKET-` pattern in a user's config. Both break outright.

## Open Questions

*   **Multi-site accounts.** What does a person with several Atlassian sites see? The working assumption is a clear error that names the limitation, not a silent pick of the first site.
*   **Auto-detection is unverified.** No code in the repo calls a tool that returns issue-type hierarchy or the project list. The Stage 0 spikes must show which Jira calls and JQL functions work on a company-managed and a team-managed site before the detection design is final.
*   **Sign-in for other organisations.** It is unknown whether a colleague at another organisation can complete the OAuth flow, or whether admin policy blocks it.
*   **Mouse in a herdr popup.** It is unknown whether herdr passes mouse events to a popup process.

## Technical Plan (Architecture)

### Decisions already made

| Question | Decision |
|---|---|
| How are lanes drawn and navigated? | A custom flattened-row model. A pure function builds a flat list of rows. Our own code handles cursor, scroll, filter and mouse. |
| How is the ticket key found in a branch name? | A generic key regex finds candidates. One batched Jira lookup keeps only the ones Jira knows. Results are cached per key. |
| How are issues, sub-tasks and epics told apart? | JQL type functions: `standardIssueTypes()`, `subTaskIssueTypes()` and the Epic type. No metadata call. |
| What does search do to the list? | A "Search results" lane is added below the locally filtered lanes. |

### Data flow

```text
 herdr action ──► picker process (Bubble Tea)
                       │
        ┌──────────────┼───────────────────────────┐
        ▼              ▼                           ▼
  jiraSource       star state                 hotkey table
  (source.go)      (starstate.go)             (one source of truth)
  3 lane queries   local JSON, epics only     drives handlers AND footer
        │              │
        └──────┬───────┘
               ▼
        buildRows(tab, data, filter, searchHits)      <- pure function
               │
               ▼
        []row  { heading | issue | subtask | epic }
               │
               ▼
        listView: cursor, scroll, mouse hit-test, render

 herdr event ──► tick (labels.go) ──► branch → candidate keys
                                      ──► cache hit?  yes: use it
                                                      no: key in (...) lookup
                                      ──► set $ticket tokens
```

### Lanes and queries

The lane queries use `statusCategory != Done` and `assignee = currentUser()` unless noted. The Starred lookup and the branch-key lookup (`search(keys)`) carry no status clause; a Done branch ticket must still be confirmed, and Done starred rows are dropped in `buildRows`. Results sort by `updated DESC`.

| Tab | Lane | Query | Notes |
|---|---|---|---|
| Issues | Assigned to me | `issuetype in standardIssueTypes()` | Issue rows. |
| Issues | (same lane) | `issuetype in subTaskIssueTypes()` | Sub-task rows, grouped under their parent by the `parent` field. |
| Epics | Assigned to me | `issuetype = Epic` | Epic rows. |
| Epics | Starred | `key in (<starred keys>)`, then drop rows whose status category is Done | One batched lookup. Chunked when the user has many stars. |
| both | Search results | Remote search, scoped to the tab | See Search. |

*   An epic that is both assigned and starred is removed from the Starred lane inside `buildRows`, so it shows once, under "Assigned to me".
*   A starred key that Jira no longer returns (deleted or no access) is hidden. The old behaviour showed a cached last-known status. The new not-Done rule has no status to test, so the row cannot be shown honestly. The key stays in the star file.
*   Lanes are capped at one page of 50. A lane that hit the cap shows `50+` in its heading, not `50`.
*   **Stage 0 result (see `docs/stage0-findings.md`):** `standardIssueTypes()` does *not* exclude Epic. The Issues query is `issuetype in standardIssueTypes() AND issuetype != Epic`, and `runSearch` also drops rows whose `issuetype.hierarchyLevel` is above 0. Paging uses `isLast` and `nextPageToken`, so the `50+` rule uses `isLast == false`. `key in (...)` returns no error for unknown keys, so no batch splitting.
*   (Original assumption, now disproved on one site:) `issuetype in standardIssueTypes()` is expected to exclude Epic and Sub-task. This is unverified. The Stage 0 spike must confirm it on a company-managed and a team-managed site. If it fails, the fallback is a name-based exclusion with a config override, and the doc must say that the lists are then not zero-config on sites with renamed types. The Epics tab uses the literal type name `Epic`, so a site that renames its Epic type shows no epics. This is a known limitation, documented in the README.
*   Search requests `parent` and `issuetype` in the fields list. Today the type is requested but never decoded.

### Row model

A row is one of four kinds. `buildRows` takes the tab, the fetched data, the filter text and the search hits, and returns an ordered slice. It does no I/O and no rendering, so lane order, de-duplication, counts and filtering can be unit-tested without a terminal.

| Kind | Selectable | Shows |
|---|---|---|
| heading | No | Lane name and count. |
| issue | Yes | Key, summary, status. Status colour comes from the status category. |
| subtask | Yes | Same as issue, indented one level under its parent. |
| epic | Yes | Key, summary, status. A star mark if starred. |

*   The cursor skips headings. Down from the last row of a lane moves to the first row of the next lane.
*   A lane with no matching rows gets no heading when a filter is active. With no filter, an empty lane shows its heading with a count of 0 and one dim line saying why it is empty.
*   The local filter matches key and summary and applies to every lane. Counts show the filtered number.
*   A sub-task whose parent is not in the lane is shown at the top level, after the other top-level issues. The parent is not fetched or shown.
*   The Issues lane count is issues plus sub-tasks displayed. Status change, assign and branch act on the sub-task row itself.

### Search

*   `/` enters search mode. Letters type into the query. `Esc` clears it and returns to the lanes, and the cursor goes back to the row it was on.
*   A query filters the lanes locally at once. After a 300 ms pause with no keystroke it also runs one remote search. A newer query makes older results stale, and stale results are dropped.
*   The remote search is scoped to the tab: standard issues and sub-tasks on Issues, epics on Epics. A query shaped like a key (`PROJ-12`) matches on key prefix. Any other text matches on summary.
*   Remote hits go in a "Search results" lane below the other lanes. A hit already shown in another lane is not repeated. Hits in the Done status category are excluded, so the list never contradicts the not-Done rule. This is a change: today's search is deliberately unscoped and can return Done items. It also means a Done epic cannot be found and starred, which is consistent with a starred Done epic being hidden.
*   Search results are kept in memory only. They are never written to the on-disk "your issues" cache.
*   The two existing search code paths (issues and epics) collapse into one that takes an optional type clause.

### Keys, input modes and mouse

There are two input modes, shown in the footer. In *browse* mode letters are commands. In *search* mode letters type. `s` is therefore a star command in browse mode and a letter in search mode, which removes the old "Tab to focus the list" step.

| Key (browse mode) | Action |
|---|---|
| `Tab` / `Shift+Tab`, `1`, `2` | Next tab, previous tab, jump to Issues or Epics. |
| `↑` `↓`, `j` `k` | Move the cursor. Headings are skipped. |
| `Enter` | Open the detail screen. On an epic, open its children; `i` there opens the epic's own detail. |
| `/` | Enter search mode. |
| `s` | Star or unstar the epic under the cursor. No effect on issue rows. |
| `t` | Transition. Choices come from a fresh Jira call, as today. |
| `a` | Assign to me. |
| `b` | New branch from the focused issue. |
| `c` / `o` | Copy the key / open the issue in the browser. |
| `r` | Refresh the current tab. |
| `Esc` | Back one screen, or clear search. |
| `q` | Close the popup. |
| `?` | Show the full key list. |

*   `b` no longer means "back". `Esc` is the only back key. This removes the old clash between back and branch.
*   `e`, `Ctrl+f` and `Tab` as list focus are removed with no replacement. `Ctrl+f` is not kept as an alias for `/`.
*   One table of key, mode, action and label drives both the key handlers and the footer legend. The README key list is generated or checked against it.
*   Mouse: click a tab to switch, click a row to select it, click a selected row to open it, wheel to scroll. Clicks on headings do nothing. The program starts with `tea.WithMouseCellMotion()`. Whether herdr passes mouse events to a popup is unverified and is checked in Stage 0.

### Colour and icons

*   Colour is chosen by status category (to do, in progress, done), which every search result already carries.
*   Icons are plain Unicode glyphs: a name-to-glyph table for the common issue types and one generic glyph for any other type. There is no Nerd Font requirement. A no-colour terminal gets glyphs and text only.

### Detection, config and site

*   **Branch key.** The generic pattern `(?i)\b([A-Z][A-Z0-9]+-[0-9]+)\b` finds candidates in the branch name. Unseen candidates go in one `key in (...)` lookup. Jira is expected to omit keys it does not know, so false matches such as `pr-4567` are dropped. The Stage 0 spike checks this; if an unknown project makes the query fail, the batch is split and each key is retried alone. Both found and not-found results are cached per key with a TTL. The lookup runs inside `tick` under the existing cooldown. When signed out, `tick` stays quiet and keeps the last label, as today.
*   **Config precedence.** Config, then detected value, then shipped default. An empty `key_pattern` now means "auto". A non-empty value is used as given, with no Jira check. `label_refresh_seconds` is unchanged. No new config keys are added.
*   **Site.** The plugin uses the single accessible Jira site. A person with no accessible site gets a clear message. What a multi-site user sees is an open question.
*   **Errors.** A sign-in blocked by organisation policy, a Data Center URL, and an unexpected tool name each get a distinct message, not a generic 403.
*   **State.** Both the key-lookup cache and the star file are stored per cloud ID. The key-lookup cache is discarded if the signed-in site changes. Stars for another site stay in the file and are not shown while a different site is signed in.

### Distribution

*   A release workflow builds binaries for the supported platforms and publishes them with a `checksums.txt`.
*   `scripts/build.sh` uses Go if it is installed. Otherwise it downloads the binary for the platform and installs it only if its SHA-256 matches. This is how herdr-recap does it.
*   Platforms: macOS and Linux, each on arm64 and amd64. That matches the manifest's `platforms` and the existing `platform_darwin.go` and `platform_linux.go`. Windows is out of scope.

### Compatibility

*   `starred_epics.json` changes shape: stars are grouped under the cloud ID of the site they belong to (`{"sites": {"<cloudId>": {"epics": [...]}}}`).
    *   The new binary reads the old shape (a top-level `epics` list) and, on the first write, moves those stars under the current cloud ID. The old list is removed from the file once moved.
    *   An old binary reading a new file finds no top-level `epics` and shows no stars. It must not crash, because Go's JSON decoding ignores unknown fields. This is checked by a test. Stars are not lost, because they stay in the file until the new binary writes again.
    *   Starring is epics only, so the file holds only epic keys.
*   Old hotkeys and a copied `TICKET-` config value break outright, as decided.

### Staging

Each stage leaves the plugin releasable.

1.  **Stage 0, spikes (no UI code).**
    *   Run `tools/list` and record the tool names.
    *   Confirm `standardIssueTypes()` and `subTaskIssueTypes()` on a company-managed and a team-managed site.
    *   Check paging, and whether a total count is returned.
    *   Check whether a second site appears in the accessible-resources call.
    *   Sign in as a person from another organisation.
    *   Check whether mouse events reach a herdr popup.
    *   Count Jira calls per popup load.
    *   Save recorded responses under `testdata/`.
2.  **Stage 1, installable by anyone.** Branch-key lookup and its cache, the picker's candidate-key entry point, JQL type functions and the new `ticketStatus` fields (the `jiraSource` signatures do not change), cloud ID kept across token refresh, sign-in and site errors, neutral fixtures and comments, the release workflow and the binary-downloading build. Stage 1 starts when every Stage 0 outcome except mouse is PASS.
3.  **Stage 2, tabs and lanes.** The `jiraSource` signature changes with `tui.go` and the fake, `buildRows`, the list view, the tab bar, the Epics tab with both lanes, `s`, the star-file migration, the hotkey table and footer, mouse, colour and icons. The old search is removed here and restored in Stage 3.
4.  **Stage 3, merged search.** One search path and the Results lane.
5.  **Stage 4, docs.** README (with the generated key table) and `docs/SPEC.md`.

## Alternatives Considered

### Alternative A: Ship everything in one release

*   **Pros**: One announcement. No half-finished layouts in between.
*   **Cons**: Only a small part of the work (no hardcoded prefix, verified sign-in, an install path) decides whether a stranger gets a working plugin. The layout rewrite touches roughly 70 to 80 percent of the picker and breaks about eighteen tests. Bundling them delays the first.
*   **Reason for Rejection**: Staged releases let installability ship before the rewrite is done. The staging section above keeps every stage releasable.

### Alternative B: Header rows inside one `bubbles/list` model

*   **Pros**: Less code. Scrolling and paging come from the library.
*   **Cons**: The library's filter and pagination count headings as items, so counts and empty states need patching. It has no mouse support and does not expose where rows are drawn, so click handling needs our own arithmetic anyway.
*   **Reason for Rejection**: The patching and the hand-written hit-testing remove most of the saving. The author chose a custom row model, whose row builder is a pure function that can be tested without a terminal.

### Alternative C: One `bubbles/list` per lane in a shared viewport

*   **Pros**: Closest to today's code. Each lane keeps its own filter.
*   **Cons**: Moving the cursor across lists, sharing one scroll position and feeding one filter box to every list all need glue code. That recreates the "two screens" problem the author wants removed.
*   **Reason for Rejection**: It does not deliver one continuous list without as much custom code as the chosen model.

### Alternative D: Detect all project keys and build the branch regex from them

*   **Pros**: Matches only real project prefixes. No lookup for a branch that has no match.
*   **Cons**: Needs a Jira tool that lists projects. The repo calls none, and it is unverified that one exists. A large organisation can have hundreds of visible projects, giving a long pattern that can still match text that is not a ticket. The sidebar `tick` runs on every herdr event, so a cold cache leaves the label blank until a fetch finishes.
*   **Reason for Rejection**: Checking candidates against Jira removes false matches without needing a project list.

### Alternative E: A generic key regex with no Jira check

*   **Pros**: No network call, no cache, the simplest code.
*   **Cons**: PR numbers, step counters and version tags in branch names match. The repo's own audit found this, and it is why the default was narrowed to `TICKET-`. The sidebar would show a status for a ticket that does not exist, or a wrong one.
*   **Reason for Rejection**: The false-match problem was already observed.

### Alternative F: Read the hierarchy level from Jira metadata

*   **Pros**: Handles renamed and localised types, and sites with levels above Epic.
*   **Cons**: Needs a Jira tool that returns hierarchy level. None is called today and none is confirmed. It adds a metadata call per site and a cache that goes stale when an admin changes types.
*   **Reason for Rejection**: It may not be buildable. The JQL type functions need no extra call. If the Stage 0 spike shows they fail, this option is the next one to try.

### Alternative G: Name-based type defaults with config

*   **Pros**: Simple, and works on any site with the usual names.
*   **Cons**: A site that renames or localises Epic or Sub-task gets wrong lists until the user edits config, which breaks the zero-config goal. Initiatives would appear in the Issues list.
*   **Reason for Rejection**: It fails the zero-config goal on the sites the generic plugin is for. It stays as the fallback if the type functions do not behave as expected.

### Alternative H: Replace the lanes with search results while typing

*   **Pros**: One rule, and no mixing of local and remote rows.
*   **Cons**: The lanes disappear while the remote answer is awaited, leaving a gap. A user who wanted to narrow their own list sees only remote results that may not include their row yet.
*   **Reason for Rejection**: The author chose a separate Results lane that keeps the filtered lanes visible.

### Alternative I: Local filter only, with no remote search

*   **Pros**: No network call. It deletes both existing search code paths.
*   **Cons**: An epic that is not assigned to you cannot be found, so a team epic cannot be starred. That contradicts the goal of replacing `Ctrl+f`.
*   **Reason for Rejection**: It removes a capability the plugin has today.

### Alternative J: Keep separate Epics and Starred panes with `Tab` focus

*   **Pros**: No rewrite of the epics view.
*   **Cons**: One list is split across two screens, and starring needs a hidden mode.
*   **Reason for Rejection**: Rejected by the author.

### Alternative K: Show starred epics regardless of status

*   **Pros**: You see at a glance when something you track finishes. This is today's behaviour.
*   **Cons**: It conflicts with the author's rule that the Epics tab shows only epics that are not Done.
*   **Reason for Rejection**: Rejected by the author. A starred epic that finishes disappears with no notice.

### Alternative L: Show only "In Progress" items

*   **Pros**: A shorter list.
*   **Cons**: A new epic you own that is still To Do would not show.
*   **Reason for Rejection**: The author chose "not Done", which matches current behaviour.

### Alternative M: A vertical sidebar of tabs

*   **Pros**: Room for more tabs.
*   **Cons**: It takes width from a popup, and neither herdr-recap nor herdr-linear uses one.
*   **Reason for Rejection**: The author meant horizontal tabs with lanes as headed sections.

### Alternative N: Support Jira Server and Data Center

*   **Pros**: More users.
*   **Cons**: The plugin signs in through the Atlassian MCP server, which is Cloud only.
*   **Reason for Rejection**: Rejected by the author.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Every file below is listed with what changes. No production code is written during design. The names in this plan are final; tests reference them.

Order of work follows the Staging section: Stage 0 first, and the later stages only after its findings are recorded. Each stage ends with `go build ./...` and `go test ./...` passing.

### Stage 0: spikes

#### 1. `docs/stage0-findings.md` (Create)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: Record the result of each spike under its own `##` heading, with the date, the Jira site type (company-managed or team-managed), the raw tool output where it matters, and a line `Result: PASS` or `Result: FAIL`.
    *   Tool names from `tools/list`, and the exact error code and text returned for an unknown tool name.
    *   Whether `standardIssueTypes()` and `subTaskIssueTypes()` exclude Epic and Sub-task as assumed.
    *   Paging behaviour of `searchJiraIssuesUsingJql`, and whether a total-count or next-page field exists. If one does, it replaces the "result count equals 50" rule in item 14.
    *   Whether `key in (PR-4567, REAL-1)` returns only `REAL-1`, or returns an error when a project does not exist. If it errors, `lookupKeys` splits the batch and retries each key alone, and `keycache` records the failing key as `missing`.
    *   Whether `key in (...)` accepts `statusCategory != Done` (informational; the plan does not depend on it).
    *   Whether a second site appears in the accessible-resources call.
    *   Result of signing in from another organisation, and the exact error if it is blocked.
    *   Whether mouse events reach a herdr popup.
    *   Jira calls per popup load, counting the `initialize` plus `tools/call` pair that `callTool` makes each time.
*   **Gate**: Stage 1 starts when every Stage 0 outcome except mouse is `PASS`. A mouse `FAIL` removes only the mouse work (item 33, mouse sub-item, and its tests). Any other failed assumption changes the plan: the author updates the Technical Plan in the PR that records the failure. If the type functions fail, switch to Alternative G with a config override before Stage 1.
*   **Finding fixtures to rename**: `grep -rnw 'TICKET' --include='*.go' --include='*.json' --include='*.md' . | grep -v HERDR_JIRA_TICKET | grep -v '^./docs/designs/'`. Items below refer to this command instead of counts.

#### 2. `testdata/stage0/*.json` (Create)

*   **Type / Scope**: Test fixtures.
*   **Proposed Changes**: Recorded responses from the spikes, with real keys, names and the real site name replaced by neutral ones (prefix `PROJ`). Before commit, grep the directory for `TICKET` and the real site name; neither may appear. The source-layer tests decode these so detection logic is tested without the network.

### Stage 1: installable by anyone

At the end of Stage 1 the `jiraSource` interface keeps its current signatures. `tui.go` changes only at the picker entry points (item 12).

#### 3. `config.go` (Modify)

*   **Type / Scope**: Go source.
*   **Proposed Changes**:
    *   Remove `defaultKeyPattern` and its `TICKET-` comment. `withConfigDefaults` no longer fills `KeyPattern`; empty means "auto".
    *   Add `genericKeyPattern`, the shipped candidate pattern `(?i)\b([A-Z][A-Z0-9]+-[0-9]+)\b`, with a comment that its matches are only candidates until Jira confirms them.
    *   Keep `LabelRefreshSeconds` and its default of 60. Add no new keys. Replace the `TICKET` text in other comments in this file.

#### 4. `config_test.go` (Create)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: Empty `key_pattern` stays empty after defaults. A set value is kept as given. A missing file and a malformed file both return defaults. `label_refresh_seconds` of 0 or below becomes 60.

#### 5. `config.example.json` (Modify)

*   **Type / Scope**: Example config.
*   **Proposed Changes**: Remove the `TICKET-` pattern. Show `key_pattern` as optional and unset, with `label_refresh_seconds` at 60.

#### 6. `branch.go` (Modify)

*   **Type / Scope**: Go source.
*   **Proposed Changes**:
    *   Add `candidateKeys(branch string) []string`. It runs the generic pattern, finds all matches, upper-cases them and removes duplicates, keeping order of first appearance.
    *   `extractKey` stays for a user-set pattern. An invalid user pattern behaves as it does now: `extractKey` returns no key.
    *   `resolveTicketKey` becomes `resolveTicketKeys(cwd, cfg) (keys []string, userPattern bool)`. With a user pattern it returns at most one key and `userPattern` is true, meaning "use it as given". Without one it returns the candidates in branch-name order, which the caller must confirm with Jira.
    *   `createBranch` and the other branch helpers are unchanged.
    *   Remove the `TICKET` comment in this file.

#### 7. `branch_test.go` (Modify)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: Replace the `TICKET` prefix in fixtures with `PROJ`. Add cases for `candidateKeys`: bare key, key with suffix, key after a path segment, two keys in one branch, lower-case input, `pr-4567`, `step-2-of-9`, and no match.

#### 8. `keycache.go` (Create)

*   **Type / Scope**: Go source and state file.
*   **Proposed Changes**:
    *   A cache file `keys.json` in the directory returned by `stateDir()` (`config.go`).
    *   `keys.json` answers one question: is this candidate a real key? Fields: the cloud ID it was filled for, and per key a result (`found` or `missing`) and the time it was recorded. It stores no summary or status. `labelState.Cache` stays and caches the status of confirmed keys.
    *   `lookupKeys(ctx, src, candidates) (confirmed []string, statuses map[string]ticketStatus, err error)` reads the cache first and sends only unknown or expired candidates to `src.search` in one batch. It returns the statuses it fetched so `tick` can seed `labelState.Cache` with them (one round trip for a new key, not two).
    *   TTLs (constants): `found` entries 1 hour (`foundKeyTTL`), `missing` entries 10 minutes (`missingKeyTTL`), so a typo'd branch name does not trigger a lookup on every event. Entries older than 24 hours are removed on write. `--force` bypasses `keys.json`.
    *   If the TTL has passed and the lookup fails because the user is offline, the old answer is kept, as the label cache does.
    *   If the cached cloud ID differs from the current one, the file is discarded.
    *   Writes are atomic (temp file then rename), like `starstate.go`. `tick` is serialised by its lock (`tick.lock`). The popup's `lookupKeys` call runs outside that lock, so the two can write at the same time; last writer wins, and a lost entry costs one extra lookup.
    *   A signed-out error leaves the cache untouched and is returned to the caller as a quiet error.
    *   If the Stage 0 spike shows that an unknown project makes `key in (...)` fail, a failed batch is split and each key is retried alone.

#### 9. `keycache_test.go` (Create)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: A fake source counts calls. Cases: first sight causes one batched call, second sight causes none. A missing key is cached and not re-asked within its TTL. An expired entry is re-asked. An expired entry with the source offline keeps the old answer. A different cloud ID discards the file. A signed-out error returns without writing. A corrupt file is treated as empty. `force` bypasses the cache. A failed batch is split and retried per key (if Stage 0 requires it).

#### 10. `labels.go` (Modify)

*   **Type / Scope**: Go source.
*   **Proposed Changes**:
    *   In `tick`, replace the direct `resolveTicketKey` call with `resolveTicketKeys` and, when `userPattern` is false, `lookupKeys`. The first confirmed key in branch order sets the tokens. The statuses returned by `lookupKeys` seed `labelState.Cache`; the existing TTL, `force` and pruning rules then apply to confirmed keys.
    *   Errors from `lookupKeys` pass through `isQuietTickErr`, like the existing search path.
    *   If nothing is confirmed, clear the tokens, as for a branch with no key.
    *   The existing cooldown and "stay quiet when signed out" behaviour stay.
    *   Batch the candidates of all worktrees in one tick into one `lookupKeys` call, not one per worktree.

#### 11. `labels_test.go`, `labels_cooldown_test.go`, `labelstate_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: Replace the `TICKET` prefix in fixtures with `PROJ`. Add cases: a branch with `pr-4567` shows no label because the lookup drops it, a user pattern skips the lookup, two worktrees in one tick cost one `lookupKeys` call, and `tick` + `lookupKeys` + `labelState` together (fake source) give the right tokens, including a Done ticket that is still confirmed.

#### 12. `main.go`, `tui.go`, `tui_issue_test.go` (Modify)

*   **Type / Scope**: Go source and unit test. `tui.go` changes only at the picker entry points in this stage.
*   **Proposed Changes**:
    *   `issuePopupEnv` keeps its `(cwd, cfg)` signature and never calls Jira. With a user pattern it sets `HERDR_JIRA_TICKET_KEY` to the single key. Otherwise it sets `HERDR_JIRA_TICKET_CANDIDATES` (comma-joined, in branch order; standard Jira keys contain no commas) and always sets `HERDR_JIRA_NO_KEY_BRANCH` to the branch name. `HERDR_JIRA_TICKET_KEY` keeps its name; it is now set only for a user pattern and is not a rename target.
    *   `runPickerFor` becomes `runPickerFor(ctx, src, initialKeys []string, noKeyBranch, cwd string)`. The `picker` command is its only caller and reads the new variable. `newPickerModelFor`, `checkSignInThenStart` and the test helper in `tui_issue_test.go` take `initialKeys`. `runPicker` is not a caller and is unchanged.
    *   On start the picker calls `lookupKeys(ctx, src, initialKeys)` under a 5-second timeout. It opens the first confirmed key in branch order. If none is confirmed, or the lookup fails or times out, it opens the list with the `noKeyBranch` notice.
    *   `main.go` usage text needs no change.

#### 13. `main_test.go` (Modify)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: Replace the `TICKET` prefix in fixtures. Cover `issuePopupEnv` for both the candidate path (candidates and `HERDR_JIRA_NO_KEY_BRANCH` set, no `HERDR_JIRA_TICKET_KEY`) and the user-pattern path. Add a test that the picker reads `HERDR_JIRA_TICKET_CANDIDATES` and, with a fake source, opens the first confirmed key, and shows the `noKeyBranch` notice when none is confirmed.

#### 14. `source.go` (Modify)

*   **Type / Scope**: Go source. In Stage 1 the `jiraSource` signatures do not change.
*   **Proposed Changes**:
    *   Add `IssueType` and `ParentKey` to `ticketStatus` (today it has Key, Summary, StatusName and StatusCategory). Add `issuetype` and `parent` to `searchFields` and decode both in `runSearch`.
    *   Replace `myIssuesJQL` with the two Issues-lane queries from the Technical Plan (`standardIssueTypes()` and `subTaskIssueTypes()`), keeping `statusCategory != Done` and `ORDER BY updated DESC`. In Stage 1 `myIssues` returns the union of both queries as one slice, sub-tasks carrying `ParentKey`; the old picker renders them flat.
    *   Keep `myEpicsJQL` as `issuetype = Epic`.
    *   `search(keys)` sends at most 50 keys per call (constant `keySearchBatch`) and sets `maxResults` to the batch size. It never carries a status clause, because it serves `lookupKeys`, `tick` and the Starred lane, and a Done branch ticket must still be confirmed.
    *   Fix the comment that says `defaultKeyPattern` is in `branch.go`, and replace the `TICKET` text in comments.
    *   `epicChildren` and `get` are unchanged.
    *   The `runSearch` return value change, the `searchScoped` merge, the removal of `searchText` and `searchEpics`, and the signature changes listed in item 33 land in Stage 2, in the same change as `tui.go` and `fakeJiraSource`.

#### 15. `source_test.go` and `testdata/search_response.json` (Modify)

*   **Type / Scope**: Unit test and fixture.
*   **Proposed Changes**: Replace the `TICKET` prefix in fixtures. Add decoding tests using the Stage 0 fixtures for the new fields, sub-task parents, chunked key batches, the union `myIssues`, and the team-managed-only fixture. Cover the JQL strings sent by the lane queries. Tests for the `more` flag and `searchScoped` belong to item 34 (Stage 2).

#### 16. `mcp.go`, `auth.go`, `callback.go`, `login.go` (Modify)

*   **Type / Scope**: Go source.
*   **Proposed Changes**:
    *   Cloud ID survives refresh. `refreshAndSave` (`mcp.go`) builds fresh credentials through `tokenResponseToCredentials` (`auth.go`), which drops `CloudID`, `SiteURL` and `AccountID`, so they are empty after every access-token refresh. `refreshAndSave` copies those three fields from the locked credentials into the refreshed ones. `signIn` (`login.go`) calls `ensureCloudID` after `saveCredentials`, so a no-site or multi-site error shows at login.
    *   Consent refusal. Map a callback with `error=access_denied` (`callback.go`, `waitForCallback`) and a 403 from `registerClientAt` (`auth.go`) when the User-Agent header is set to `errConsentBlocked`, message: "Sign-in was refused. Your organisation may block third-party MCP clients; ask your Jira admin." It is distinct from `errSignedOut`.
    *   Missing tool. `rawMCPCall` returns a plain error for JSON-RPC errors today. Add a typed `mcpRPCError{Code, Message}` and map the error recorded in Stage 0 (code or text, also `isError: true` text from `checkToolResult`) to `errToolMissing`, whose message names the tool and says the Atlassian MCP server may have changed. There is no `tools/list` check on each call, because `callTool` already costs two round trips.
    *   `errConsentBlocked` and `errToolMissing` are sentinel values (`errors.New`; `errToolMissing` is wrapped with `%w` to carry the name).
    *   The `userAgent` constant (`auth.go`) already exists and is tested. Read the plugin version once and use it in `userAgent` and in `clientInfo.version` (`mcp.go`), which both hard-code `0.1` today.

#### 17. `mcp_test.go`, `auth_test.go`, `login` tests (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: A refresh keeps `CloudID`, `SiteURL` and `AccountID`. The blocked-consent message (`errors.Is(err, errConsentBlocked)`) for the callback error and for the 403 case. The missing-tool message (`errors.Is(err, errToolMissing)`). `signIn` surfaces a no-site error.

#### 18. `status.go` (Modify)

*   **Type / Scope**: Go source.
*   **Proposed Changes**:
    *   `resolveCloudID` de-duplicates the accessible-resources list by `id` (the same site can appear once per scope bundle), then returns `errNoSite` for zero sites, `errMultipleSites{names}` for more than one, and `errNotCloud` when the single site URL does not end in `.atlassian.net`, tested on `url.Parse(u).Hostname()` with `strings.HasSuffix`. Messages: "No Jira Cloud site is available to this account.", "This account has access to several Jira sites (<names>). Only one is supported.", "This site is not Jira Cloud."
    *   The multi-site error stands until the multi-site Open Question is answered; this item is the only place that changes.
    *   `ensureCloudID` resolves once and stores the ID in `credentials.json` (item 16 keeps it across refreshes). Empty means unresolved.
    *   `status` prints the effective settings: signed-in user (from `fetchUserInfo`), site, and whether the branch key pattern is a user value or "auto".

#### 19. `status_test.go` (Create)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: The same site listed twice resolves to one site. Zero sites, two sites and a non-Cloud URL give the three errors. A URL with a trailing slash is accepted.

#### 20. `scripts/build.sh` (Modify)

*   **Type / Scope**: Shell script.
*   **Proposed Changes**:
    *   If Go is installed, build from source as today, with the same flags as the release build (`-trimpath -ldflags "-s -w"`).
    *   Otherwise: read `version` from `herdr-plugin.toml` with `sed -n 's/^version *= *"\(.*\)"/\1/p' herdr-plugin.toml | head -1` (anchored, because `min_herdr_version` also contains the word). The tag is `v<version>`. Detect OS and architecture with `uname -s -m` (`x86_64` to `amd64`, `aarch64` to `arm64`). Download `https://github.com/craicoverflow/herdr-jira/releases/download/v<version>/herdr-jira-<os>-<arch>` and `checksums.txt` (`<hash>  <filename>` format) with `curl -fsSL` into a `mktemp` file. Verify with `shasum -a 256` if present, otherwise `sha256sum`. Only then `mv` it over the binary path. A `trap` removes the temp file. Any mismatch or unsupported platform fails with a message and installs nothing.
    *   Remove the "Go is required" text and reword the "no prebuilt releases yet" comment.

#### 21. `.github/workflows/release.yml` (Create)

*   **Type / Scope**: GitHub Actions workflow.
*   **Proposed Changes**: Trigger `push: tags: ['v*']` on `ubuntu-latest`, with `permissions: contents: write`. A first step fails if the tag minus `v` differs from `version` in `herdr-plugin.toml`. Use `actions/setup-go` with `go-version-file: go.mod`. Build `herdr-jira` for darwin and linux on arm64 and amd64 with `-trimpath -ldflags "-s -w"`, name the files `herdr-jira-<os>-<arch>`, write `checksums.txt`, and run `gh release create "$TAG" herdr-jira-* checksums.txt`.

#### 22. `herdr-plugin.toml` (Modify)

*   **Type / Scope**: Plugin manifest.
*   **Proposed Changes**: Update the plugin and `open` action descriptions, which mention "plus your epics with drill-down". The `issue` action description changes from "The ticket keyed in the current branch name" to say it also confirms the key with Jira. Set `version` to `0.2.0` for the first generic release.

### Stage 2: tabs and lanes

Stage 2 removes `Ctrl+f` and the search phases as a deliberate step back; Stage 3 restores search in the merged form. Stage 2 is not released on its own.

#### 23. `rows.go` (Create)

*   **Type / Scope**: Go source, pure functions.
*   **Proposed Changes**: These types, and `buildRows(t tab, d laneData, filter string, hits searchHits) []row`, where `tab` is an int enum (`tabIssues`, `tabEpics`):
    ```go
    type rowKind int // rowHeading, rowIssue, rowSubtask, rowEpic
    type row struct {
        Kind  rowKind
        Issue ticketStatus // zero for headings
        Depth int          // 0, or 1 for a sub-task under its parent
        Lane  string       // heading text without the count
        Count int          // headings only
        More  bool         // headings only: lane hit its page cap, render "50+"
    }
    type laneData struct {
        Issues, Subtasks, Epics []ticketStatus
        Starred     []ticketStatus  // as returned by Jira; may include Done; buildRows drops Done
        StarredKeys map[string]bool // all starred keys, including ones Jira did not return
        IssuesMore, SubtasksMore, EpicsMore bool
    }
    type searchHits []ticketStatus
    ```
    `buildRows` does no I/O. Ordered steps:
    1.  Drop Done rows from `Starred`.
    2.  Epics tab: `assigned = Epics`; `starred = Starred` minus keys in `assigned`. De-duplication happens here, not before the call.
    3.  Issues tab: `top = Issues`; each sub-task is grouped under its parent in `top`, in `updated` order.
    4.  A sub-task whose parent is not in the lane is placed at depth 0, after all top-level issues, in its own query's `updated` order.
    5.  Apply the filter to key and summary. Under a filter a sub-task is shown if it matches; its parent is shown at depth 0 only if the parent matches. Lanes with zero rows get no heading.
    6.  Search hits minus keys already in any lane become the Results lane, placed last. It shows only while the query is non-empty and a response for the current `seq` has arrived.
    7.  Headings carry the count. The Issues count is issues plus sub-tasks displayed. `50+` is shown when the lane's `More` flag is set and no filter is active.
    *   Empty-lane lines: Issues "No open issues assigned to you.", Epics "No epics assigned to you.", Starred "No starred epics. Press s on an epic to star it.", Results "No matches."

#### 24. `rows_test.go` (Create)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: Cover lane order (Results last), an epic that is assigned and starred, a starred Done epic, a starred key Jira did not return, sub-task grouping, an orphan sub-task (position after top-level issues), a matching sub-task under a non-matching parent, an empty lane with and without a filter (asserting the strings above), counts under a filter, the `50+` heading, and search hits that duplicate lane rows.

#### 25. `listview.go` (Create)

*   **Type / Scope**: Go source.
*   **Proposed Changes**:
    *   Holds the row slice, cursor index, scroll offset and window height. Window height comes from `tea.WindowSizeMsg`. The list gets `Height - 2` lines (tab bar and footer), or `Height - 3` in search mode (the search box takes one line). The `?` list replaces the list area.
    *   `moveCursor(delta)` skips headings in both directions and stops at the ends. With zero selectable rows the cursor index is -1 and `Enter`, `s` and `t` do nothing.
    *   `scrollToCursor()` keeps the cursor on screen. When the cursor is on the first selectable row after a heading and the offset is below the heading index, set the offset to the heading index. After a refresh the cursor stays on the same key, not the same index.
    *   `render(width)` draws rows with indentation for sub-tasks and a pointer mark on the selected row.
    *   `hitTest(y)` returns the row under a line relative to the list's first line, or none for a heading or blank line. The caller passes `msg.Y - 1`, because `tea.MouseMsg.Y` is absolute and the list starts on line 1.
    *   Wheel scrolling moves the offset by 3 lines without moving the cursor off screen.

#### 26. `listview_test.go` (Create)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: Cursor skipping across headings, cursor at both ends, zero selectable rows, scroll follows cursor, heading stays visible above the first row after it, cursor stays on a key after rows change, `hitTest` on rows, headings and below the last row, wheel step, search-mode height, and rendering fits the window width and height.

#### 27. `hotkeys.go` (Create)

*   **Type / Scope**: Go source.
*   **Proposed Changes**:
    *   `type hotkey struct { Key string; Mode inputMode; Action string; Label string; Rows []rowKind }` and one table of entries. `tui.go` looks keys up here. The footer is built from the same table for the current mode and row. At widths under 60 the footer truncates to the first entries that fit, followed by `? more`. A function renders the full `?` list.
    *   `var handlers = map[string]func(*pickerModel, row) tea.Cmd` maps each action name to its handler.
    *   Search-mode keys: printable runes type, Backspace deletes, up and down move the cursor, Enter opens the selected row, Esc clears and returns to browse.
    *   `hotkeyMarkdown() string` dumps the browse-mode table as Markdown for the README (item 36).
    *   The Stage 2 table omits `/`; item 35 adds it with its handler.

#### 28. `hotkeys_test.go` (Create)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: No key is bound twice in one mode. Every action in the table has a non-nil entry in `handlers`. The footer for a mode lists only keys valid for the focused row (for example `s` shows only on an epic). The narrow-width footer rule. The README comparison test is added in item 36.

#### 29. `theme.go` (Create)

*   **Type / Scope**: Go source.
*   **Proposed Changes**: lipgloss styles keyed by status category, the glyph table for issue types with one generic fallback, the heading and tab styles, and a plain mode used when the terminal has no colour.

#### 30. `theme_test.go` (Create)

*   **Type / Scope**: Unit test.
*   **Proposed Changes**: Every status category has a style. An unknown issue type gets the generic glyph. Plain mode produces no escape sequences.

#### 31. `starstate.go`, `starstate_test.go` (Modify)

*   **Type / Scope**: Go source and unit test.
*   **Proposed Changes**:
    *   New shape: `{"sites": {"<cloudId>": {"epics": [{"key": "...", "summary": "..."}]}}}`. The old `Status` field is dropped, because status comes from Jira on each load. Migration copies the summary from the old `Status.Summary`.
    *   `readStars(cloudID)` and `writeStars(cloudID, keys)` take a cloud ID from `ensureCloudID` (the stored ID, or one network lookup). A missing-credentials error maps to `errSignedOut`.
    *   Write is read-modify-write on the whole file: read, replace the entry for this cloud ID, keep every other site's entry, write atomically. The file has no lock; two popups starring at once is last-writer-wins, accepted.
    *   Migration: an old-shape file (top-level `epics`) is read as belonging to the current cloud ID. This can attribute another site's stars to the current one if the user switched sites before this version; Jira returns nothing for keys it does not know, so the effect is that those stars show nothing. Accepted. The next write moves them to the new shape. If both a top-level `epics` and `sites` exist, `sites[cloudID]` wins and the top-level list is dropped on the next write.
    *   Downgrade: an old binary that stars or unstars after migration overwrites the file with `{"epics": [...]}` and loses `sites`. Accepted and noted in the README.
    *   Add a function that returns the current site's starred keys. Item 14's `search(keys)` does the chunking.
    *   Replace the `TICKET` prefix in fixtures.
    *   Tests: an old-shape file loads and migrates on write, both keys present, a new-shape file written for site A shows nothing for site B and keeps site A's stars, a Done starred epic stays in the file, and a file written by the new binary decodes into the old struct with no error and no stars.

#### 32. `cache.go`, `tui_test.go` (Modify)

*   **Type / Scope**: Go source and unit test.
*   **Proposed Changes**: `writePickerCache` writes the Issues-lane `Issues` followed by `Subtasks`, never epics or search hits. The file is a bare `[]ticketStatus` array. Old files lack `IssueType` and `ParentKey`; on load, rows with `ParentKey != ""` go to `Subtasks` and the rest to `Issues`, and a row with an empty `IssueType` is treated as a normal issue, so old caches show sub-tasks at top level until the first refresh. Tests in `tui_test.go`: a cache written while search hits are present contains none of them; an old-shape cache decodes and renders.

#### 33. `tui.go`, `source.go` (Modify, large)

*   **Type / Scope**: Go source. Split into sub-items; name functions, not line ranges.
*   **Proposed Changes**:
    *   **33a. Interface and source.** Change `jiraSource` as follows, and update `fakeJiraSource` and every caller (`tick` is a caller of `search`, which stays a map):
        ```go
        type searchScope int // scopeIssues, scopeEpics
        type jiraSource interface {
            search(ctx, keys []string) (map[string]ticketStatus, error) // unchanged
            myIssues(ctx) (issues, subtasks []ticketStatus, issuesMore, subtasksMore bool, err error)
            myEpics(ctx) (epics []ticketStatus, more bool, err error)
            searchScoped(ctx, query string, scope searchScope) ([]ticketStatus, error)
            // unchanged: get (returns issueDetail), transitions, transition, assign, accountID, epicChildren(ctx, epicKey)
        }
        ```
        `runSearch` gains a `more bool` return. Page size is 50 for the lane queries and `searchScoped`; `searchTextMaxResults` (20) is deleted. A result is truncated when it returns 50 rows (an exact-50 false positive is accepted), unless Stage 0 found a total or next-page field. The sub-task query has its own cap and `SubtasksMore` flag; the Issues heading shows `50+` if either query was truncated. Lane queries carry `statusCategory != Done` and `assignee = currentUser()`. `searchScoped` carries `statusCategory != Done` and no assignee clause. Merge `searchText` and `searchEpics` into `searchScoped`. Key-shaped queries (matching `^[A-Za-z][A-Za-z0-9]+-[0-9]+$` or a partial key) use today's prefix search, `key ~ "KEY*" AND ... ORDER BY key ASC`. Other text uses `summary ~ "q*"`, escaped with `escapeJQL`. Type clause: Issues scope `(issuetype in standardIssueTypes() OR issuetype in subTaskIssueTypes())`; Epics scope `issuetype = Epic`.
    *   **33b. Model.** Replace the seven `list.Model` fields and the `epicsOnStarred` and `epicSearchOnList` flags with `tab`, `view listview` and `mode` (browse, search). The detail, transition and branch fields stay.
    *   **33c. Messages.** Keep `issuesLoadedMsg`. Delete `epicSearchDebounceFireMsg`, `epicSearchResultsMsg`, `epicSearchSeq`, `debounceEpicSearchKeystroke`, `runSearchEpics`, their handlers, `phaseEpics`, `phaseEpicSearch`, `phaseSearch`, `focusedEpicList`, and the `e` and `Ctrl+f` handlers. `debounceFireMsg` and `searchResultsMsg` are kept for item 35.
    *   **33d. `Update`.** Routes keys through `hotkeys.go`. `yankSelectedKey` and `openSelectedInBrowser` change from `*list.Model` to take the selected `ticketStatus` from `listview`.
    *   **33e. `View`.** Tab bar (one line), list, footer.
    *   **33f. Epic children.** Enter on an epic opens a children screen that reuses one `list.Model` (the only one left). In it, `i` opens the epic's own detail, Enter on a child opens its detail, `t`, `a` and `b` act on the highlighted child, and `Esc` returns to the Epics tab with the cursor on that epic.
    *   **33g. Mouse.** Pass `tea.WithMouseCellMotion()` in both `runPicker` and `runPickerFor` (both use `tea.WithAltScreen()` only today). A click on line 0 picks the tab by the x range of each label. A click on a row selects it (`hitTest(msg.Y - 1)`); a second click on the already selected row opens it (no timer). The wheel moves the offset by 3 lines. Clicks on headings do nothing.
    *   **33h. Tab load and stars.** Issues loads one query set; Epics loads the assigned query and the starred batch. A tab loads when first shown; `r` refreshes the current tab. `s` toggles a star only on an epic row; after starring, the star file is written and the lane is rebuilt. `b` starts a branch only; `Esc` is the only back key.
    *   Keep the detail, transition and branch wizard functions and the clipboard and browser helpers. Show an empty-state line for an empty lane and an error line with `[r]` to retry. Replace any remaining `TICKET` mention (use the grep in item 1).

#### 34. `tui_test.go`, `tui_issue_test.go`, `tui_branch_test.go`, `source_test.go` (Modify)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**:
    *   Delete the tests that encode the split and phase model. They include `EKeyLoadsEpicsThenDrillsIntoChildrenThenAssigns`, `TabTogglesFocusBetweenOwnedAndStarredEpicLists`, `CtrlFEntersEpicSearch`, `SOnOwnedListIsANoop`, `StarFromSearchResultsAddsToStarredList` and the epic-search debounce tests.
    *   Keep and adapt: load on success, error and retry, enter opens detail, transitions and failed transitions, stale-result dropping, debounce, `q` while filtering, and the branch wizard tests.
    *   Add: tab switching by key and click, `s` works on epics and does nothing on issues, `Esc` returns to browse with the cursor restored, `b` does not go back, the old keys do nothing, epic children navigation and `i`. In `source_test.go`: the `more` flag, `searchScoped` JQL for both scopes and both query shapes, and sub-task cap handling.
    *   Keep the `fakeJiraSource` seam and give it every method of the interface in item 33a.
    *   Replace the `TICKET` prefix in fixtures.

### Stage 3: merged search

#### 35. `tui.go`, `rows.go`, `hotkeys.go` (Modify)

*   **Type / Scope**: Go source and unit tests.
*   **Proposed Changes**: Add `searchState` and the `/` binding (with its `handlers` entry). Reuse `searchDebounce` (300 ms). Merge `searchSeq` and the removed epic counter into one `searchSeq`. A keystroke increments it and schedules `debounceFireMsg{seq, query}`. When it fires with the current `seq`, the query runs through `searchScoped`; the `searchResultsMsg` response carries `seq` and a `scope` field, and an old `seq` is dropped. Every non-empty query triggers a remote search. Hits go to `buildRows` as `searchHits` and are never passed to `writePickerCache`. On a search error, show a one-line error under the Results heading and keep the lanes. Tests: the Results lane appears after the debounce, a stale response is dropped, search mode types letters including `s`, a search error keeps the lanes.

### Stage 4: docs

#### 36. `README.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: Rewrite the feature list, install section (binary download, Go optional), first-use sign-in (including the blocked-by-admin case and the single-site limit), key table between `<!-- hotkeys:start -->` and `<!-- hotkeys:end -->` markers (generated from `hotkeys.go`), configuration table (only `key_pattern` and `label_refresh_seconds`), a note that the plugin writes to Jira (transitions and assigning), a note that a site with a renamed Epic type shows no epics, and the downgrade note from item 31. Replace the `TICKET` text and remove the stale "MVP implemented" line. Document the removed hotkeys. Add the README comparison test to `hotkeys_test.go`: the text between the markers equals `hotkeyMarkdown()`.

#### 37. `docs/SPEC.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: Replace the dated changelog layers with current-state sections. Add a short decision log. Mark each as superseded, one bullet per statement: starred Done epics stay visible; `s` is unbound on owned epics; search is unscoped; the split epics view. The "Not Doing" entry on a shared OAuth app stays as it is. Replace the `TICKET` text and remove the stale Open Questions.

### Verification Plan

*   **Compilation/Build**: `go build ./...`, then `sh scripts/build.sh` once with Go on the path and once with Go hidden from the path to exercise the download path. The download path can only be verified after the `v0.2.0` tag build exists. Change one byte of the downloaded binary and confirm `build.sh` exits non-zero and installs nothing.
*   **Unit Tests**: `go test ./...`. The new logic files (`rows.go`, `listview.go`, `keycache.go`, `hotkeys.go`) have their own test files as listed above.
*   **Static Analysis & Linting**: `go vet ./...` and `gofmt -l .` with no output.
*   **Test Coverage Requirement**: Run `go test -coverprofile=c.out ./...` and `go tool cover -func=c.out`. In `rows.go`, `listview.go`, `keycache.go` and `hotkeys.go`, every function is at least 80 percent and the mean of the file's function percentages is at least 90 percent. All files are `package main`, so `-cover` alone reports only per package. `tui.go` has no percentage target; every behaviour listed in item 34 has a test.
*   **E2E/Integration**: A manual run on a second Jira Cloud account with a single site, in a fresh herdr install.
    *   Sign in with no config file present. `status` prints the user, site and "auto".
    *   Issues tab has no epics and shows sub-tasks under parents.
    *   Epics tab shows assigned then starred, with an assigned and starred epic shown once.
    *   `s` stars an epic and does nothing on an issue.
    *   A branch named `PROJ-123-foo` shows its label, and `pr-4567-fix` shows none.
    *   Search shows a Results lane, and Esc restores the lanes with the cursor in place.
    *   Click a tab and a row (skipped if Stage 0 found mouse events do not reach the popup). Scroll with the wheel. Old keys do nothing.
    *   Install on a machine with no Go toolchain.
*   **Acceptance scenarios** (from the brief): empty Issues tab shows a message, no epics shows a message, a team-managed-only account (Stage 0 fixture test in item 15), a site with a renamed Epic type shows no epics (known limitation, documented in the README), a signed-out `tick` stays quiet, an old-shape `starred_epics.json` loads and migrates, and a new-shape file does not crash an old binary.

--------------------------------------------------------------------------------

<!-- Provenance: Indicates initial structural drafting via EGM. -->

*Drafted with EGM*
