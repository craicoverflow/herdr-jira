// Tests for the Jira mod's pure data shaping (app/mods/jira/model.js).
// Run: node --test app/test/

import { test } from "node:test";
import assert from "node:assert/strict";
import * as M from "../mods/jira/model.js";

const issue = (key, extra = {}) => ({ key, summary: `Summary of ${key}`, status: "To Do", category: "new", type: "Task", level: 0, ...extra });
const kinds = (rows) => rows.map((r) => (r.kind === "head" ? `#${r.lane}(${r.count}${r.more ? "+" : ""})` : r.kind === "note" ? `~${r.text}` : `${r.kind}:${r.issue.key}${r.depth ? `@${r.depth}` : ""}`));

const MY = { id: "my-work", name: "My work", kind: "my-work", groupBy: "status" };
const EPICS = { id: "epics", name: "Epics", kind: "epics", groupBy: "status" };
const BUGS = { id: "view-2", name: "Team bugs", kind: "jql", groupBy: "status" };
const FLAT = { id: "view-3", name: "Triage", kind: "jql", groupBy: "none" };
const at = (hours) => new Date(Date.parse("2026-11-23T12:00:00Z") - hours * 3600e3).toISOString().replace("Z", "+0000");
const prog = (key, hours, extra = {}) => issue(key, { status: "In Progress", category: "indeterminate", updated: at(hours), ...extra });
const review = (key, hours, extra = {}) => issue(key, { status: "In Review", category: "indeterminate", updated: at(hours), ...extra });
const todo = (key, hours, extra = {}) => issue(key, { updated: at(hours), ...extra });

test("My work groups by status: in-progress kinds first, then To Do, newest first", () => {
  const data = {
    issues: [todo("A-1", 1), review("A-2", 5), prog("A-3", 9), prog("A-4", 2), todo("A-5", 7)],
    subtasks: [],
  };
  assert.deepEqual(kinds(M.buildRows(MY, data)), [
    "#In Progress(2)", "issue:A-4", "issue:A-3",
    "#In Review(1)", "issue:A-2",
    "#To Do(2)", "issue:A-1", "issue:A-5",
  ]);
});

test("each distinct in-progress status gets its own heading", () => {
  const data = { issues: [issue("A-1", { status: "Blocked", category: "indeterminate" }), review("A-2", 1), prog("A-3", 1)], subtasks: [] };
  assert.deepEqual(M.buildRows(MY, data).filter((r) => r.kind === "head").map((r) => r.lane), ["In Progress", "In Review", "Blocked"]);
});

test("sub-tasks nest under their parent, whatever their own status; orphans group by theirs", () => {
  const data = {
    issues: [prog("A-1", 3), todo("A-2", 1)],
    subtasks: [todo("A-3", 1, { level: -1, parent: "A-1" }), review("A-4", 2, { level: -1, parent: "B-9" })],
  };
  assert.deepEqual(kinds(M.buildRows(MY, data)), [
    "#In Progress(2)", "issue:A-1", "subtask:A-3@1",
    "#In Review(1)", "subtask:A-4",
    "#To Do(1)", "issue:A-2",
  ]);
});

test("assigned epics are ordinary rows in My work", () => {
  const rows = M.buildRows(MY, { issues: [prog("E-1", 1, { type: "Epic", level: 1 })], subtasks: [] });
  assert.deepEqual(kinds(rows), ["#In Progress(1)", "issue:E-1"]);
});

test("a custom view groups the same way; Done sorts last", () => {
  const data = { issues: [issue("A-1", { status: "Done", category: "done" }), todo("A-2", 1), prog("A-3", 1)], subtasks: [] };
  assert.deepEqual(M.buildRows(BUGS, data).filter((r) => r.kind === "head").map((r) => r.lane), ["In Progress", "To Do", "Done"]);
});

test("group_by none lists in Jira's order with no headings", () => {
  const data = { issues: [todo("A-1", 9), prog("A-2", 1), todo("A-3", 5)], subtasks: [] };
  assert.deepEqual(kinds(M.buildRows(FLAT, data)), ["issue:A-1", "issue:A-2", "issue:A-3"]);
});

test("empty views read well", () => {
  const empty = { issues: [], subtasks: [] };
  assert.deepEqual(kinds(M.buildRows(MY, empty)), [`~${M.NOTE_NO_ISSUES}`]);
  assert.deepEqual(kinds(M.buildRows(BUGS, empty)), ["~Nothing matches Team bugs."]);
  assert.deepEqual(kinds(M.buildRows(FLAT, empty)), ["~Nothing matches Triage."]);
  assert.deepEqual(kinds(M.buildRows(EPICS, { epics: [], starred: [], starredKeys: [] })).slice(0, 2), ["#Assigned to me(0)", `~${M.NOTE_NO_EPICS}`]);
  assert.deepEqual(kinds(M.buildRows(MY, null)), []);
});

