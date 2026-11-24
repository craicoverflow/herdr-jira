// Tests for the Jira mod's pure data shaping (app/mods/jira/model.js).
// Run: node --test app/test/

import { test } from "node:test";
import assert from "node:assert/strict";
import * as M from "../mods/jira/model.js";

const issue = (key, extra = {}) => ({ key, summary: `Summary of ${key}`, status: "To Do", category: "new", type: "Task", level: 0, ...extra });
const kinds = (rows) => rows.map((r) => (r.kind === "head" ? `#${r.lane}(${r.count}${r.more ? "+" : ""})` : r.kind === "note" ? `~${r.text}` : `${r.kind}:${r.issue.key}${r.depth ? `@${r.depth}` : ""}`));

test("issues lane nests sub-tasks under their parent and keeps orphans last", () => {
  const data = {
    issues: [issue("A-1"), issue("A-2")],
    subtasks: [issue("A-3", { level: -1, parent: "A-1" }), issue("A-4", { level: -1, parent: "B-9" })],
  };
  assert.deepEqual(kinds(M.buildRows("issues", data)), [
    "#Assigned to me(4)", "issue:A-1", "subtask:A-3@1", "issue:A-2", "subtask:A-4",
  ]);
});

test("an empty lane says why; a filtered-out lane disappears", () => {
  const data = { issues: [], subtasks: [] };
  assert.deepEqual(kinds(M.buildRows("issues", data)), ["#Assigned to me(0)", `~${M.NOTE_NO_ISSUES}`]);
  assert.deepEqual(kinds(M.buildRows("issues", { issues: [issue("A-1")], subtasks: [] }, "zzz")), []);
});

test("a full page shows 50+ until a filter is typed", () => {
  const data = { issues: [issue("A-1")], subtasks: [], issuesMore: true };
  assert.equal(M.buildRows("issues", data)[0].more, true);
  assert.equal(M.buildRows("issues", data, "A-1")[0].more, false);
});

test("epics: assigned first, then starred; an epic in both shows once; Done stars hide", () => {
  const data = {
    epics: [issue("E-1", { type: "Epic", level: 1 }), issue("E-2", { type: "Epic", level: 1 })],
    starred: [issue("E-2", { type: "Epic", level: 1 }), issue("S-1", { type: "Epic", level: 1 }), issue("S-2", { type: "Epic", level: 1, category: "done" })],
    starredKeys: ["E-2", "S-1", "S-2"],
  };
  const rows = M.buildRows("epics", data);
  assert.deepEqual(kinds(rows), ["#Assigned to me(2)", "epic:E-1", "epic:E-2", "#Starred(1)", "epic:S-1"]);
  assert.equal(rows.find((r) => r.issue?.key === "E-2").starred, true);
  assert.equal(rows.find((r) => r.issue?.key === "E-1").starred, false);
});

test("a starred epic no longer in starredKeys (unstarred) is not listed", () => {
  const data = { epics: [], starred: [issue("S-1", { type: "Epic", level: 1 })], starredKeys: [] };
  assert.deepEqual(kinds(M.buildRows("epics", data)), ["#Assigned to me(0)", `~${M.NOTE_NO_EPICS}`, "#Starred(0)", `~${M.NOTE_NO_STARRED}`]);
});

test("search results never repeat a row already shown, and drop Done hits", () => {
  const data = { issues: [issue("A-1")], subtasks: [] };
  const hits = [issue("A-1"), issue("B-2"), issue("B-3", { category: "done" }), issue("B-4", { level: -1 })];
  assert.deepEqual(kinds(M.buildRows("issues", data, "", hits)), [
    "#Assigned to me(1)", "issue:A-1", "#Search results(2)", "issue:B-2", "subtask:B-4",
  ]);
});

test("hits that are all listed already say so, rather than 'no matches'", () => {
  const rows = M.buildRows("issues", { issues: [issue("A-1")], subtasks: [] }, "A", [issue("A-1")]);
  assert.deepEqual(kinds(rows).slice(-2), ["#Search results(0)", `~${M.NOTE_NO_MORE}`]);
});

test("an empty answer still shows its heading", () => {
  assert.deepEqual(kinds(M.buildRows("issues", { issues: [], subtasks: [] }, "nothing", [])), ["#Search results(0)", `~${M.NOTE_NO_MATCHES}`]);
});

