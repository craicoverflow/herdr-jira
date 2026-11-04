# herdr-jira

A [herdr](https://herdr.dev) plugin that brings Jira into your terminal: see
your issues, search any ticket, drill into epics, and transition status —
all without leaving your pane.

Talks to Jira as a standalone MCP client against `mcp.atlassian.com` — no LLM
in the path, so no token cost regardless of call volume.

Status: MVP implemented. See [`docs/SPEC.md`](docs/SPEC.md) for the design.

## Features

- **Sidebar ticket label.** Each pane shows the issue key and live status
  for the branch it's on — updates automatically as you work, no manual
  refresh.
- **Your issues, one keystroke away.** A searchable popup lists every issue
  assigned to you, newest-updated first, and paints instantly from cache
  before refreshing.
- **Jump straight to "this branch's ticket."** Resolves the current branch
  to a ticket key and opens right on its detail screen.
- **Search any issue, not just yours.** A live, debounced search against
  Jira itself — type a key fragment for a prefix search, or free text for a
  summary search.
- **Epics with drill-down.** Browse your epics, step into their children,
  and assign a child to yourself without leaving the popup.
- **Live transitions.** Change an issue's status from a freshly fetched
  list of valid transitions — never hardcoded.
- **Branch from a ticket.** Create a branch from the focused issue, with
  prompts to assign it to you and move it to In Progress.
- **Copy a key anywhere.** Grab the focused issue's key to paste elsewhere.

## Install

`herdr plugin install` pulls from `github.com/<owner>/<repo>`:

```sh
herdr plugin install craicoverflow/herdr-jira
```

This clones the repo and runs `scripts/build.sh`, which needs Go 1.26.8+
installed (there are no prebuilt releases yet).

If you already have the repo checked out locally, you can skip the clone
and link it directly instead, though you'll need to run `scripts/build.sh`
yourself first:

```sh
herdr plugin link /path/to/herdr-jira
```

## First use: signing in

There's no separate login step. The first time you run the `open` or
`issue` action and the plugin has no stored credentials, it opens your
browser for a normal Atlassian OAuth consent screen, waits for you to
approve it, and then loads the popup. After that, a stored refresh token
keeps you signed in — `tick` never triggers sign-in itself, so the sidebar
just stays quiet (no label) until you've signed in once via a popup.

`herdr-jira login` runs the same flow directly from the terminal; it's a
debugging entry point, not something you need day to day.

Being signed out produces the same message everywhere it comes up: in the
`tick` log (quiet, once per event, last-known label kept on screen), in the
picker (inline, with a `[r]` to retry), and from `status`.

## Sidebar label

Bind `tick` to pick up ticket status automatically — it's already wired to
fire on `pane.focused`, `pane.agent_status_changed`, and worktree/workspace
creation in `herdr-plugin.toml`. Add the token to an Agent sidebar row in
`~/.config/herdr/config.toml`:

```toml
[ui.sidebar.agents]
rows = [
  ["state_icon", "machine", "workspace", "tab"],
  ["agent", "$ticket_badge"],
]
```

Available tokens:

- `$ticket` — the issue key, e.g. `TICKET-1234`
- `$ticket_status` — the status name, e.g. `In Progress`
- `$ticket_badge` — both together, e.g. `TICKET-1234 In Progress`

A space with no resolvable ticket key on its branch shows none of these
(they're cleared, not left stale).

## Keybindings

Bind the actions in `~/.config/herdr/config.toml` — see the commented
examples in [`herdr-plugin.toml`](herdr-plugin.toml):

```toml
[[keys.command]]
key = "prefix+alt+j"
type = "plugin_action"
command = "herdr-jira.open"

[[keys.command]]
key = "prefix+alt+t"
type = "plugin_action"
command = "herdr-jira.issue"
```

- **`open`** — a searchable popup of every issue assigned to you
  (`assignee = currentUser() AND statusCategory != Done`, newest-updated
  first). The last list is cached, so it paints instantly before
  refreshing.
- **`issue`** — resolves the current branch to a ticket key and opens
  straight on its detail screen. No key on the branch, or a key Jira
  doesn't recognize, falls back to the `open` list with an inline notice
  instead of failing.

In the popup: type to locally filter your issues, `Enter` to open one, `t`
on the detail screen to fetch and choose a live transition (never
hardcoded — always a fresh `getTransitionsForJiraIssue` call), `Esc`/`b` to
go back, `Esc`/`q` to close.

**`Ctrl+f` — search any issue, not just yours.** Typing in the search box
queries Jira itself (debounced ~300ms after the last keystroke), not just
your cached "your issues" list. The query behaves in two modes:

- Type something key-shaped (`TICKET-300`, complete or not) → a prefix
  search on the issue key (`key ~ "TICKET-300*"`), so it narrows toward
  every matching issue (`TICKET-3001`, `TICKET-3008`, …) as you keep
  typing.
- Type anything else → a text search against the issue summary
  (`summary ~ "<text>*"`).

`Esc` clears the search box first, then (on an empty box) returns to "your
issues."

**`e` — your epics, with drill-down and assign-to-me.** Opens a vertical
split of two lists: your own epics on top (`assignee = currentUser() AND
issuetype = Epic AND statusCategory != Done`, newest-updated first), and
epics you've starred below. `Tab` moves focus between the two lists.
`Enter` on an epic (either list) loads its children
(`parent = <epicKey>`); `Enter` on a child opens its detail screen, where
`[a] assign to me` sits next to `[t] transition`. `Esc` steps back one
screen at a time: child detail → children list → epics tab → your issues.

**`Ctrl+f` (in the epics tab)—find and star a team epic you don't own.**
Opens a live search scoped to epics (same key-vs-text query behavior as the
issue search above), so you can find an epic that isn't assigned to
you—a team epic you contribute to but don't own. `Tab` moves focus from the
query box into the results list; `s` there stars (or un-stars, if pressed
again) the selected epic, adding it to the starred list in the epics tab.
`s` only works once `Tab` has moved focus to the list—otherwise it's just
a letter in your search text. Starring is local to this plugin (stored in
`starred_epics.json` next to the rest of its state); it doesn't write
anything to Jira. In the starred list itself, `s` un-stars the selected
epic directly. A starred epic keeps showing regardless of status—its
current status (e.g. `Done`, `In Progress`) is always shown next to its
key, so you can see at a glance when something you're tracking wraps up.

**`c` — copy the focused issue's key.** Works on any issue list (your
issues, epics, starred epics, epic children) and on the detail screen.
Useful for pasting a key elsewhere.

**`o` — open the focused issue in your browser.** Works on any issue list
and on the detail screen. Resolves straight to the issue's Jira page
(`<your site>/browse/<KEY>`) using the site URL discovered during sign-in —
no base URL to configure, it works against whatever Jira site you're
authenticated against.

**`b` — new branch from the focused issue.** Prompts for a suffix (the
issue key is the fixed prefix, e.g. typing `start-spec` for `TICKET-2815`
makes `TICKET-2815-start-spec`) and runs `git checkout -b` in the
space the popup was opened from. If the issue isn't already assigned to
you, or isn't already In Progress (and a transition to it exists), it then
asks — as two separate yes/no questions — whether to assign it to you and
whether to transition it to In Progress.

## Configuration

Config lives at
`~/.config/herdr/plugins/config/herdr-jira/config.json` — see
[`config.example.json`](config.example.json). All keys are optional.

| Key | Default | What it does |
|---|---|---|
| `key_pattern` | `(?i)\b(TICKET-[0-9]+)\b` | The regex used to find a ticket key in a branch name, matched anywhere in the name, case-insensitively. The default is scoped to the `TICKET` project rather than a bare generic key pattern, because a generic `[A-Za-z0-9]+-\d+` regex matches plenty of non-Jira things in real branch names (PR numbers, step counters) — see [`docs/spike-findings.md`](docs/spike-findings.md)'s Task 0b section. Override this for a different project prefix, or to match more than one. |
| `label_refresh_seconds` | `60` | How old a ticket's cached status may get before a `tick` asks Jira about it again. |

The popup's "your issues" JQL
(`assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC`)
is not configurable in this MVP — it's a deliberate choice to keep the
config surface small; a knob can be added if it turns out to matter.

## Build

```sh
sh scripts/build.sh
```