test("filtering narrows each view at once, and a view with no match shows nothing", () => {
  const data = {
    issues: [prog("A-1", 1, { summary: "cart bug" }), todo("A-2", 1, { summary: "other" }), review("B-3", 1, { summary: "cart again" })],
    subtasks: [todo("A-4", 1, { level: -1, parent: "A-2", summary: "cart child" })],
  };
  for (const view of [MY, BUGS]) {
    assert.deepEqual(kinds(M.buildRows(view, data, "cart")), [
      "#In Progress(1)", "issue:A-1", "#In Review(1)", "issue:B-3", "#To Do(1)", "subtask:A-4",
    ]);
  }
  assert.deepEqual(kinds(M.buildRows(FLAT, data, "b-3")), ["issue:B-3"]);
  assert.deepEqual(kinds(M.buildRows(MY, data, "zzz")), []);
  assert.deepEqual(kinds(M.buildRows(FLAT, data, "zzz")), []);
});

test("a full page shows 50+ on every heading until a filter is typed", () => {
  const data = { issues: [prog("A-1", 1), todo("A-2", 1)], subtasks: [], issuesMore: true };
  const heads = (rows) => rows.filter((r) => r.kind === "head");
  assert.deepEqual(heads(M.buildRows(MY, data)).map((r) => r.more), [true, true]);
  assert.deepEqual(heads(M.buildRows(MY, data, "A-")).map((r) => r.more), [false, false]);
  assert.match(M.buildRows(FLAT, data).at(-1).text, /first 2/);
  assert.equal(M.viewCount(MY, data), "2+");
  assert.equal(M.viewCount(MY, null), "");
});

test("epics keep their lanes and count", () => {
  const data = { epics: [issue("E-1", { level: 1 })], starred: [issue("S-1", { level: 1 })], starredKeys: ["S-1"] };
  assert.equal(M.viewCount(EPICS, data), "2");
});

test("views cycle with wrap, from an unknown id too", () => {
  const views = [MY, BUGS, EPICS];
  assert.equal(M.cycleView(views, "my-work", 1).id, "view-2");
  assert.equal(M.cycleView(views, "epics", 1).id, "my-work");
  assert.equal(M.cycleView(views, "my-work", -1).id, "epics");
  assert.equal(M.cycleView(views, "gone", 1).id, "my-work");
  assert.equal(M.cycleView([], "x", 1), null);
});

test("epics: assigned first, then starred; an epic in both shows once; Done stars hide", () => {
  const data = {
    epics: [issue("E-1", { type: "Epic", level: 1 }), issue("E-2", { type: "Epic", level: 1 })],
    starred: [issue("E-2", { type: "Epic", level: 1 }), issue("S-1", { type: "Epic", level: 1 }), issue("S-2", { type: "Epic", level: 1, category: "done" })],
    starredKeys: ["E-2", "S-1", "S-2"],
  };
  const rows = M.buildRows(EPICS, data);
  assert.deepEqual(kinds(rows), ["#Assigned to me(2)", "epic:E-1", "epic:E-2", "#Starred(1)", "epic:S-1"]);
  assert.equal(rows.find((r) => r.issue?.key === "E-2").starred, true);
  assert.equal(rows.find((r) => r.issue?.key === "E-1").starred, false);
});

test("a starred epic no longer in starredKeys (unstarred) is not listed", () => {
  const data = { epics: [], starred: [issue("S-1", { type: "Epic", level: 1 })], starredKeys: [] };
  assert.deepEqual(kinds(M.buildRows(EPICS, data)), ["#Assigned to me(0)", `~${M.NOTE_NO_EPICS}`, "#Starred(0)", `~${M.NOTE_NO_STARRED}`]);
});

test("search results never repeat a row already shown, and drop Done hits", () => {
  const data = { issues: [issue("A-1")], subtasks: [] };
  const hits = [issue("A-1"), issue("B-2"), issue("B-3", { category: "done" }), issue("B-4", { level: -1 })];
  assert.deepEqual(kinds(M.buildRows(MY, data, "", hits)), [
    "#To Do(1)", "issue:A-1", "#Search results(2)", "issue:B-2", "subtask:B-4",
  ]);
  // A custom view's query decides what belongs, so its Done hits stay.
  assert.deepEqual(kinds(M.buildRows(BUGS, data, "", hits)).slice(-3), ["issue:B-2", "issue:B-3", "subtask:B-4"]);
});

