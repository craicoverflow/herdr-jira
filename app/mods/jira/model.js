// Pure data shaping for the Jira mod: lanes, filtering, names and times.
// No Herdr or gpui imports, so it runs under `node --test` too
// (app/test/model.test.mjs). The lane rules mirror rows.go's buildRows, which
// the terminal popup uses, so both show the same lists.

export const LANE_ASSIGNED = "Assigned to me";
export const LANE_STARRED = "Starred";
export const LANE_RESULTS = "Search results";
export const LANE_CHILDREN = "In this epic";

export const NOTE_NO_ISSUES = "No open issues assigned to you.";
export const NOTE_NO_EPICS = "No epics assigned to you.";
export const NOTE_NO_STARRED = "No starred epics. Press s on an epic to star it.";
export const NOTE_NO_MATCHES = "No matches in Jira.";
export const NOTE_NO_MORE = "Nothing else in Jira.";
export const NOTE_NO_CHILDREN = "This epic has no child issues.";

export const isDone = (i) => i?.category === "done";

export function matches(issue, filter) {
  if (!filter) return true;
  const f = filter.toLowerCase();
  return issue.key.toLowerCase().includes(f) || (issue.summary ?? "").toLowerCase().includes(f);
}

// A row is { kind: "head", lane, count, more } | { kind: "note", text } |
// { kind: "issue" | "subtask" | "epic", issue, depth, starred, lane }.
// Every row has a unique `id` for the list.
//
// tab: "issues" | "epics". data: { issues, subtasks, issuesMore, subtasksMore }
// for issues, { epics, more, starred, starredKeys } for epics (either may be
// null while loading). hits: null (no answer yet) or the search results.
export function buildRows(tab, data, filter = "", hits = null) {
  const rows = [];
  const shown = new Set();
  const note = (text, lane) => rows.push({ kind: "note", text, id: `note:${lane}` });
  const addLane = (lane, body, more, empty) => {
    if (body.length === 0 && filter) return;
    rows.push({ kind: "head", lane, count: body.length, more: Boolean(more) && !filter, id: `head:${lane}` });
    if (body.length === 0) return note(empty, lane);
    for (const r of body) {
      rows.push({ ...r, lane, id: `${lane}:${r.issue.key}` });
      shown.add(r.issue.key);
    }
  };

  if (tab === "issues") {
    if (data) addLane(LANE_ASSIGNED, issueRows(data.issues ?? [], data.subtasks ?? [], filter), data.issuesMore || data.subtasksMore, NOTE_NO_ISSUES);
  } else if (data) {
    const starredKeys = new Set(data.starredKeys ?? []);
    const assigned = new Set((data.epics ?? []).map((e) => e.key));
    const mine = (data.epics ?? []).filter((e) => matches(e, filter))
      .map((e) => ({ kind: "epic", issue: e, depth: 0, starred: starredKeys.has(e.key) }));
    addLane(LANE_ASSIGNED, mine, data.more, NOTE_NO_EPICS);
    const starred = (data.starred ?? [])
      .filter((e) => matches(e, filter) && !isDone(e) && !assigned.has(e.key) && starredKeys.has(e.key))
      .map((e) => ({ kind: "epic", issue: e, depth: 0, starred: true }));
    addLane(LANE_STARRED, starred, false, NOTE_NO_STARRED);
  }

  if (hits) {
    const starredKeys = new Set(data?.starredKeys ?? []);
    const body = hits.filter((h) => !isDone(h) && !shown.has(h.key)).map((h) => {
      const kind = tab === "epics" ? "epic" : h.level < 0 ? "subtask" : "issue";
      return { kind, issue: h, depth: 0, starred: kind === "epic" && starredKeys.has(h.key) };
    });
    // The results lane is Jira's answer to the query, so it keeps its heading
    // even when it is empty.
    rows.push({ kind: "head", lane: LANE_RESULTS, count: body.length, more: false, id: `head:${LANE_RESULTS}` });
    if (body.length === 0) note(hits.some((h) => !isDone(h)) ? NOTE_NO_MORE : NOTE_NO_MATCHES, LANE_RESULTS);
    for (const r of body) rows.push({ ...r, lane: LANE_RESULTS, id: `${LANE_RESULTS}:${r.issue.key}` });
  }
  return rows;
}

