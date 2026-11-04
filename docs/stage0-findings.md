# Stage 0 findings

Spike results for `docs/designs/generic-jira-cloud-tabs.md`. Fixtures are in
`testdata/stage0/`, with the real keys, site name and cloud ID replaced
(prefixes `PROJ` and `TEAM`, site `example`).

Date: 2026-11-04. Run against one Jira Cloud site with two projects through the
plugin's own `callTool`. Read-only queries only. The site is the author's, so
the second-organisation checks could not be run (see the end).

## Tool names (`tools/list`)

31 tools. The ones the plugin relies on are present: `searchJiraIssuesUsingJql`,
`getJiraIssue`, `getTransitionsForJiraIssue`, `transitionJiraIssue`,
`editJiraIssue`, `getAccessibleAtlassianResources`, `atlassianUserInfo`.
Also present, and unused today: `getVisibleJiraProjects`,
`getJiraProjectIssueTypesMetadata`, `getJiraIssueTypeMetaWithFields`.

An unknown tool name does not fail at the JSON-RPC layer. The call succeeds and
returns `{"isError": true, "content": [{"text": "MCP error -32602: Tool noSuchTool not found"}]}`.
`errToolMissing` (design item 16) must match this `isError` text
("-32602" and "not found"), not a JSON-RPC error code. See `unknown_tool.json`.

Result: PASS

## `standardIssueTypes()` and `subTaskIssueTypes()`

- `subTaskIssueTypes()` returns only sub-tasks (`PROJ-3`, "Subtask"). PASS.
- `standardIssueTypes()` **includes Epics**. The result had five Epics
  (`hierarchyLevel: 1`) next to Task and Story. The design assumed it excludes
  them. See `issues_standard.json`.

Result: **FAIL** for the Epic exclusion. Sub-task handling passes.

Consequence for the plan (the Technical Plan note allows this fallback):

- The Issues-lane query becomes
  `issuetype in standardIssueTypes() AND issuetype != Epic`. This still works on
  a site that renames Epic only if the name matches, which the Epics tab already
  requires.
- Better: the search result carries `issuetype.hierarchyLevel` (0 standard,
  1 Epic, sub-tasks have `subtask: true`). `runSearch` should decode
  `hierarchyLevel` and drop any row with a level above 0 from the Issues lane,
  so an Initiative or a renamed Epic never reaches it. The JQL `!= Epic` clause
  stays as a cheap first filter so the 50-row page is not wasted on Epics.
- Alternative G (name-based types with config override) is not needed.

This was seen on a single site. The company-managed versus team-managed
comparison was not possible (see the end), so the result is not yet confirmed
on both.

## Paging

`searchJiraIssuesUsingJql` returns `isLast: true|false` and, when more rows
exist, an opaque `nextPageToken`. There is no total count.
See `paging_page1.json` (`maxResults: 5`, `isLast: false`).

Result: PASS. Item 14 and item 33a should use `isLast == false` for the `more`
flag, not "result count equals 50".

## `key in (...)` with unknown keys

- `key in (PR-4567, ZZZZ-1)` (projects that do not exist) returns
  `{"issues": [], "isLast": true}`. No error.
- `key in (PR-4567, PROJ-1)` returns only `PROJ-1`.
- `key in (PROJ-1) AND statusCategory != Done` is accepted (informational).

Result: PASS. The batch-split fallback in item 8 is not needed. See
`key_in_unknown.json` and `key_in_mixed.json`.

## Accessible resources

Returned one site. Whether a second site appears for a multi-site account could
not be checked. See `accessible_resources.json` for the shape.

Result: not tested (single-site account only).

## Calls per popup load

`callTool` makes `initialize` then `tools/call`: two HTTP round trips per Jira
call. Today's picker load runs the issues query and the epics query, so four
round trips. The new design adds one sub-task query and a batched Starred
lookup: Issues tab 4 round trips, Epics tab 4 round trips, plus 2 for the
key lookup on open when the branch has candidates.

Result: PASS (documented, no change to the plan).

## Not run, needs a person

These need an account or a terminal that this session does not have.

- Sign-in from a person at another organisation, and the exact error if blocked.
- A company-managed versus a team-managed comparison of the type functions.
- A second site in the accessible-resources call.
- Whether mouse events reach a herdr popup.

Result: **OPEN**. The design's gate asks for all of these except mouse before
Stage 1. Stage 1 work can proceed in the meantime because none of it depends on
their outcome, but they must be closed before the `v0.2.0` release.