test("hits that are all listed already say so, rather than 'no matches'", () => {
  const rows = M.buildRows(MY, { issues: [issue("A-1")], subtasks: [] }, "A", [issue("A-1")]);
  assert.deepEqual(kinds(rows).slice(-2), ["#Search results(0)", `~${M.NOTE_NO_MORE}`]);
});

test("an empty answer still shows its heading", () => {
  assert.deepEqual(kinds(M.buildRows(MY, { issues: [], subtasks: [] }, "nothing", [])), ["#Search results(0)", `~${M.NOTE_NO_MATCHES}`]);
});

test("row ids are unique even when an issue is in two lanes", () => {
  const data = { epics: [], starred: [], starredKeys: [] };
  const rows = M.buildRows(EPICS, data, "", [issue("E-9", { type: "Epic", level: 1 })]);
  const ids = rows.map((r) => r.id);
  assert.equal(new Set(ids).size, ids.length);
});

test("children list in-progress first, with a heading that leads back", () => {
  const rows = M.childRows({ key: "E-1" }, [issue("C-1", { category: "done" }), issue("C-2", { category: "indeterminate" }), issue("C-3")]);
  assert.deepEqual(kinds(rows), ["#In this epic(3)", "issue:C-2", "issue:C-3", "issue:C-1"]);
  assert.equal(rows[0].epic.key, "E-1");
});