// Each top-level issue followed by its sub-tasks (depth 1), then sub-tasks
// whose parent isn't listed, at depth 0.
export function issueRows(issues, subtasks, filter = "") {
  const top = issues.filter((i) => matches(i, filter));
  const subs = subtasks.filter((i) => matches(i, filter));
  const inTop = new Set(top.map((i) => i.key));
  const children = new Map();
  const orphans = [];
  for (const s of subs) {
    if (s.parent && inTop.has(s.parent)) {
      if (!children.has(s.parent)) children.set(s.parent, []);
      children.get(s.parent).push(s);
    } else orphans.push(s);
  }
  const out = [];
  for (const t of top) {
    out.push({ kind: "issue", issue: t, depth: 0 });
    for (const c of children.get(t.key) ?? []) out.push({ kind: "subtask", issue: c, depth: 1 });
  }
  for (const o of orphans) out.push({ kind: "subtask", issue: o, depth: 0 });
  return out;
}

// The drill-down list for one epic: its children, open ones first.
export function childRows(epic, children, filter = "") {
  const kids = (children ?? []).filter((c) => matches(c, filter));
  const order = { indeterminate: 0, new: 1, done: 2 };
  kids.sort((a, b) => (order[a.category] ?? 1) - (order[b.category] ?? 1));
  const lane = LANE_CHILDREN;
  const rows = [{ kind: "head", lane, count: kids.length, more: false, id: `head:${lane}`, epic }];
  if (!kids.length) rows.push({ kind: "note", text: filter ? "No matching children." : NOTE_NO_CHILDREN, id: `note:${lane}` });
  for (const c of kids) rows.push({ kind: c.level < 0 ? "subtask" : "issue", issue: c, depth: 0, lane, id: `${lane}:${c.key}` });
  return rows;
}

export const selectable = (r) => r && (r.kind === "issue" || r.kind === "subtask" || r.kind === "epic");

// The index of the selectable row `step` places from `from` (wrapping), or
// the first selectable row when from is -1.
export function moveSelection(rows, from, step) {
  const idx = rows.map((r, i) => (selectable(r) ? i : -1)).filter((i) => i >= 0);
  if (!idx.length) return -1;
  if (from < 0 || !idx.includes(from)) return step < 0 ? idx[idx.length - 1] : idx[0];
  const at = idx.indexOf(from);
  return idx[(at + step + idx.length) % idx.length];
}

export const countLabel = (count, more) => `${count}${more ? "+" : ""}`;

// "PROJ-12" whole or being typed: a key-prefix search rather than a text one.
export const keyShaped = (q) => /^[A-Za-z][A-Za-z0-9]*-[0-9]+$/.test(q.trim());

// Finds candidate keys in a branch name, as branch.go's candidateKeys does.
export function branchKeys(branch) {
  const out = [];
  for (const m of (branch ?? "").matchAll(/\b([A-Za-z][A-Za-z0-9]+-[0-9]+)\b/g)) {
    const k = m[1].toUpperCase();
    if (!out.includes(k)) out.push(k);
  }
  return out;
}

// The branch the Start work sheet suggests: KEY-short-summary-slug.
export function branchName(key, summary, max = 48) {
  const words = (summary ?? "").toLowerCase().normalize("NFKD").replace(/[̀-ͯ]/g, "")
    .replace(/[^a-z0-9]+/g, " ").trim().split(/\s+/).filter(Boolean);
  let slug = "";
  for (const w of words) {
    const next = slug ? `${slug}-${w}` : w;
    if (next.length > max) break;
    slug = next;
  }
  return slug ? `${key}-${slug}` : key;
}

