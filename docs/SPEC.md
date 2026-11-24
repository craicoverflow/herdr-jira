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

The same binary also serves **the Herdr app mod** (`app/mods/jira`, see
[`app/README.md`](../app/README.md)): a gpui-kit dialog or pane that draws the
same lanes, a detail pane, status changes, assign, stars and Start work, and
asks the binary for everything Jira through `herdr-jira api <op>` (one JSON
object per call). `herdr-jira app install` copies the mod, which is built into
the binary, into the app's folder along with the binary as its helper.

It is a standalone MCP client. On first use it performs OAuth dynamic client
registration against `mcp.atlassian.com` from the compiled binary, stores its
own refresh token under `~/.config/herdr/plugins/config/herdr-jira/`, and
makes narrow JSON-RPC calls (`searchJiraIssuesUsingJql`, `getJiraIssue`,
`getTransitionsForJiraIssue`, `transitionJiraIssue`, `editJiraIssue`). No LLM
is in the path, so there is no token cost regardless of call volume.

Transitions are workflow- and project-specific, so the next-state picker
always offers what `getTransitionsForJiraIssue` returned; it never hardcodes a
status list. The answer is kept for a day per workflow step (project, issue
type, status; `transitions_cache.json`), since it is the same for every issue
at that step, unless a transition has a condition (Jira hides those per
issue). On a miss the issue is read alongside, so a stale status from a list
row can't file the list under the wrong step. An empty list is not kept, a
refused transition drops the project's lists, and refresh refetches.

## Current behaviour

### Views (the app's tabs)

The Herdr app's tabs are `views` in `config.json` (see `app/README.md`,
"Tabs"). Without `views` the app shows My work only. The terminal popup keeps
the fixed Issues and Epics tabs below.

| View | Query | Grouping |
|---|---|---|
| `my-work` | `assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC` (all types, sub-tasks nested under their parent) | status headings: `indeterminate` statuses first, then `new`; one heading per status name |
| `epics` | the Epics rows below | Assigned to me, Starred |
| `jql` | the entry's JQL as written | status headings, or none with `group_by: "none"` |
| `filter` | `filter = <id>` | as `jql` |

Search inside a view runs `(<view JQL without ORDER BY>) AND (summary ~ "q*")`
(`key ~ "Q*"` for key-shaped text), keeping the view's ORDER BY. Each view is
cached on its own, keyed by kind and query, for 30 minutes.
`herdr-jira api view <id|index>` answers one; `api views` lists them.

### Lanes and queries (terminal popup)

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

One Jira Cloud site in use per account, kept as `cloud_id`/`site_url` in the
credentials. An account with one site resolves it by itself; with several, sign-in
reports `multiple_sites` and first-run setup (below) lists them (`api sites`)
and stores the choice (`api site-select <id>`) in that same cache. No site, a
non-Cloud URL, a sign-in blocked by organisation policy, and a renamed MCP tool
each get a distinct message.

### First-run setup (the app)

Offered after sign-in and from `?` then Set up, while `setup.json` in the state
folder says it hasn't been done and `config.json` has neither `views` nor
`default_project`. `session` carries `setup: {needed, defaultProject}`.

| Op | Does |
|---|---|
| `sites` | `getAccessibleAtlassianResources`, de-duplicated; flags `cloud` and `current` |
| `site-select <id>` | stores the site's `cloud_id`/`site_url` (Jira Cloud only) |
| `projects` | as quick-create; its default is ticket's project, last used, `default_project`, first |
| `jql-check --query` | runs the JQL once (one page); `invalid_jql` carries Jira's message, auth and network errors keep their codes |
| `setup-save --json` | validates, merges `default_project` and/or `views` into `config.json`, marks setup done |
| `setup-skip` | marks setup done, writes nothing |