test("moveSelection skips headings and notes and wraps", () => {
  const rows = M.buildRows(MY, { issues: [issue("A-1"), issue("A-2")], subtasks: [] }, "", []);
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

test("validateCreate asks for project, type and a summary, in that order", () => {
  assert.equal(M.validateCreate({ type: "Task", summary: "x" }), "Pick a project.");
  assert.equal(M.validateCreate({ project: "WEB", summary: "x" }), "Pick an issue type.");
  assert.equal(M.validateCreate({ project: "WEB", type: "Task", summary: "   " }), "Summary is required.");
  assert.match(M.validateCreate({ project: "WEB", type: "Task", summary: "é".repeat(256) }), /longer than 255/);
  assert.equal(M.validateCreate({ project: "WEB", type: "Task", summary: "é".repeat(255) }), "");
});

test("pickIssueType keeps the current type, then the last used, then a plain one", () => {
  const types = [{ name: "Epic", level: 1 }, { name: "Bug" }, { name: "Task" }];
  assert.equal(M.pickIssueType(types, "Bug", "Task"), "Bug");
  assert.equal(M.pickIssueType(types, "Story", "Bug"), "Bug");
  assert.equal(M.pickIssueType(types, null, null), "Task");
  assert.equal(M.pickIssueType([{ name: "Epic", level: 1 }, { name: "Incident" }], null, null), "Incident");
  assert.equal(M.pickIssueType([{ name: "Epic", level: 1 }], null, null), "Epic");
  assert.equal(M.pickIssueType([], "Bug", null), null);
});

test("filterProjects matches key or name", () => {
  const ps = [{ key: "WEB", name: "Storefront" }, { key: "API", name: "Platform API" }];
  assert.deepEqual(M.filterProjects(ps, "front").map((p) => p.key), ["WEB"]);
  assert.deepEqual(M.filterProjects(ps, "api").map((p) => p.key), ["API"]);
  assert.equal(M.filterProjects(ps, "").length, 2);
});

// ---- first-run setup ------------------------------------------------------------

test("setup shows the site step only for an account with several Jira Cloud sites", () => {
  assert.deepEqual(M.setupSteps([]), ["project", "tabs"]);
  assert.deepEqual(M.setupSteps([{ id: "a" }]), ["project", "tabs"]);
  assert.deepEqual(M.setupSteps([{ id: "a" }, { id: "b" }]), ["site", "project", "tabs"]);
  // A site that isn't Jira Cloud can't be chosen, so it doesn't make a choice.
  assert.deepEqual(M.setupSteps([{ id: "a" }, { id: "dc", cloud: false }]), ["project", "tabs"]);
  assert.ok(M.setupSteps([{ id: "a" }, { id: "b" }]).length <= 3);
});

test("setup steps move forward and back, and end with null (done)", () => {
  const steps = ["site", "project", "tabs"];
  assert.equal(M.stepAfter(steps, "site"), "project");
  assert.equal(M.stepAfter(steps, "tabs"), null);
  assert.equal(M.stepAfter(steps, "tabs", -1), "project");
  assert.equal(M.stepAfter(steps, "site", -1), null);
  assert.equal(M.stepAfter(["project", "tabs"], "project", -1), null);
});

test("rankProjects puts exact and prefix key matches first, then names", () => {
  const ps = [{ key: "WEBX", name: "Other" }, { key: "API", name: "Web services" }, { key: "WEB", name: "Storefront" }, { key: "AWEB", name: "Zed" }];
  assert.deepEqual(M.rankProjects(ps, "web").map((p) => p.key), ["WEB", "WEBX", "AWEB", "API"]);
  assert.deepEqual(M.rankProjects(ps, "").map((p) => p.key), ["WEBX", "API", "WEB", "AWEB"]);
  assert.deepEqual(M.rankProjects(ps, "zzz"), []);
  assert.deepEqual(M.rankProjects(null, "a"), []);
});

test("windowAround keeps the highlight in view and counts what is cut", () => {
  const list = Array.from({ length: 20 }, (_, i) => i);
  assert.deepEqual(M.windowAround(list, 0, 7), { items: [0, 1, 2, 3, 4, 5, 6], start: 0, before: 0, after: 13 });
  assert.deepEqual(M.windowAround(list, 10, 7).items, [7, 8, 9, 10, 11, 12, 13]);
  assert.deepEqual(M.windowAround(list, 19, 7), { items: [13, 14, 15, 16, 17, 18, 19], start: 13, before: 13, after: 0 });
  assert.deepEqual(M.windowAround([1, 2], 1, 7), { items: [1, 2], start: 0, before: 0, after: 0 });
  assert.equal(M.clampMove(0, -1, 5), 0);
  assert.equal(M.clampMove(4, 1, 5), 4);
  assert.equal(M.clampMove(0, 1, 0), 0);
});

test("validateTab wants a unique name and a query", () => {
  assert.equal(M.validateTab({ name: "Bugs", jql: "type = Bug" }, [{ name: "My work" }]), "");
  assert.match(M.validateTab({ name: " ", jql: "x" }), /name/);
  assert.match(M.validateTab({ name: "Bugs", jql: "  " }), /JQL/);
  assert.match(M.validateTab({ name: "bugs", jql: "x" }, [{ name: "Bugs" }]), /already/);
  assert.match(M.validateTab({ name: "x".repeat(31), jql: "x" }), /longer/);
});

test("setupViews: My work always, Epics as asked, existing tabs keep their order", () => {
  assert.deepEqual(M.setupViews({ existing: [MY], epics: false, custom: [] }), [{ builtin: "my-work" }]);
  assert.deepEqual(M.setupViews({ existing: [MY], epics: true, custom: [] }), [{ builtin: "my-work" }, { builtin: "epics" }]);
  assert.deepEqual(M.setupViews({ existing: [], epics: false, custom: [] }), [{ builtin: "my-work" }]);

  const bugs = { ...BUGS, jql: "type = Bug" };
  const flat = { ...FLAT, jql: "filter = 10234" };
  const existing = [MY, bugs, flat, EPICS];
  // Nothing changed: the same list back, a filter tab as its JQL, group_by kept.
  assert.deepEqual(M.setupViews({ existing, epics: true, custom: [bugs, flat] }), [
    { builtin: "my-work" }, { name: "Team bugs", jql: "type = Bug" }, { name: "Triage", jql: "filter = 10234", group_by: "none" }, { builtin: "epics" },
  ]);
  // Epics off and one tab removed; a new one goes last.
  assert.deepEqual(M.setupViews({ existing, epics: false, custom: [flat, { name: " New ", jql: " a = 1 " }] }), [
    { builtin: "my-work" }, { name: "Triage", jql: "filter = 10234", group_by: "none" }, { name: "New", jql: "a = 1" },
  ]);
  // A renamed built-in keeps its name.
  assert.deepEqual(M.setupViews({ existing: [{ ...MY, name: "Mine" }], epics: false, custom: [] }), [{ builtin: "my-work", name: "Mine" }]);
});

test("setupPayload carries only the steps that were done", () => {
  assert.deepEqual(M.setupPayload({ project: null, tabs: null }), {});
  assert.deepEqual(M.setupPayload({ project: "WEB", tabs: null }), { default_project: "WEB" });
  assert.deepEqual(M.setupPayload({ project: null, tabs: { existing: [MY], epics: true, custom: [] } }), { views: [{ builtin: "my-work" }, { builtin: "epics" }] });
});