// A branch name git would accept, roughly (check-ref-format still has the
// final word): no spaces, no "..", no control or special characters.
export function validBranch(name) {
  const n = name.trim();
  if (!n || n.length > 200) return false;
  if (/[\s~^:?*[\\\x00-\x1f\x7f]/.test(n)) return false;
  if (n.includes("..") || n.includes("@{") || n.includes("//")) return false;
  if (n.startsWith("-") || n.startsWith("/") || n.endsWith("/") || n.endsWith(".") || n.endsWith(".lock")) return false;
  return true;
}

// The worktree folder for a branch, as Herdr's worktree mod lays them out.
// Different branches can share one (feat/a and feat-a), and a branch made of
// punctuation can have none: see usableSlug.
export const worktreeSlug = (branch) => branch.replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");

// Whether a slug names a folder of its own: not empty, and not "." or "..".
export const usableSlug = (slug) => slug !== "" && !/^\.+$/.test(slug);

// The folder of the worktree that has branch checked out, from `git worktree
// list --porcelain` (blank-line separated records; detached and bare
// worktrees have no branch line), or null.
export function worktreeForBranch(porcelain, branch) {
  let path = null;
  for (const line of porcelain.split("\n")) {
    if (line.startsWith("worktree ")) path = line.slice(9);
    else if (line === "") path = null;
    else if (line === `branch refs/heads/${branch}` && path) return path;
  }
  return null;
}

// A generation counter per name, for dropping replies that went stale on
// their way: take a stamp before a request, bump the name when something
// supersedes it, and check the stamp when the reply comes.
export class Generations {
  constructor() { this.n = new Map(); }
  bump(name) { this.n.set(name, this.stamp(name) + 1); }
  stamp(name) { return this.n.get(name) ?? 0; }
  same(name, stamp) { return this.stamp(name) === stamp; }
}

// The first message an agent gets when it starts on an issue.
export function agentTask(issue) {
  const lines = [`Work on Jira issue ${issue.key}: ${issue.summary}`];
  if (issue.url) lines.push(issue.url);
  const desc = (issue.description ?? "").trim();
  if (desc) lines.push("", desc.length > 4000 ? `${desc.slice(0, 4000)}…` : desc);
  return lines.join("\n");
}

// Jira timestamps ("2026-11-04T07:37:06.711+0000") as a short age: "5m",
// "3h", "2d", "4w", "3mo", "2y".
export function ago(ts, now = Date.now()) {
  const t = parseJiraTime(ts);
  if (t === null) return "";
  const s = Math.max(0, (now - t) / 1000);
  if (s < 60) return "now";
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  if (s < 86400 * 14) return `${Math.floor(s / 86400)}d`;
  if (s < 86400 * 60) return `${Math.floor(s / 86400 / 7)}w`;
  if (s < 86400 * 365) return `${Math.floor(s / 86400 / 30)}mo`;
  return `${Math.floor(s / 86400 / 365)}y`;
}

export function parseJiraTime(ts) {
  if (!ts) return null;
  // Jira's offsets have no colon ("+0000"), which Date.parse may refuse.
  const fixed = String(ts).replace(/([+-]\d\d)(\d\d)$/, "$1:$2");
  const t = Date.parse(fixed);
  return Number.isNaN(t) ? null : t;
}

// "3 Nov 2026" for a Jira timestamp.
export function dateLabel(ts) {
  const t = parseJiraTime(ts);
  if (t === null) return "";
  const d = new Date(t);
  const months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  return `${d.getDate()} ${months[d.getMonth()]} ${d.getFullYear()}`;
}

export function initials(name) {
  const parts = (name ?? "").trim().split(/\s+/).filter(Boolean);
  if (!parts.length) return "?";
  const first = [...parts[0]][0] ?? "";
  const last = parts.length > 1 ? [...parts[parts.length - 1]][0] ?? "" : "";
  return (first + last).toUpperCase();
}

// A stable index into a palette for a name.
export function hashIndex(text, n) {
  let h = 0;
  for (const ch of text ?? "") h = (h * 31 + ch.codePointAt(0)) >>> 0;
  return n ? h % n : 0;
}

// The type an issue's icon shows: bug, story, task, epic, subtask or other.
export function typeKind(issue) {
  const t = (issue?.type ?? "").toLowerCase();
  if (issue?.level < 0 || t === "sub-task" || t === "subtask") return "subtask";
  if (t === "bug") return "bug";
  if (t === "story" || t === "user story") return "story";
  if (t === "epic" || issue?.level === 1) return "epic";
  if (t === "task") return "task";
  if (issue?.level > 1) return "initiative";
  return "other";
}

// Jira's default priority names, highest first, as a 0..4 rank (null: none).
export function priorityRank(name) {
  const n = (name ?? "").toLowerCase();
  if (!n) return null;
  if (n.includes("highest") || n.includes("blocker") || n.includes("critical")) return 0;
  if (n.includes("high") || n.includes("major")) return 1;
  if (n.includes("medium") || n.includes("normal")) return 2;
  if (n.includes("lowest") || n.includes("trivial")) return 4;
  if (n.includes("low") || n.includes("minor")) return 3;
  return 2;
}

// What a space or pane shows: { branch -> ticket } resolved by the helper.
// Returns the distinct branches worth asking about, in a stable order.
export function branchesToResolve(spaces, panes) {
  const set = new Set();
  for (const s of spaces ?? []) if (s.branch) set.add(s.branch);
  for (const p of panes ?? []) if (p.branch) set.add(p.branch);
  return [...set].filter((b) => branchKeys(b).length > 0).sort();
}

// Which spaces are on which ticket: key -> [space, ...]. A space counts when
// the branch it shows, or any of its panes' branches, resolves to the key.
export function spacesByTicket(spaces, panes, tickets) {
  const out = new Map();
  for (const s of spaces ?? []) {
    const tabs = new Set((s.tabs ?? []).map((t) => t.id));
    const branches = new Set([s.branch, ...(panes ?? []).filter((p) => tabs.has(p.tab)).map((p) => p.branch)].filter(Boolean));
    const keys = new Set();
    for (const b of branches) { const t = tickets?.[b]; if (t) keys.add(t.key); }
    for (const k of keys) {
      if (!out.has(k)) out.set(k, []);
      out.get(k).push(s);
    }
  }
  return out;
}