test("row ids are unique even when an issue is in two lanes", () => {
  const data = { epics: [], starred: [], starredKeys: [] };
  const rows = M.buildRows("epics", data, "", [issue("E-9", { type: "Epic", level: 1 })]);
  const ids = rows.map((r) => r.id);
  assert.equal(new Set(ids).size, ids.length);
});

test("children list in-progress first, with a heading that leads back", () => {
  const rows = M.childRows({ key: "E-1" }, [issue("C-1", { category: "done" }), issue("C-2", { category: "indeterminate" }), issue("C-3")]);
  assert.deepEqual(kinds(rows), ["#In this epic(3)", "issue:C-2", "issue:C-3", "issue:C-1"]);
  assert.equal(rows[0].epic.key, "E-1");
});

test("moveSelection skips headings and notes and wraps", () => {
  const rows = M.buildRows("issues", { issues: [issue("A-1"), issue("A-2")], subtasks: [] }, "", []);
  const first = M.moveSelection(rows, -1, 1);
  assert.equal(rows[first].issue.key, "A-1");
  const second = M.moveSelection(rows, first, 1);
  assert.equal(rows[second].issue.key, "A-2");
  assert.equal(M.moveSelection(rows, second, 1), first);
  assert.equal(M.moveSelection(rows, first, -1), second);
  assert.equal(M.moveSelection([{ kind: "head" }], -1, 1), -1);
});

test("branch keys are found anywhere, upper-cased, once each", () => {
  assert.deepEqual(M.branchKeys("feature/proj-12-fix-PROJ-12-and-ABC-7"), ["PROJ-12", "ABC-7"]);
  assert.deepEqual(M.branchKeys("main"), []);
  assert.deepEqual(M.branchKeys(null), []);
});

test("branch names come from key and summary, slugged and bounded", () => {
  assert.equal(M.branchName("WEB-1", "Checkout: remember the shipping address!"), "WEB-1-checkout-remember-the-shipping-address");
  assert.equal(M.branchName("WEB-1", "Café déjà vu"), "WEB-1-cafe-deja-vu");
  assert.equal(M.branchName("WEB-1", ""), "WEB-1");
  assert.ok(M.branchName("WEB-1", "word ".repeat(40)).length <= "WEB-1-".length + 48);
});

test("validBranch refuses what git would", () => {
  for (const ok of ["WEB-1-fix", "alex/WEB-1", "release/1.2"]) assert.ok(M.validBranch(ok), ok);
  for (const bad of ["", "has space", "a..b", "-x", "x/", "x.lock", "a~b", "a:b", "a//b", "x."]) assert.ok(!M.validBranch(bad), bad);
});

test("worktree folders are slugs of the branch", () => {
  assert.equal(M.worktreeSlug("alex/WEB-1 fix"), "alex-WEB-1-fix");
});

test("agent tasks carry the key, summary, link and description", () => {
  const t = M.agentTask({ key: "WEB-1", summary: "Fix it", url: "https://x/browse/WEB-1", description: "Do the thing." });
  assert.equal(t, "Work on Jira issue WEB-1: Fix it\nhttps://x/browse/WEB-1\n\nDo the thing.");
  assert.equal(M.agentTask({ key: "WEB-1", summary: "Fix it" }), "Work on Jira issue WEB-1: Fix it");
});

test("ages read Jira's offset format", () => {
  const now = Date.parse("2026-11-23T12:00:00Z");
  assert.equal(M.ago("2026-11-23T11:30:00.000+0000", now), "30m");
  assert.equal(M.ago("2026-11-23T09:00:00.000+0000", now), "3h");
  assert.equal(M.ago("2026-11-20T12:00:00.000+0000", now), "3d");
  assert.equal(M.ago("2026-10-01T12:00:00.000+0000", now), "7w");
  assert.equal(M.ago("2026-09-01T12:00:00.000+0000", now), "2mo");
  assert.equal(M.ago("2024-11-01T12:00:00.000+0000", now), "2y");
  assert.equal(M.ago("not a time", now), "");
  assert.equal(M.dateLabel("2026-11-04T07:37:06.711+0000"), "4 Nov 2026");
});

test("initials and type kinds", () => {
  assert.equal(M.initials("Alex Rivera"), "AR");
  assert.equal(M.initials("Cher"), "C");
  assert.equal(M.initials(""), "?");
  assert.equal(M.typeKind({ type: "Bug" }), "bug");
  assert.equal(M.typeKind({ type: "Sub-task", level: -1 }), "subtask");
  assert.equal(M.typeKind({ type: "Feature", level: 1 }), "epic");
  assert.equal(M.typeKind({ type: "Initiative", level: 2 }), "initiative");
  assert.equal(M.typeKind({ type: "Spike", level: 0 }), "other");
});

