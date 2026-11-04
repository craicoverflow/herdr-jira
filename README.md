# herdr-jira

A [herdr](https://herdr.dev) plugin that brings Jira Cloud into your terminal:
see your issues and epics, search, change an issue's status, assign it to
yourself, branch from it, and see the ticket for your current branch in the
herdr sidebar — all without leaving your pane.

Talks to Jira as a standalone MCP client against `mcp.atlassian.com` — no LLM
in the path, so no token cost regardless of call volume. See
[`docs/SPEC.md`](docs/SPEC.md) for the design.

**This plugin writes to Jira.** It changes an issue's status (transitions) and
assigns issues to you, when you ask it to. Starring an epic is local to the
plugin and writes nothing to Jira.

## Features

- **Two tabs, one scrolling list each.** **Issues** and **Epics**, each
  divided into headed lanes with counts.
  - *Issues:* your open issues (bugs, stories, tasks), with your sub-tasks
    indented under their parent. No epics.
  - *Epics:* the epics assigned to you, then the epics you have starred.
    Anything Done is hidden.
- **Search.** `/` filters your lanes as you type, then searches Jira for
  matches in a *Search results* lane below them.
- **Star team epics.** Find an epic you do not own with search, press `s` to
  keep it in your Starred lane.
- **Mouse.** Click a tab or a row (click the selected row again to open it)
  and scroll with the wheel.
- **Sidebar ticket label.** Each pane shows the issue key and live status for
  the branch it is on, confirmed against Jira so a branch like `pr-4567-fix`
  never shows a bogus ticket.
- **Live transitions.** Change status from a freshly fetched list of valid
  transitions — never hardcoded.
- **Branch from a ticket.** Create a branch from the focused issue, with
  prompts to assign it to you and move it to In Progress.

## Install

```sh
herdr plugin install craicoverflow/herdr-jira
```

On install, `scripts/build.sh` builds from source if Go 1.26.8+ is present;
otherwise it downloads the prebuilt binary for your platform from the GitHub
release and installs it only if its SHA-256 matches `checksums.txt`. No Go
toolchain is needed. Supported: macOS and Linux, on arm64 and amd64.

If you already have the repo checked out locally, link it instead and run
`scripts/build.sh` yourself first:

```sh
herdr plugin link /path/to/herdr-jira
```

## First use: signing in

There is no separate login step. The first time you run the `open` or `issue`
action with no stored credentials, the plugin opens your browser for a normal
Atlassian OAuth consent screen, waits for you to approve it, then loads the
popup. A stored refresh token keeps you signed in. `tick` never triggers
sign-in itself, so the sidebar stays quiet until you have signed in once via
the popup.

Things that can go wrong, each with its own message:

- **Blocked by your organisation.** If sign-in is refused, your organisation
  may block third-party MCP clients; ask your Jira admin.
- **One site only.** The account must have exactly one Jira Cloud site.
  Several sites, none, or a non-Cloud (Server / Data Center) URL are reported
  as errors rather than picking one silently.
- **Changed server.** If the Atlassian MCP server renames a tool the plugin
  uses, the error names the tool.

`herdr-jira status` prints the signed-in user, the site, and whether the
branch key pattern is a configured value or "auto".

## The popup

Bind the actions in `~/.config/herdr/config.toml` (see the commented examples
in [`herdr-plugin.toml`](herdr-plugin.toml)):

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

- **`open`** — the tabbed popup. The last Issues list is cached, so it paints
  instantly before refreshing.
- **`issue`** — resolves the current branch to a ticket key, confirms it with
  Jira, and opens straight on its detail screen. No key on the branch, or one
  Jira does not recognise, falls back to the list with a notice.

The popup has two input modes, shown in the footer. In *browse* mode letters
are commands; in *search* mode (after `/`) letters type into the query, so
`s` is the star command in browse mode and just a letter in search mode.

<!-- hotkeys:start -->
| Key | Action |
|---|---|
| `enter` | open the detail screen (an epic opens its children) |
| `/` | search: filters the lanes as you type, then searches Jira |
| `t` | change status (a live list of valid transitions) |
| `a` | assign to me |
| `b` | new branch from the issue |
| `s` | star or unstar the epic (epics only) |
| `c` | copy the issue key |
| `o` | open the issue in the browser |
| `↓` / `j` | move down |
| `↑` / `k` | move up |
| `tab` | next tab |
| `shift+tab` | previous tab |
| `1` | jump to Issues |
| `2` | jump to Epics |
| `r` | refresh the current tab |
| `?` | show all keys |
| `esc` | close the popup (or clear a search) |
| `q` | close the popup |
<!-- hotkeys:end -->

In search mode, `↑` `↓` move the cursor, `enter` opens the row, and `esc`
clears the search and puts the cursor back where it was. `b` is *not* "back":
`esc` is the only back key. `Ctrl+c` always closes the popup.

**Epics.** `enter` on an epic lists its children. There, `enter` opens a
child, `i` opens the epic's own detail, `t` `a` `b` `c` `o` act on the
highlighted child, and `esc` returns to the Epics tab with the cursor on that
epic.

**Search.** Typing filters your lanes at once. After a 300 ms pause it also
runs one search against Jira, scoped to the tab (issues and sub-tasks, or
epics, that are not Done). A key-shaped query (`PROJ-12`) matches on key
prefix; anything else matches on summary. Hits go in a *Search results* lane
below your lanes, without repeating rows you already see.

**Stars.** `s` on an epic stars or unstars it. Stars are kept in
`starred_epics.json` in the plugin's state directory, grouped by Jira site,
and never written to Jira. A starred epic that is also assigned to you shows
once, under *Assigned to me*. A starred epic that is Done, deleted or no
longer visible is hidden (its star stays in the file).

**Upgrading.** The old keys are gone: `e` (epics), `Ctrl+f` (search) and
`Tab` as list focus have no replacement — use the tabs and `/`. A
`key_pattern` copied from an older default (`TICKET-…`) is used as given.
`starred_epics.json` is migrated on first write; an older binary that stars
or unstars afterwards overwrites the file and loses the migrated entries.

## Sidebar label

`tick` is wired to `pane.focused`, `pane.agent_status_changed`, and
worktree/workspace creation in `herdr-plugin.toml`. Add a token to an Agent
sidebar row in `~/.config/herdr/config.toml`:

```toml
[ui.sidebar.agents]
rows = [
  ["state_icon", "machine", "workspace", "tab"],
  ["agent", "$ticket_badge"],
]
```

- `$ticket` — the issue key, e.g. `PROJ-1234`
- `$ticket_status` — the status name, e.g. `In Progress`
- `$ticket_badge` — both together, e.g. `PROJ-1234 In Progress`

A space with no ticket on its branch shows none of these (they are cleared,
not left stale).

## Configuration

Config lives at `~/.config/herdr/plugins/config/herdr-jira/config.json` — see
[`config.example.json`](config.example.json). Both keys are optional.

| Key | Default | What it does |
|---|---|---|
| `key_pattern` | unset (auto) | Leave unset and the plugin finds Jira-shaped keys in the branch name and keeps only those Jira confirms exist. Set a regex to use it as given, with no Jira check. |
| `label_refresh_seconds` | `60` | How old a ticket's cached status may get before a `tick` asks Jira again. |

The lane queries, lane layout, icons and hotkeys are not configurable; a knob
can be added if someone needs one.

## Known limitations

- Jira Cloud only; one Atlassian site per account.
- The Epics tab uses the issue type named `Epic`. A site that renames or
  localises that type shows no epics.
- Each lane shows one page (50). A lane that hit its cap shows `50+`.
- Mouse support needs herdr to pass mouse events to the popup; if it does
  not, the keyboard works as normal.

## Build

```sh
sh scripts/build.sh
```