The merge keeps every other key and its value as written (and the order),
refuses a file that isn't one JSON object, writes whole beside the file and
renames over it. `views` is written only if wholly valid, and with My work in
it. The demo uses `<state>/demo/config.json`. Saved filters: the MCP's tools
(testdata/stage0/tools_list.json) have none for listing them, and its token
isn't offered to Jira's REST API, so the tabs step offers a JQL tab instead.

### Distribution

A release workflow builds macOS and Linux binaries (arm64 and amd64) and
publishes them with `checksums.txt`. `scripts/build.sh` builds from source if
Go is present, otherwise downloads the binary and installs it only if its
SHA-256 matches.

### The Herdr app mod

The app runs a mod's programs with a 30-second limit, so `api login` starts a
detached sign-in worker that records its consent URL and outcome in
`login.json`, and the mod polls `api session`. Lists use the plugin's caches
and TTLs (`api issues|epics [--cached|--refresh]`), and
`api comment KEY` posts a comment through `addCommentToJiraIssue`
(`commentBody` as markdown, as the tool's format guide asks); the body comes
from `--json '{"body": "…"}'` or stdin, a blank body is refused, and the reply
carries the re-read issue so the mod shows Jira's own copy. Demo mode appends
it to the pretend site only.
`api transitions KEY --type … --status …` answers from the transitions cache
(`--refresh` skips it). Changes made through the
API (`transition`, `assign`, `star`; `comment` changes only the detail) patch the cached rows in place, keeping
each cache's fetch time, so the next paint is right without extending the TTL.
`api tickets --branch …` replaces `tick` for the app: the same candidate-key
confirmation, with statuses kept in `app_labels.json` so the TUI's `tick`
(which prunes keys its spaces no longer use) and the app never undo each other.
Issue details are fetched with `responseContentFormat: markdown`; Jira still
answers some fields in ADF, which `adf.go` turns into markdown.

Quick create (`n`) uses three ops: `api projects [--ticket KEY] [--cached|--refresh]`
(projects you may create in, from `getVisibleJiraProjects`, plus the default:
the ticket's project, else the last project created in, kept in
`last_project.json`, else the first), `api issuetypes PROJECT` (from
`getJiraProjectIssueTypesMetadata`, sub-types left out) and `api create
--project P --type T --summary S [--description D] [--assign-me]`
(`createJiraIssue`; the description goes as plain text, which the tool reads as
markdown). Projects and issue types are cached six hours each
(`projects_cache.json`, `issuetypes_cache.json`). A create puts the new issue
in the cached lists when it is assigned to the user, and Jira's refusals are
mapped to plain sentences (`create_failed`).

`api --demo` (or a `demo` file beside the helper, written by
`app install --demo`) answers from a pretend site in `demo.go`, in a state
folder of its own, so the mod can be tried and screenshotted without an
account and without writing to anyone's Jira.

## Decision log

Statements below replace earlier ones in this document's history.

- **The app mod reuses this binary rather than re-implementing Jira in
  JavaScript.** A mod can't run the OAuth loopback callback, and one client
  keeps one sign-in, one set of caches and one set of stars for both front ends.
- **No sidebar tokens in the app.** The app's spaces sidebar is Herdr's own
  mod; instead the mod pins the current space's ticket at the top and marks
  every row with the spaces working on it.
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
- **Full issue CRUD (field edits, editing or deleting comments).** This is a
  status-and-transition tool first, plus adding a comment; broader editing
  dilutes the glance-and-act value.
- **Shared public Atlassian OAuth app.** An Atlassian Developer Console
  registration and maintenance commitment that should not block personal use.
- **Push notifications (webhook relay).** Needs hosting outside the plugin's
  poll/tick model.
- **Jira Server / Data Center, multiple sites, multiple accounts.** The
  plugin signs in through the Atlassian MCP server, which is Cloud only.
- **Starring non-epic issues; Jira's own stars and filters.**
- **Configurable lanes, icons or hotkeys.** Added only if someone asks. (Configurable JQL tabs exist in the Herdr app only: see Views.)

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