test("priority ranks follow Jira's default names", () => {
  assert.deepEqual(["Highest", "High", "Medium", "Low", "Lowest", "Blocker", ""].map(M.priorityRank), [0, 1, 2, 3, 4, 0, null]);
});

test("spaces map to tickets through any of their panes' branches", () => {
  const spaces = [
    { id: "s1", branch: "WEB-1-x", tabs: [{ id: "t1" }] },
    { id: "s2", branch: "main", tabs: [{ id: "t2" }] },
  ];
  const panes = [{ tab: "t2", branch: "WEB-1-y" }, { tab: "t1", branch: null }];
  const tickets = { "WEB-1-x": { key: "WEB-1" }, "WEB-1-y": { key: "WEB-1" }, main: null };
  const by = M.spacesByTicket(spaces, panes, tickets);
  assert.deepEqual(by.get("WEB-1").map((s) => s.id), ["s1", "s2"]);
  assert.deepEqual(M.branchesToResolve(spaces, panes), ["WEB-1-x", "WEB-1-y"]);
});

test("worktree slugs: collisions share a folder; punctuation-only names have none", () => {
  assert.equal(M.worktreeSlug("feat/a"), M.worktreeSlug("feat-a"));
  for (const b of ["+", "+.+", "+..+"]) assert.equal(M.usableSlug(M.worktreeSlug(b)), false, b);
  assert.equal(M.usableSlug(M.worktreeSlug("PROJ-12-fix-login")), true);
});

test("worktreeForBranch reads porcelain output, skipping bare and detached entries", () => {
  const out = [
    "worktree /repos/app", "bare", "",
    "worktree /home/me/.herdr/worktrees/app/old one", "HEAD 1234", "detached", "",
    "worktree /home/me/.herdr/worktrees/app/PROJ-1 x", "HEAD 5678", "branch refs/heads/PROJ-1-x", "",
  ].join("\n");
  assert.equal(M.worktreeForBranch(out, "PROJ-1-x"), "/home/me/.herdr/worktrees/app/PROJ-1 x");
  assert.equal(M.worktreeForBranch(out, "PROJ-1"), null);
  assert.equal(M.worktreeForBranch("", "PROJ-1-x"), null);
});

test("generations: a stamp goes stale once its name is bumped", () => {
  const g = new M.Generations();
  const s = g.stamp("issues");
  assert.equal(g.same("issues", s), true);
  g.bump("epics");
  assert.equal(g.same("issues", s), true, "other names don't affect it");
  g.bump("issues");
  assert.equal(g.same("issues", s), false);
});

test("canPost refuses blank drafts", () => {
  assert.equal(M.canPost(""), false);
  assert.equal(M.canPost("  \n "), false);
  assert.equal(M.canPost(undefined), false);
  assert.equal(M.canPost(" hi "), true);
});

test("mergeComments shows pending comments until Jira returns them", () => {
  const server = [{ author: "A", created: "t1", body: "first" }];
  const pending = [{ id: 1, author: "Me", created: "t2", body: "mine\n" }];
  assert.deepEqual(M.mergeComments(server, pending).map((c) => [c.body.trim(), Boolean(c.pending)]), [["first", false], ["mine", true]]);
  const later = [...server, { author: "Me", created: "t3", body: "mine" }];
  assert.deepEqual(M.mergeComments(later, pending).map((c) => [c.body, Boolean(c.pending)]), [["first", false], ["mine", false]]);
  assert.deepEqual(M.stillPending(later, pending), []);
  assert.equal(M.stillPending(server, pending).length, 1);
  const posted = [{ ...pending[0], postedAt: 100 }];
  assert.equal(M.stillPending(server, posted, 99).length, 1, "read began before the post");
  assert.equal(M.stillPending(server, posted, 100).length, 0, "reworded by Jira: the fresh read wins");
  assert.equal(M.stillPending(server, pending, 1e12).length, 1, "still sending");
  assert.deepEqual(M.mergeComments(null, null), []);
});

test("commentError says what happened in plain words", () => {
  assert.match(M.commentError({ code: "signed_out" }), /signed out/i);
  assert.match(M.commentError({ code: "network", message: "dial tcp" }), /reach Jira/);
  assert.match(M.commentError({ code: "x", message: "comment on A-1: 403" }), /403/);
  assert.match(M.commentError({}), /try again/);
  assert.match(M.commentError({}), /kept/);
});
