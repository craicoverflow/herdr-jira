// Jira for Herdr: your Jira Cloud issues and epics beside your terminals.
//
// Two tabs (Issues, Epics) of headed lanes, a search that filters as you type
// and then asks Jira, and a detail pane: status changes from the issue's live
// transitions, assign to yourself, Start work (a branch or a worktree space,
// optionally with an agent on it), star epics. The ticket your space's branch
// names is pinned at the top, and rows show which spaces are on them.
//
// Jira is reached through the herdr-jira helper (api.js), which signs in to
// Atlassian in the browser; nothing here holds a token.
//
// Opens as a dialog (`mod.open:jira`, cmd-j in keys.json), or as a pane
// (`pane.split {direction: "right", mod: "jira"}`): it lays itself out for
// the width it gets.

import { View, div, svg, image } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, TextareaState, Textarea, Scrollbar, v_virtual_list, VirtualListScrollHandle, TextView } from "gpui-base";
import { spaces, panes, focused, theme, props, closeOverlay, run, notify, knownAgents, home, nextCommand } from "herdr";
import { api } from "./api.js";
import * as M from "./model.js";
import { icon, typeTile, statusPill, statusColors, priorityMark, avatar, kbd, button, spaceChip, spaceColor } from "./ui.js";
import { currentSpace, spaceFolder, repoOf, remoteHome, startBranch } from "./start.js";

const ROW_H = { head: 32, note: 34, issue: 36, subtask: 34, epic: 36 };
const WIDE = 720; // below this, the list and the detail take turns
const SEARCH_DELAY = 300;
const DETAIL_TTL = 60_000;

export default class Jira extends View {
  init(_props, cx) {
    this.overlay = props()?.overlay ?? null;
    this.wide = Boolean(this.overlay);
    this.handle = cx.focus_handle();
    this.menuHandle = cx.focus_handle();
    this.scroll = VirtualListScrollHandle.new();
    this.tab = this.stored("tab") === "epics" ? "epics" : "issues";

    this.session = null; // null while checking
    this.setupError = null; // the helper is missing or broken
    this.me = null;
    this.data = { issues: null, epics: null };
    this.loading = { issues: false, epics: false };
    this.errors = { issues: null, epics: null };

    this.query = "";
    this.typing = false;
    this.hits = { issues: null, epics: null };
    this.hitsFor = { issues: null, epics: null };
    this.searching = { issues: 0, epics: 0 }; // the seq of the search running, or 0
    this.searchSeq = 0;
    // Bumped when something makes replies already on their way stale: a sign
    // in or out ("session"), a local change to a tab's list ("issues",
    // "epics"), a newer tickets lookup ("tickets").
    this.gen = new M.Generations();

    this.selected = { issues: null, epics: null }; // row ids
    this.pinned = null; // an issue shown in the detail that isn't a list row
    this.autoPinned = false;
    this.narrowDetail = false;
    this.drill = null; // { epic, back } while listing one epic's children

    this.details = new Map(); // key -> { data, loading, error, at }
    this.children = new Map(); // epic key -> { list, loading, error }
    this.transitions = new Map(); // key -> { list, status, loading, error }
    this.staleTransitions = new Set(); // keys whose next transitions load asks Jira
    this.busy = new Map(); // key -> what's running ("status", "assign", "star")
    this.menu = null; // { key, at } while the status menu is open
    this.boxes = new Map(); // key -> the comment box (its text is the draft, kept while the mod is open)
    this.commenting = null; // the key whose comment box has the keyboard
    this.pendingComments = new Map(); // key -> comments posted here that the issue hasn't returned yet
    this.commentSt = new Map(); // key -> { busy, error }
    this.commentSeq = 0;
    this.sheet = null; // { kind: "start" | "help", ... }
    this.notice = null;

    this.tickets = {};
    this.ticketsKey = null;

    this.input = InputState.new({ placeholder: "Filter, or search Jira" });
    this.input.on("change", (_e, cx) => this.onQuery(cx));
    this.input.on("focus", (_e, cx) => { this.typing = true; cx.notify(); });
    this.input.on("blur", (_e, cx) => { this.typing = false; cx.notify(); });
    this.input.on("submit", (_e, cx) => { this.open(cx); cx.notify(); });

    this.handle.focus();
    cx.timer.after(250, () => { if (!this.typing && !this.sheet) this.handle.focus(); });

    this.start(cx);
    cx.timer.every(60_000, (cx) => this.tick(cx));
    cx.spawn(async (cx) => {
      for (;;) {
        const c = await nextCommand();
        this.command(c, cx);
        cx.notify();
      }
    });
  }

  // ---- small helpers -------------------------------------------------------

  stored(k) { try { return localStorage.getItem(k); } catch { return null; } }
  store(k, v) { try { localStorage.setItem(k, v); } catch {} }

  say(text, level = "info", cx) {
    const id = (this.notice?.id ?? 0) + 1;
    this.notice = { text, level, id };
    cx?.timer.after(level === "error" ? 8000 : 4000, (cx) => { if (this.notice?.id === id) { this.notice = null; cx.notify(); } });
  }

  // A failure that means "sign in again" sends the mod back to its sign-in
  // screen; others are shown where they happened.
  authFailed(error) {
    if (error?.code === "signed_out" && this.session) {
      this.session = { ...this.session, signedIn: false };
      return true;
    }
    if (error?.code === "no_helper" || error?.code === "helper") {
      this.setupError = error;
      return true;
    }
    return false;
  }

  siteHost() {
    try { return new URL(this.session?.siteURL ?? "").host; } catch { return ""; }
  }

  // ---- loading -------------------------------------------------------------

  start(cx) {
    cx.spawn(async (cx) => {
      const s = await api("session");
      if (!s.ok) { this.setupError = s.error; cx.notify(); return; }
      this.setupError = null;
      this.session = s.data;
      if (s.data.signedIn) this.loadAll(cx);
      else if (s.data.login?.state === "waiting") this.watchLogin(cx);
      cx.notify();
    });
  }

  loadAll(cx) {
    this.loadTab("issues", "auto", cx);
    this.loadTab("epics", "auto", cx);
    this.ticketsKey = null;
    this.resolveTickets(cx);
    if (!this.me) {
      const g = this.gen.stamp("session");
      cx.spawn(async (cx) => {
        const r = await api("me");
        if (r.ok && this.gen.same("session", g)) { this.me = r.data; cx.notify(); }
      });
    }
  }

  // mode: "auto" (the cache when it is fresh, else Jira) or "refresh".
  // Whatever is cached paints first. A reply is dropped when the tab's list
  // was changed here (a transition, assign or star) while it was on its way,
  // since it was read before that change; the next tick catches up.
  loadTab(tab, mode, cx) {
    if (this.loading[tab]) return;
    this.loading[tab] = true;
    const session = this.gen.stamp("session");
    cx.spawn(async (cx) => {
      if (!this.data[tab]) {
        const g = this.gen.stamp(tab);
        const c = await api(tab, "--cached");
        if (c.ok && c.data.cached && this.gen.same(tab, g) && this.gen.same("session", session)) { this.data[tab] = c.data; cx.notify(); }
      }
      const g = this.gen.stamp(tab);
      const r = await api(tab, ...(mode === "refresh" ? ["--refresh"] : []));
      if (!this.gen.same("session", session)) return; // signing in or out reset loading
      this.loading[tab] = false;
      if (r.ok && !this.gen.same(tab, g)) {
        this.errors[tab] = null;
      } else if (r.ok) {
        this.data[tab] = r.data;
        this.errors[tab] = null;
      } else if (!this.authFailed(r.error)) {
        this.errors[tab] = r.error;
      }
      cx.notify();
    });
  }

  refresh(cx) {
    if (this.drill) { this.loadChildren(this.drill.epic.key, cx, true); return; }
    this.loadTab(this.tab, "refresh", cx);
    this.ticketsKey = null;
    this.resolveTickets(cx, true);
    const key = this.current()?.key;
    if (key) {
      this.loadDetail(key, cx);
      this.staleTransitions.add(key);
    }
  }

  tick(cx) {
    if (!this.session?.signedIn) return;
    for (const tab of ["issues", "epics"]) this.loadTab(tab, "auto", cx);
    this.ticketsKey = null;
    this.resolveTickets(cx);
  }

  // Which ticket each space's branches name (the helper confirms candidate
  // keys with Jira and caches the answers).
  resolveTickets(cx, force = false) {
    if (!this.session?.signedIn) return;
    const branches = M.branchesToResolve(spaces(), panes());
    const key = branches.join("\n");
    if (key === this.ticketsKey) return;
    this.ticketsKey = key;
    if (!branches.length) { this.tickets = {}; return; }
    this.gen.bump("tickets");
    const g = this.gen.stamp("tickets");
    const session = this.gen.stamp("session");
    cx.spawn(async (cx) => {
      const r = await api("tickets", ...branches.flatMap((b) => ["--branch", b]), ...(force ? ["--force"] : []));
      // A newer lookup (after a transition, say) answers instead.
      if (!this.gen.same("tickets", g) || !this.gen.same("session", session)) return;
      if (r.ok) {
        this.tickets = r.data.tickets ?? {};
        this.maybeAutoPin();
        cx.notify();
      } else this.authFailed(r.error);
    });
  }

  loadDetail(key, cx) {
    const prev = this.details.get(key);
    this.details.set(key, { ...prev, loading: true, error: null });
    const session = this.gen.stamp("session");
    const began = Date.now();
    cx.spawn(async (cx) => {
      const r = await api("issue", key);
      if (!this.gen.same("session", session)) return;
      if (r.ok) {
        this.details.set(key, { data: r.data, at: Date.now(), loading: false });
        this.reconcileComments(key, r.data, began);
        if (r.data.level > 0 && !this.children.has(key)) this.loadChildren(key, cx);
        // The menu may have opened on a list row's older status.
        if (this.menu?.key === key || this.sheet?.issue?.key === key) this.loadTransitions(key, cx);
      } else {
        this.details.set(key, { ...this.details.get(key), loading: false, error: r.error, failedAt: Date.now() });
        this.authFailed(r.error);
      }
      cx.notify();
    });
  }

  // Loads key's detail once the selection has rested on it for a moment, so
  // arrowing through the list doesn't fire a request per row.
  wantDetail(key, cx) {
    if (!key) return;
    const d = this.details.get(key);
    if (d?.loading || (d?.data && Date.now() - d.at < DETAIL_TTL) || (d?.error && Date.now() - (d.failedAt ?? 0) < 10_000)) return;
    if (this.detailWanted === key) return;
    this.detailWanted = key;
    cx.timer.after(d?.data ? 0 : 140, (cx) => {
      if (this.detailWanted !== key) return;
      this.detailWanted = null;
      this.loadDetail(key, cx);
    });
  }

  loadChildren(key, cx, force = false) {
    const c = this.children.get(key);
    if (c?.loading || (c?.list && !force)) return;
    this.children.set(key, { ...c, loading: true, error: null });
    const session = this.gen.stamp("session");
    cx.spawn(async (cx) => {
      const r = await api("children", key);
      if (!this.gen.same("session", session)) return;
      this.children.set(key, r.ok ? { list: r.data.children, loading: false } : { ...this.children.get(key), loading: false, error: r.error });
      if (!r.ok) this.authFailed(r.error);
      cx.notify();
    });
  }

  // The helper keeps each workflow step's transitions (project, issue type,
  // status) for a day, so it answers from disk when told the step: the
  // issue's detail when loaded, else its row. A list loaded for another
  // status than the issue has now is loaded again.
  loadTransitions(key, cx, issue = null) {
    const row = issue?.key === key ? issue : this.current();
    const at = this.details.get(key)?.data ?? (row?.key === key ? row : null);
    const tr = this.transitions.get(key);
    const force = this.staleTransitions.has(key);
    if (tr?.loading || (tr?.list && !force && tr.status === at?.status)) return;
    this.staleTransitions.delete(key);
    this.transitions.set(key, { loading: true });
    const hints = this.session?.api >= 2 ? [at?.type && `--type=${at.type}`, at?.status && `--status=${at.status}`].filter(Boolean) : [];
    const session = this.gen.stamp("session");
    cx.spawn(async (cx) => {
      const r = await api("transitions", key, ...hints, ...(force ? ["--refresh"] : []));
      if (!this.gen.same("session", session)) return;
      this.transitions.set(key, r.ok ? { list: r.data.transitions, status: at?.status } : { error: r.error });
      if (!r.ok) this.authFailed(r.error);
      cx.notify();
    });
  }

  // ---- sign-in -------------------------------------------------------------

  signIn(cx) {
    this.session = { ...this.session, login: { state: "waiting" } };
    cx.spawn(async (cx) => {
      const r = await api("login", "--force");
      if (!r.ok) {
        this.session = { ...this.session, login: { state: "error", error: r.error } };
      } else {
        this.session = { ...this.session, login: r.data };
        this.watchLogin(cx);
      }
      cx.notify();
    });
  }

  // Polls the helper while a sign-in waits on the browser. One poll runs at
  // a time, and polling stops if the helper stops answering.
  watchLogin(cx) {
    if (this.loginTimer) return;
    let polling = false;
    let failures = 0;
    const stop = () => { this.loginTimer?.cancel(); this.loginTimer = null; };
    this.loginTimer = cx.timer.every(1500, (cx) => {
      if (polling) return;
      polling = true;
      cx.spawn(async (cx) => {
        const s = await api("session");
        polling = false;
        if (!this.loginTimer) return;
        if (!s.ok) {
          if (++failures >= 5) {
            stop();
            this.session = { ...this.session, login: { state: "error", error: s.error } };
            cx.notify();
          }
          return;
        }
        failures = 0;
        const state = s.data.login?.state;
        if (state === "waiting") {
          this.session = { ...this.session, login: s.data.login };
        } else {
          stop();
          this.session = s.data;
          if (s.data.signedIn && state === "done") {
            this.gen.bump("session");
            this.loading = { issues: false, epics: false };
            this.data = { issues: null, epics: null };
            this.details.clear(); this.transitions.clear();
            this.loadAll(cx);
            this.say(`Signed in to ${this.siteHost()}`, "success", cx);
          }
        }
        cx.notify();
      });
    });
  }

  signOut(cx) {
    cx.spawn(async (cx) => {
      const r = await api("logout");
      if (r.ok) {
        this.gen.bump("session"); // replies still on their way are for the old sign-in
        this.loginTimer?.cancel();
        this.loginTimer = null;
        this.session = { signedIn: false };
        this.loading = { issues: false, epics: false };
        this.data = { issues: null, epics: null };
        this.details.clear(); this.children.clear(); this.transitions.clear(); this.staleTransitions.clear();
        this.tickets = {}; this.me = null; this.pinned = null; this.sheet = null;
      } else this.say(r.error.message, "error", cx);
      cx.notify();
    });
  }

  // ---- what's shown ----------------------------------------------------------

  rows() {
    const q = this.query.trim();
    if (this.drill) return M.childRows(this.drill.epic, this.children.get(this.drill.epic.key)?.list ?? [], q);
    const hits = q && this.hitsFor[this.tab] === q ? this.hits[this.tab] : null;
    return M.buildRows(this.tab, this.data[this.tab], q, hits);
  }

  selKey() { return this.drill ? `drill:${this.drill.epic.key}` : this.tab; }

  // The selected row's index, settling on the first selectable row.
  selIndex(rows) {
    const id = this.selected[this.selKey()];
    let i = rows.findIndex((r) => r.id === id);
    if (i < 0 || !M.selectable(rows[i])) {
      // Same issue in another lane (search hit becoming a lane row) keeps the selection.
      const key = id?.split(":").pop();
      i = rows.findIndex((r) => M.selectable(r) && r.issue.key === key);
    }
    if (i < 0) i = M.moveSelection(rows, -1, 1);
    if (i >= 0) this.selected[this.selKey()] = rows[i].id;
    return i;
  }

  // The issue the detail shows: one pinned from elsewhere, else the selection.
  current() {
    if (this.pinned) return this.pinned;
    const rows = this.rows();
    const i = this.selIndex(rows);
    return i >= 0 ? rows[i].issue : null;
  }

  // The ticket this space is on: the focused pane's branch first, then its
  // tab's other terminals (this view may itself be the focused pane), then the
  // branch the space shows.
  spaceTicket() {
    const f = focused();
    const all = panes();
    const pane = f?.pane ? all.find((p) => p.pane === f.pane) : null;
    const space = spaces().find((s) => s.active) ?? null;
    const tabs = new Set((space?.tabs ?? []).map((t) => t.id));
    const branches = [
      pane?.branch,
      ...all.filter((p) => p.tab === (pane?.tab ?? f?.tab) && p.branch).map((p) => p.branch),
      ...all.filter((p) => tabs.has(p.tab) && p.branch).map((p) => p.branch),
      space?.branch,
    ];
    for (const branch of branches) {
      if (branch && this.tickets[branch]) return { issue: this.tickets[branch], branch, space };
    }
    return null;
  }

  // On opening, show this space's ticket if the person hasn't picked anything.
  maybeAutoPin() {
    if (this.autoPinned) return;
    const here = this.spaceTicket();
    if (!here) return;
    this.autoPinned = true;
    if (!this.pinned && !this.touched) this.pinned = here.issue;
  }

  // ---- actions ---------------------------------------------------------------

  move(step, cx) {
    const rows = this.rows();
    let i = this.selIndex(rows);
    if (this.pinned) { this.pinned = null; if (step < 0) step = 0; }
    this.touched = true;
    if (step !== 0) i = M.moveSelection(rows, i, step);
    if (i < 0) return;
    this.selected[this.selKey()] = rows[i].id;
    this.scroll.scroll_to_item(i);
    this.menu = null;
  }

  select(id, cx) {
    const rows = this.rows();
    const i = rows.findIndex((r) => r.id === id);
    const r = rows[i];
    this.touched = true;
    if (r?.kind === "head" && this.drill && i === 0) return this.leaveDrill(cx);
    if (!M.selectable(r)) return;
    const again = !this.pinned && this.selected[this.selKey()] === id;
    this.pinned = null;
    this.menu = null;
    this.selected[this.selKey()] = id;
    if (!this.wide) { this.narrowDetail = true; return; }
    if (again && r.kind === "epic" && !this.drill) this.enterDrill(r.issue, cx);
  }

  open(cx) {
    const issue = this.current();
    if (!issue) return;
    if (!this.pinned && M.typeKind(issue) === "epic" && !this.drill && this.wide) return this.enterDrill(issue, cx);
    if (!this.wide) this.narrowDetail = true;
    if (this.typing) this.handle.focus();
  }

  pin(issue, cx) {
    this.touched = true;
    this.pinned = issue;
    this.menu = null;
    if (!this.wide) this.narrowDetail = true;
    this.wantDetail(issue.key, cx);
  }

  enterDrill(epic, cx) {
    this.drill = { epic, back: this.selected[this.tab] };
    this.selected[this.selKey()] = null;
    this.pinned = null;
    this.loadChildren(epic.key, cx);
    this.scroll.scroll_to_item(0);
  }

  leaveDrill() {
    if (!this.drill) return;
    this.drill = null;
    this.scroll.scroll_to_item(Math.max(0, this.rows().findIndex((r) => r.id === this.selected[this.tab])));
  }

  setTab(tab, cx) {
    if (tab === this.tab && !this.drill) return;
    this.drill = null;
    this.tab = tab;
    this.store("tab", tab);
    this.pinned = null;
    this.menu = null;
    this.touched = true;
    if (this.query.trim()) this.search(cx);
    this.scroll.scroll_to_item(Math.max(0, this.selIndex(this.rows())));
  }

  onQuery(cx) {
    const q = this.input.value();
    if (q === this.query) return;
    this.query = q;
    this.pinned = null;
    this.touched = true;
    this.scroll.scroll_to_item(0);
    this.search(cx);
    cx.notify();
  }

  // A remote search for the tab, once typing pauses. Older answers are dropped.
  search(cx) {
    const q = this.query.trim();
    const tab = this.drill ? null : this.tab;
    const seq = ++this.searchSeq;
    if (!q || !tab) return;
    cx.timer.after(SEARCH_DELAY, (cx) => {
      if (seq !== this.searchSeq) return;
      this.searching[tab] = seq;
      cx.notify();
      cx.spawn(async (cx) => {
        const r = await api("search", "--scope", tab, "--query", q);
        // The spinner is this search's until a newer one on the tab starts,
        // whether or not its answer is still wanted.
        if (this.searching[tab] === seq) this.searching[tab] = 0;
        if (seq !== this.searchSeq) { cx.notify(); return; }
        if (r.ok) { this.hits[tab] = r.data.hits; this.hitsFor[tab] = q; }
        else if (!this.authFailed(r.error)) this.say(`Search failed: ${r.error.message}`, "error", cx);
        cx.notify();
      });
    });
  }

  clearSearch() {
    this.input.set_value("");
    this.query = "";
    this.searchSeq++;
    this.searching = { issues: 0, epics: 0 };
  }

  // Updates an issue everywhere it is shown after a change in Jira.
  applyIssue(issue) {
    this.gen.bump("issues");
    this.gen.bump("epics");
    const patch = (list, dropDone) => (list ?? []).filter((i) => !(dropDone && i.key === issue.key && M.isDone(issue)))
      .map((i) => (i.key === issue.key ? { ...i, ...issue } : i));
    if (this.data.issues) this.data.issues = { ...this.data.issues, issues: patch(this.data.issues.issues, true), subtasks: patch(this.data.issues.subtasks, true) };
    if (this.data.epics) this.data.epics = { ...this.data.epics, epics: patch(this.data.epics.epics, true), starred: patch(this.data.epics.starred, false) };
    for (const tab of ["issues", "epics"]) if (this.hits[tab]) this.hits[tab] = patch(this.hits[tab], true);
    for (const [k, c] of this.children) if (c.list) this.children.set(k, { ...c, list: patch(c.list, false) });
    const d = this.details.get(issue.key);
    if (d?.data) this.details.set(issue.key, { ...d, data: { ...d.data, status: issue.status, category: issue.category, updated: issue.updated ?? d.data.updated } });
    for (const [b, t] of Object.entries(this.tickets)) if (t?.key === issue.key) this.tickets[b] = { ...t, ...issue };
    if (this.pinned?.key === issue.key) this.pinned = { ...this.pinned, ...issue };
  }

  openMenu(key, cx) {
    if (!key) return;
    this.menu = { key, at: 0 };
    this.loadTransitions(key, cx);
    if (!this.wide) this.narrowDetail = true;
    cx.timer.after(30, () => { if (this.menu?.key === key && !this.sheet) this.menuHandle.focus(); });
  }

  closeMenu() {
    this.menu = null;
    this.handle.focus();
  }

  transition(key, tr, cx) {
    this.closeMenu();
    if (this.busy.has(key)) return this.say(`${key} is still being updated`, "info", cx);
    this.busy.set(key, "status");
    cx.spawn(async (cx) => {
      const r = await api("transition", key, tr.id);
      this.busy.delete(key);
      if (!r.ok) {
        this.transitions.delete(key); // the helper forgot the list that offered it
        if (!this.authFailed(r.error)) this.say(`Couldn't move ${key}: ${r.error.message}`, "error", cx);
      } else {
        this.applyIssue(r.data.issue ?? { key, status: tr.to, category: tr.category });
        this.transitions.delete(key);
        this.say(`${key} is now ${r.data.issue?.status ?? tr.to}`, "success", cx);
        this.ticketsKey = null;
        this.resolveTickets(cx, true);
      }
      cx.notify();
    });
  }

  // ---- comments ---------------------------------------------------------------

  commentBox(key) {
    let b = this.boxes.get(key);
    if (b) return b;
    b = TextareaState.new({ placeholder: "Add a comment (markdown)" });
    b.set_auto_grow(3, 10);
    b.on("focus", (_e, cx) => { this.commenting = key; cx.notify(); });
    b.on("blur", (_e, cx) => { if (this.commenting === key) this.commenting = null; cx.notify(); });
    b.on("change", (_e, cx) => {
      const st = this.commentSt.get(key);
      if (st?.error) this.commentSt.set(key, { ...st, error: "" });
      cx.notify();
    });
    // Enter writes a new line; cmd/ctrl+enter posts.
    b.on("submit", (e, cx) => {
      if (e?.secondary) this.postComment(key, cx);
      else if (!e?.shift) b.replace("\n");
      cx.notify();
    });
    this.boxes.set(key, b);
    return b;
  }

  // `m`: the keyboard goes to the current issue's comment box.
  focusComment(cx) {
    const key = this.current()?.key;
    if (!key) return;
    this.commentBox(key);
    if (!this.wide) this.narrowDetail = true;
    if (!this.details.has(key)) this.loadDetail(key, cx);
    cx.timer.after(40, () => { if (this.current()?.key === key && !this.sheet && !this.menu) this.commentBox(key).focus(); });
  }

  leaveComment() {
    const b = this.commenting && this.boxes.get(this.commenting);
    if (b) b.blur(); // the draft stays in the box
    this.commenting = null;
    this.handle.focus();
  }

  // Drops the posted comments the fresh read of the issue now covers.
  reconcileComments(key, data, fetchedAt) {
    const list = this.pendingComments.get(key);
    if (!list?.length) return;
    const kept = M.stillPending(data?.comments, list, fetchedAt);
    if (kept.length) this.pendingComments.set(key, kept);
    else this.pendingComments.delete(key);
  }

  postComment(key, cx) {
    const box = this.commentBox(key);
    const text = box.value();
    if (!M.canPost(text) || this.commentSt.get(key)?.busy) return;
    const mine = { id: ++this.commentSeq, author: this.me?.name ?? "You", created: new Date().toISOString(), body: text };
    this.pendingComments.set(key, [...(this.pendingComments.get(key) ?? []), mine]);
    this.commentSt.set(key, { busy: true, error: "" });
    box.set_value("");
    const session = this.gen.stamp("session");
    cx.spawn(async (cx) => {
      const r = await api("comment", key, "--json", JSON.stringify({ body: text }));
      if (!this.gen.same("session", session)) return;
      if (!r.ok) {
        // Back into the box (ahead of anything typed since): the text is never lost.
        this.pendingComments.set(key, (this.pendingComments.get(key) ?? []).filter((p) => p.id !== mine.id));
        const now = box.value();
        box.set_value(now.trim() ? `${text}\n\n${now}` : text);
        this.commentSt.set(key, { busy: false, error: M.commentError(r.error) });
        this.authFailed(r.error);
      } else {
        this.commentSt.set(key, { busy: false, error: "" });
        this.pendingComments.set(key, (this.pendingComments.get(key) ?? []).map((p) => (p.id === mine.id ? { ...p, postedAt: Date.now() } : p)));
        if (r.data.issue) {
          this.details.set(key, { data: r.data.issue, at: Date.now(), loading: false });
          this.reconcileComments(key, r.data.issue, Date.now());
        } else this.loadDetail(key, cx);
        this.say(`Commented on ${key}`, "success", cx);
      }
      cx.notify();
    });
  }

  assign(key, cx) {
    if (!key || this.busy.has(key)) return;
    const d = this.details.get(key)?.data;
    if (d?.mine) return this.say(`${key} is already yours`, "info", cx);
    this.busy.set(key, "assign");
    cx.spawn(async (cx) => {
      const r = await api("assign", key);
      this.busy.delete(key);
      if (!r.ok) {
        if (!this.authFailed(r.error)) this.say(`Couldn't assign ${key}: ${r.error.message}`, "error", cx);
      } else {
        const d = this.details.get(key);
        if (d?.data) this.details.set(key, { ...d, data: { ...d.data, mine: true, assignee: this.me?.name ?? "You" } });
        const issue = r.data.issue;
        if (issue && !M.isDone(issue)) this.addMine(issue);
        this.say(`Assigned ${key} to you`, "success", cx);
      }
      cx.notify();
    });
  }

  addMine(issue) {
    this.gen.bump(issue.level > 0 ? "epics" : "issues");
    const top = (list) => [issue, ...(list ?? []).filter((i) => i.key !== issue.key)];
    if (issue.level > 0) { if (this.data.epics) this.data.epics = { ...this.data.epics, epics: top(this.data.epics.epics) }; }
    else if (issue.level < 0) { if (this.data.issues) this.data.issues = { ...this.data.issues, subtasks: top(this.data.issues.subtasks) }; }
    else if (this.data.issues) this.data.issues = { ...this.data.issues, issues: top(this.data.issues.issues) };
  }

  star(issue, cx) {
    if (!issue || M.typeKind(issue) !== "epic") return this.say("Only epics can be starred", "info", cx);
    const e = this.data.epics ?? { epics: [], starred: [], starredKeys: [] };
    const was = (e.starredKeys ?? []).includes(issue.key);
    const apply = (on) => {
      const keys = (this.data.epics?.starredKeys ?? []).filter((k) => k !== issue.key);
      const starred = (this.data.epics?.starred ?? []).filter((i) => i.key !== issue.key);
      this.gen.bump("epics");
      this.data.epics = { ...(this.data.epics ?? e), starredKeys: on ? [...keys, issue.key] : keys, starred: on ? [...starred, issue] : starred };
      const d = this.details.get(issue.key);
      if (d?.data) this.details.set(issue.key, { ...d, data: { ...d.data, starred: on } });
    };
    apply(!was);
    const row = { key: issue.key, summary: issue.summary, status: issue.status, category: issue.category, type: issue.type, level: issue.level };
    cx.spawn(async (cx) => {
      const r = await api("star", issue.key, "--row", JSON.stringify(row));
      if (!r.ok) {
        apply(was);
        if (!this.authFailed(r.error)) this.say(`Couldn't save the star: ${r.error.message}`, "error", cx);
      } else this.say(r.data.starred ? `Starred ${issue.key}` : `Unstarred ${issue.key}`, "info", cx);
      cx.notify();
    });
  }

  copyKey(key, cx) {
    if (!key) return;
    cx.write_to_clipboard(key);
    this.say(`Copied ${key}`, "info", cx);
  }

  browse(key, cx) {
    if (!key || !this.session?.siteURL) return;
    cx.open_url(`${this.session.siteURL}/browse/${key}`);
  }

  openAsPane() {
    run("pane.split", { direction: "right", mod: "jira", props: {} });
    if (this.overlay) closeOverlay(this.overlay);
  }

  close() {
    if (this.overlay) closeOverlay(this.overlay);
  }

  // Commands from keys.json ("cmd-shift-j": "jira.ticket") or `herdr-app do
  // jira.<command>`, for a shown copy: the key names below, or key:<key>.
  command(c, cx) {
    if (c === "focus") return this.handle.focus();
    if (c === "search") return this.input.focus();
    if (c.startsWith("search:")) {
      this.input.set_value(c.slice(7));
      this.onQuery(cx);
      return this.input.focus();
    }
    if (c.startsWith("key:")) return this.handleKey(c.slice(4), cx);
    // Named commands act directly, whatever has the keyboard (a key typed in
    // the search box is text; a bound command is not).
    const issue = () => this.current();
    const actions = {
      refresh: () => this.refresh(cx),
      ticket: () => this.handleKeyBrowsing("i", cx),
      status: () => this.openMenu(issue()?.key, cx),
      assign: () => this.assign(issue()?.key, cx),
      start: () => this.openStart(issue(), cx),
      star: () => this.star(issue(), cx),
      copy: () => this.copyKey(issue()?.key, cx),
      browse: () => this.browse(issue()?.key, cx),
      next: () => this.move(1, cx),
      prev: () => this.move(-1, cx),
      open: () => this.open(cx),
      back: () => this.handleKey("escape", cx),
      issues: () => this.setTab("issues", cx),
      epics: () => this.setTab("epics", cx),
      keys: () => { this.sheet = { kind: "help" }; },
    };
    actions[c]?.();
  }

  // ---- Start work ------------------------------------------------------------

  openStart(issue, cx) {
    if (!issue) return;
    const d = this.details.get(issue.key)?.data;
    const space = currentSpace();
    const sheet = {
      kind: "start", issue, space, repo: undefined, home: null, error: "", busy: false,
      mode: "worktree", assign: d ? !d.mine : true, progress: issue.category === "new", agent: null, agents: [],
      input: InputState.new({ value: M.branchName(issue.key, issue.summary), placeholder: "branch name" }),
    };
    sheet.input.on("change", (_e, cx) => { sheet.error = ""; cx.notify(); });
    sheet.input.on("submit", (_e, cx) => this.runStart(cx));
    this.sheet = sheet;
    this.menu = null;
    cx.timer.after(60, () => sheet.input.focus());
    this.loadTransitions(issue.key, cx, issue);
    if (!d) this.wantDetail(issue.key, cx);
    cx.spawn(async (cx) => {
      try {
        const on = space?.machine ?? null;
        sheet.home = on ? await remoteHome(on) : home();
        sheet.repo = await repoOf(spaceFolder(space), on);
        if (sheet.repo === null) sheet.mode = null;
        try { sheet.agents = (await knownAgents({ on })).filter((a) => a.installed); } catch { sheet.agents = []; }
        const last = this.stored("agent");
        sheet.agent = last ? sheet.agents.find((a) => a.key === last) ?? null : null;
      } catch (e) {
        sheet.repo = null;
        sheet.error = String(e?.message ?? e);
      }
      cx.notify();
    });
  }

  // The transition the sheet's "Move to In Progress" uses: one named In
  // Progress, else the first that lands in an in-progress status.
  progressTransition(key, category) {
    if (category && category !== "new") return null; // already under way, or done
    const list = this.transitions.get(key)?.list ?? [];
    return list.find((t) => /^in progress$/i.test(t.to)) ?? list.find((t) => t.category === "indeterminate") ?? null;
  }

  // An issue's status category as last seen anywhere (it may have moved
  // since the row was built).
  currentCategory(issue) {
    return this.details.get(issue.key)?.data?.category ?? issue.category;
  }

  runStart(cx) {
    const s = this.sheet;
    if (!s || s.kind !== "start" || s.busy) return;
    const branch = s.input.value().trim();
    if (!s.mode && !s.agent) { s.error = "Not in a git repository: pick an agent to start, or open Jira from a repo's space."; return cx.notify(); }
    if (s.mode && !M.validBranch(branch)) { s.error = "That isn't a valid branch name."; return cx.notify(); }
    if (s.mode && !s.repo) { s.error = "Still looking for the repository…"; return cx.notify(); }
    s.busy = true;
    s.error = "";
    const issue = s.issue;
    const detail = this.details.get(issue.key)?.data ?? issue;
    const tr = s.progress ? this.progressTransition(issue.key, this.currentCategory(issue)) : null;
    if (s.agent) this.store("agent", s.agent.key);
    cx.spawn(async (cx) => {
      const did = [];
      try {
        if (s.mode) {
          did.push(await startBranch({
            mode: s.mode, branch, repo: s.repo, on: s.space?.machine ?? null, home: s.home, space: s.space,
            agent: s.agent, task: M.agentTask({ ...detail, url: detail.url ?? `${this.session.siteURL}/browse/${issue.key}` }), tabName: issue.key,
          }));
        } else if (s.agent) {
          run("tab.new", { space: s.space?.id, name: issue.key, argv: s.agent.run, task: M.agentTask(detail) });
          did.push(`started ${s.agent.name}`);
        }
      } catch (e) {
        s.busy = false;
        s.error = String(e?.message ?? e);
        cx.notify();
        return;
      }
      if (s.mode && s.agent) did.push(`${s.agent.name} is on it`);
      if (s.assign && !this.details.get(issue.key)?.data?.mine) {
        const r = await api("assign", issue.key);
        if (r.ok) { did.push("assigned to you"); if (r.data.issue) this.addMine(r.data.issue); const d = this.details.get(issue.key); if (d?.data) this.details.set(issue.key, { ...d, data: { ...d.data, mine: true, assignee: this.me?.name ?? "You" } }); }
        else did.push(`not assigned (${r.error.message})`);
      }
      if (tr) {
        const r = await api("transition", issue.key, tr.id);
        if (r.ok) { this.applyIssue(r.data.issue ?? { key: issue.key, status: tr.to, category: tr.category }); this.transitions.delete(issue.key); did.push(`moved to ${tr.to}`); }
        else { this.transitions.delete(issue.key); did.push(`not moved (${r.error.message})`); }
      }
      this.sheet = null;
      const text = `${issue.key}: ${did.join(", ")}`;
      notify({ title: `Started ${issue.key}`, body: did.join(", "), level: "success", timeout: 6000 });
      this.say(text, "success", cx);
      this.ticketsKey = null;
      this.resolveTickets(cx, true);
      if (this.overlay && (s.mode === "worktree" || s.agent)) this.close();
      else this.handle.focus();
      cx.notify();
    });
    cx.notify();
  }

  // ---- keys --------------------------------------------------------------------

  onKey(e, cx) {
    const mod = e.modifiers ?? {};
    if (mod.platform || mod.control || mod.alt) return; // app shortcuts
    if (this.handleKey(e.key, cx)) {
      cx.stop_propagation();
      cx.notify();
    }
  }

  // One key, from the keyboard or a `jira.key:<k>` command. Returns whether
  // it did something (so the event stops here).
  handleKey(k, cx) {
    if (this.sheet) {
      if (k === "escape") { if (!this.sheet.busy) { this.sheet = null; this.handle.focus(); } return true; }
      if (this.sheet.kind === "help") { this.sheet = null; return true; }
      if (this.sheet.kind === "start" && k === "enter") { this.runStart(cx); return true; }
      return false;
    }
    if (this.menu) return this.menuKey(k, cx);

    if (this.commenting) {
      if (k === "escape") { this.leaveComment(); return true; }
      return false; // typing
    }

    if (this.typing) {
      if (k === "escape") {
        if (this.query) this.clearSearch();
        this.handle.focus();
        return true;
      }
      if (k === "down" || k === "up") { this.move(k === "down" ? 1 : -1, cx); return true; }
      if (k === "tab") { this.setTab(this.tab === "issues" ? "epics" : "issues", cx); return true; }
      return false; // typing
    }

    const issue = this.current();
    const key = issue?.key;
    switch (k) {
      case "down": case "j": this.move(1, cx); break;
      case "up": case "k": this.move(-1, cx); break;
      case "enter": this.open(cx); break;
      case "/": this.input.focus(); break;
      case "1": this.setTab("issues", cx); break;
      case "2": this.setTab("epics", cx); break;
      case "tab": this.setTab(this.tab === "issues" ? "epics" : "issues", cx); break;
      case "t": this.openMenu(key, cx); break;
      case "a": this.assign(key, cx); break;
      case "b": this.openStart(issue, cx); break;
      case "m": if (!issue) return false; this.focusComment(cx); break;
      case "s": this.star(issue, cx); break;
      case "c": this.copyKey(key, cx); break;
      case "o": this.browse(key, cx); break;
      case "r": this.refresh(cx); break;
      case "i": this.handleKeyBrowsing("i", cx); break;
      case "p": if (!this.overlay) return false; this.openAsPane(); break;
      case "?": this.sheet = { kind: "help" }; break;
      case "left": case "backspace":
        if (!this.wide && this.narrowDetail) this.narrowDetail = false;
        else if (this.drill) this.leaveDrill(cx);
        else return false;
        break;
      case "escape":
        if (!this.wide && this.narrowDetail) this.narrowDetail = false;
        else if (this.pinned) this.pinned = null;
        else if (this.drill) this.leaveDrill(cx);
        else if (this.query) this.clearSearch();
        else if (this.overlay) this.close();
        else return false;
        break;
      default:
        return false;
    }
    return true;
  }

  // The keys that also act as commands, whatever the mode.
  handleKeyBrowsing(k, cx) {
    if (k === "i") {
      const here = this.spaceTicket();
      if (here) this.pin(here.issue, cx);
      else this.say("This space's branch doesn't name a Jira issue", "info", cx);
    }
  }

  menuKey(k, cx) {
    const tr = this.transitions.get(this.menu?.key)?.list ?? [];
    if (k === "escape") this.closeMenu();
    else if (k === "down" || k === "j") this.menu.at = tr.length ? (this.menu.at + 1) % tr.length : 0;
    else if (k === "up" || k === "k") this.menu.at = tr.length ? (this.menu.at + tr.length - 1) % tr.length : 0;
    else if (k === "enter") { const t = tr[this.menu.at]; if (t) this.transition(this.menu.key, t, cx); }
    else return false;
    return true;
  }

  onMenuKey(e, cx) {
    if (this.menuKey(e.key, cx)) {
      cx.stop_propagation();
      cx.notify();
    }
  }

  // ---- render ------------------------------------------------------------------

  render(cx) {
    const t = theme();
    const width = this.size?.width ?? (this.overlay ? 1000 : 420);
    this.wide = width >= WIDE;

    let body;
    if (this.setupError) body = this.setupView(t);
    else if (!this.session) body = this.centered(t, v_flex().items_center().gap(10).child(icon("spinner").size(20).text_color(t.dim).spin(900)).child(div().text_color(t.dim).child("Connecting to Jira…")));
    else if (!this.session.signedIn) body = this.signInView(t);
    else {
      this.resolveTickets(cx);
      body = this.mainView(t, cx, width);
    }

    return v_flex().id("jira-root").track_focus(this.handle).size_full().min_h(0).relative()
      .bg(this.overlay ? t.popover : t.background).text_color(t.foreground).text_size(13)
      .on_size_change((size, cx) => { this.size = size; cx.notify(); })
      .on_key_down((e, cx) => this.onKey(e, cx))
      .on_mouse_down("left", (_e, cx) => { if (this.menu) { this.closeMenu(); cx.notify(); } })
      .child(this.header(t))
      .child(body)
      .when(Boolean(this.sheet), (el) => el.child(this.sheetView(t, cx)));
  }

  centered(t, content) {
    return v_flex().flex_1().min_h(0).items_center().justify_center().p(24).child(content);
  }

  header(t) {
    const signedIn = this.session?.signedIn;
    const loading = this.loading.issues || this.loading.epics || this.searching.issues || this.searching.epics;
    const fetched = this.data[this.tab]?.fetchedAt;
    return h_flex().flex_none().h(48).px(16).gap(10).items_center().border_b(1).border_color(t.border)
      .child(div().size(26).flex_none().rounded(7).bg("#2684ff").flex().items_center().justify_center()
        .child(icon("ticket").size(16).text_color("#ffffff")))
      .child(v_flex().min_w(0).gap(0)
        .child(h_flex().gap(6).items_center().child(div().text_size(14).font_semibold().child("Jira"))
          .when(Boolean(this.session?.demo), (el) => el.child(div().px(5).h(16).rounded(4).bg(t.raised).flex().items_center().text_size(9.5).font_semibold().text_color(t.muted).child("DEMO"))))
        .when(Boolean(signedIn && this.siteHost()), (el) => el.child(div().text_size(11).text_color(t.dim).truncate().child(this.siteHost()))))
      .child(div().flex_1())
      .when(Boolean(signedIn && fetched && this.wide), (el) => el.child(div().text_size(11).text_color(t.dim).child(`Updated ${M.ago(fetched) === "now" ? "just now" : M.ago(fetched) + " ago"}`)))
      .when(Boolean(signedIn), (el) => el.child(button(t, "jira-refresh", { iconName: "refresh", kind: "ghost", busy: loading, tooltip: "Refresh (r)", onClick: (_e, cx) => { this.refresh(cx); cx.notify(); } })))
      .when(Boolean(this.overlay && signedIn), (el) => el.child(button(t, "jira-pane", { iconName: "pane", kind: "ghost", tooltip: "Open as a pane (p)", onClick: () => this.openAsPane() })))
      .child(button(t, "jira-help", { iconName: "keyboard", kind: "ghost", tooltip: "Keys (?)", onClick: (_e, cx) => { this.sheet = { kind: "help" }; cx.notify(); } }))
      .when(Boolean(signedIn && this.me), (el) => el.child(div().id("jira-me").cursor_pointer().tooltip(`${this.me.name}${this.me.email ? ` · ${this.me.email}` : ""}`).child(avatar(this.me.name, 24))))
      .when(Boolean(this.overlay), (el) => el.child(button(t, "jira-close", { iconName: "x", kind: "ghost", tooltip: "Close (esc)", onClick: () => this.close() })));
  }

  setupView(t) {
    const old = this.setupError?.code === "helper";
    return this.centered(t, v_flex().max_w(460).gap(14).items_center()
      .child(div().size(44).rounded(12).bg(t.raised).flex().items_center().justify_center().child(icon("alert").size(22).text_color(t.blocked)))
      .child(div().text_size(16).font_semibold().child(old ? "The Jira helper needs an update" : "Install the Jira helper"))
      .child(div().text_center().text_color(t.muted).child(old
        ? this.setupError.message
        : "This mod talks to Jira through herdr-jira, a small program that signs you in to Atlassian and calls Jira Cloud. Build it from the herdr-jira repo, then let it install itself here:"))
      .child(div().px(12).py(8).rounded(8).bg(t.raised).border_1().border_color(t.border).font_family("JetBrains Mono").text_size(12)
        .child("sh scripts/build.sh && ./bin/herdr-jira app install"))
      .child(button(t, "jira-retry", { label: "Try again", iconName: "refresh", onClick: (_e, cx) => { this.setupError = null; this.start(cx); cx.notify(); } })));
  }

  signInView(t) {
    const login = this.session?.login;
    const waiting = login?.state === "waiting";
    const error = login?.state === "error" ? login.error : null;
    const messages = {
      consent_blocked: "Sign-in was refused. Your organisation may block third-party MCP clients; ask your Jira admin.",
      multiple_sites: error?.message,
      no_site: "This Atlassian account has no Jira Cloud site.",
      not_cloud: "This site isn't Jira Cloud. Only Jira Cloud is supported.",
      timeout: "Sign-in timed out. Try again.",
    };
    return this.centered(t, v_flex().w(420).max_w_full().gap(16).items_center()
      .child(div().size(52).rounded(14).bg("#2684ff").flex().items_center().justify_center().child(icon("ticket").size(28).text_color("#ffffff")))
      .child(div().text_size(18).font_semibold().child("Connect Jira"))
      .child(div().text_center().text_color(t.muted).child("Sign in with your Atlassian account in the browser. Your issues come straight from Jira Cloud: no API token to make, and no AI in between."))
      .when(Boolean(error), (el) => el.child(h_flex().gap(8).px(12).py(9).rounded(8).bg(t.blocked + "1f").text_color(t.foreground).text_size(12)
        .child(icon("alert").size(14).flex_none().text_color(t.blocked)).child(div().flex_1().child(messages[error?.code] ?? error?.message ?? "Sign-in failed."))))
      .child(waiting
        ? v_flex().gap(10).items_center()
          .child(h_flex().gap(8).items_center().text_color(t.muted).child(icon("spinner").size(14).spin(900).text_color(t.accent)).child("Waiting for you to approve in your browser…"))
          .when(Boolean(login?.url), (el) => el.child(button(t, "jira-reopen", { label: "Open the sign-in page again", iconName: "external", kind: "ghost", size: "sm", onClick: (_e, cx) => cx.open_url(login.url) })))
        : button(t, "jira-signin", { label: "Sign in with Atlassian", iconName: "arrow-right", kind: "brand", onClick: (_e, cx) => this.signIn(cx) })));
  }

  // ---- main view ---------------------------------------------------------------

  mainView(t, cx, width) {
    const listW = this.wide ? Math.round(Math.max(360, Math.min(520, width * 0.44))) : width;
    const issue = this.current();
    if (issue && (this.wide || this.narrowDetail)) this.wantDetail(issue.key, cx);
    const showList = this.wide || !this.narrowDetail;
    const showDetail = this.wide || this.narrowDetail;
    return v_flex().flex_1().min_h(0)
      .child(h_flex().flex_1().min_h(0)
        .when(showList, (el) => el.child(this.listColumn(t, cx, listW)))
        .when(showDetail, (el) => el.child(v_flex().flex_1().min_w(0).min_h(0).h_full().when(this.wide, (e) => e.border_l(1).border_color(t.border).bg(t.surface))
          .child(issue ? this.detailView(t, issue, cx) : this.emptyDetail(t)))))
      .child(this.footer(t));
  }

  listColumn(t, cx, w) {
    const rows = this.rows();
    const sel = this.pinned ? -1 : this.selIndex(rows);
    const here = this.spaceTicket();
    const byTicket = M.spacesByTicket(spaces(), panes(), this.tickets);
    const sizes = rows.map((r) => ROW_H[r.kind] ?? 36);
    const err = this.errors[this.tab];
    const loadingFirst = !this.data[this.tab] && this.loading[this.tab];
    return v_flex().w(w).flex_none().min_h(0).h_full()
      .when(Boolean(here), (el) => el.child(this.spaceCard(t, here)))
      .child(this.tabsBar(t))
      .child(this.searchBox(t))
      .when(Boolean(this.drill), (el) => el.child(this.drillBar(t)))
      .when(Boolean(err), (el) => el.child(h_flex().mx(12).mb(6).px(10).py(7).gap(8).rounded(7).bg(t.blocked + "1a").text_size(12)
        .child(icon("alert").size(13).flex_none().text_color(t.blocked)).child(div().flex_1().min_w(0).child(err.message))))
      .child(loadingFirst
        ? v_flex().flex_1().p(12).gap(8).children([0, 1, 2, 3, 4].map((i) => this.skeletonRow(t, i)))
        : div().flex_1().min_h(0).relative()
          .child(v_virtual_list("jira-list", rows.length, sizes, (i) => rows[i].id,
            (range) => rows.slice(range.start, range.end).map((r, j) => this.rowView(t, r, range.start + j === sel, byTicket)))
            .track_scroll(this.scroll).size_full().pb(8)
            .on_item_click((id, cx) => { this.select(id, cx); this.handle.focus(); cx.notify(); }))
          .child(Scrollbar.vertical("jira-list").absolute().inset_0()));
  }

  skeletonRow(t, i) {
    return h_flex().h(28).gap(10).items_center().px(8)
      .child(div().size(16).rounded(4).bg(t.raised))
      .child(div().w(52).h(10).rounded(3).bg(t.raised))
      .child(div().w(80 + ((i * 47) % 120)).h(10).rounded(3).bg(t.raised))
      .loop("opacity", { from: 0.45, to: 1, period: 1100 });
  }

  spaceCard(t, here) {
    const { issue, space } = here;
    const on = this.pinned?.key === issue.key;
    return v_flex().id("jira-here").mx(12).mt(12).px(12).py(10).gap(6).rounded(9).cursor_pointer()
      .border_1().border_color(on ? t.accent : t.border).bg(on ? t.selected : t.background)
      .hover((s) => s.border_color(t.accent))
      .on_click((_e, cx) => { this.pin(issue, cx); this.handle.focus(); cx.notify(); })
      .child(h_flex().gap(6).items_center().text_size(10.5).font_semibold().text_color(t.dim)
        .child(div().size(7).rounded(2).bg(spaceColor(t, space)))
        .child(div().child("THIS SPACE"))
        .child(div().truncate().font_normal().child(space?.name && space.name !== here.branch ? `· ${space.name} · ${here.branch}` : `· ${here.branch}`)))
      .child(h_flex().gap(8).items_center()
        .child(typeTile(issue, 16))
        .child(div().flex_none().text_size(12).text_color(t.muted).child(issue.key))
        .child(div().flex_1().min_w(0).truncate().font_medium().child(issue.summary))
        .child(statusPill(t, issue.status, issue.category)));
  }

  tabsBar(t) {
    const count = (tab) => {
      const d = this.data[tab];
      if (!d) return "";
      if (tab === "issues") return M.countLabel((d.issues?.length ?? 0) + (d.subtasks?.length ?? 0), d.issuesMore || d.subtasksMore);
      const keys = new Set((d.epics ?? []).map((e) => e.key));
      const starred = (d.starred ?? []).filter((e) => !M.isDone(e) && !keys.has(e.key) && (d.starredKeys ?? []).includes(e.key));
      return M.countLabel((d.epics?.length ?? 0) + starred.length, d.more);
    };
    const tabEl = (tab, label, n) => {
      const on = this.tab === tab;
      return h_flex().id(`jira-tab-${tab}`).flex_1().h(28).gap(6).items_center().justify_center().rounded(6).cursor_pointer()
        .text_size(12.5).font_medium()
        .when(on, (e) => e.bg(t.background).shadow_sm().text_color(t.foreground).border_1().border_color(t.border))
        .when(!on, (e) => e.text_color(t.muted).hover((s) => s.text_color(t.foreground)))
        .on_click((_e, cx) => { this.setTab(tab, cx); this.handle.focus(); cx.notify(); })
        .child(label)
        .when(n !== "", (e) => e.child(div().px(6).h(16).rounded_full().bg(on ? t.raised : t.hover).flex().items_center().text_size(10.5).text_color(t.muted).child(n)));
    };
    return h_flex().mx(12).mt(12).p(3).gap(3).rounded(9).bg(t.raised)
      .child(tabEl("issues", "Issues", count("issues")))
      .child(tabEl("epics", "Epics", count("epics")));
  }

  searchBox(t) {
    const busy = this.searching[this.tab] && Boolean(this.query.trim());
    return h_flex().id("jira-search").mx(12).mt(10).mb(6).h(34).px(10).gap(8).items_center().rounded(8)
      .border_1().border_color(this.typing ? t.accent : t.border).bg(t.background).cursor_text()
      .on_click(() => this.input.focus())
      .child(icon("search").size(14).flex_none().text_color(t.dim))
      .child(Input.new(this.input).flex_1().h_full().text_size(12.5).text_color(t.foreground))
      .when(busy, (el) => el.child(icon("spinner").size(13).text_color(t.dim).spin(900)))
      .when(!this.typing && !this.query, (el) => el.child(kbd(t, "/")))
      .when(Boolean(this.query) && !busy, (el) => el.child(div().id("jira-clear").cursor_pointer().text_color(t.dim).hover((s) => s.text_color(t.foreground))
        .on_click((_e, cx) => { cx.stop_propagation(); this.clearSearch(); this.handle.focus(); cx.notify(); }).child(icon("x").size(13))));
  }

  drillBar(t) {
    const epic = this.drill.epic;
    return h_flex().id("jira-drill").mx(12).mb(4).h(30).px(8).gap(8).items_center().rounded(7).cursor_pointer()
      .bg(t.raised).hover((s) => s.bg(t.hover))
      .on_click((_e, cx) => { this.leaveDrill(cx); cx.notify(); })
      .child(icon("chevron-left").size(14).text_color(t.muted))
      .child(typeTile(epic, 14))
      .child(div().text_size(12).text_color(t.muted).child(epic.key))
      .child(div().flex_1().min_w(0).truncate().text_size(12).child(epic.summary))
      .when(Boolean(this.children.get(epic.key)?.loading), (el) => el.child(icon("spinner").size(12).text_color(t.dim).spin(900)));
  }

  rowView(t, r, on, byTicket) {
    if (r.kind === "head") {
      const label = r.lane === M.LANE_CHILDREN ? `Children of ${r.epic?.key ?? "epic"}` : r.lane;
      return h_flex().h(ROW_H.head).px(18).pb(5).gap(7).items_end()
        .child(div().text_size(10.5).font_semibold().text_color(t.dim).child(label.toUpperCase()))
        .child(div().text_size(10.5).text_color(t.dim).child(M.countLabel(r.count, r.more)))
        .when(r.lane === M.LANE_RESULTS && this.searching[this.tab], (el) => el.child(icon("spinner").size(10).text_color(t.dim).spin(900)));
    }
    if (r.kind === "note") {
      return h_flex().h(ROW_H.note).px(18).items_center().text_size(12).text_color(t.dim).child(div().truncate().child(r.text));
    }
    const i = r.issue;
    const inSpaces = byTicket.get(i.key) ?? [];
    const busy = this.busy.has(i.key);
    const narrow = (this.size?.width ?? 400) < 430;
    return h_flex().h(ROW_H[r.kind]).mx(6).pl(10 + r.depth * 20).pr(10).gap(8).items_center().rounded(7)
      .when(on, (el) => el.bg(t.selected))
      .when(!on, (el) => el.hover((s) => s.bg(t.hover)))
      .when(r.depth > 0, (el) => el.child(div().w(8).h(14).flex_none().border_l(1).border_b(1).border_color(t.border).rounded(2).mb(7)))
      .child(typeTile(i, r.kind === "subtask" ? 14 : 16))
      .child(div().flex_none().text_size(12).text_color(on ? t.foreground : t.muted).child(i.key))
      .child(div().flex_1().min_w(0).truncate().text_color(t.foreground).when(on, (e) => e.font_medium()).child(i.summary))
      .when(inSpaces.length > 0 && !narrow, (el) => el.child(spaceChip(t, inSpaces[0])))
      .when(r.starred, (el) => el.child(icon("star-fill").size(12).flex_none().text_color("#f5b83d")))
      .when(!narrow && Boolean(i.priority), (el) => el.child(priorityMark(i.priority, 13)))
      .when(busy, (el) => el.child(icon("spinner").size(12).text_color(t.dim).spin(900)))
      .child(statusPill(t, i.status, i.category));
  }

  emptyDetail(t) {
    const loading = this.loading[this.tab] && !this.data[this.tab];
    return this.centered(t, v_flex().items_center().gap(8).text_color(t.dim)
      .child(icon("ticket").size(28).text_color(t.border))
      .child(loading ? "Loading your issues…" : "Nothing selected"));
  }

  // ---- detail ------------------------------------------------------------------

  detailView(t, issue, cx) {
    const entry = this.details.get(issue.key);
    const d = entry?.data;
    const shown = { ...issue, ...(d ?? {}) };
    const kind = M.typeKind(shown);
    const busy = this.busy.get(issue.key);
    const byTicket = M.spacesByTicket(spaces(), panes(), this.tickets).get(issue.key) ?? [];
    const kids = kind === "epic" ? this.children.get(issue.key) : null;

    const crumbs = h_flex().gap(6).items_center().text_size(12).text_color(t.muted).min_w(0)
      .when(!this.wide, (el) => el.child(div().id("jira-back").cursor_pointer().mr(2).text_color(t.muted).hover((s) => s.text_color(t.foreground))
        .on_click((_e, cx) => { this.narrowDetail = false; cx.notify(); }).child(icon("chevron-left").size(16))))
      .when(Boolean(d?.projectName), (el) => el.child(div().truncate().max_w(160).child(d.projectName)).child(div().text_color(t.dim).child("/")))
      .when(Boolean(shown.parent), (el) => el.child(h_flex().id("jira-parent").gap(5).items_center().cursor_pointer().hover((s) => s.text_color(t.foreground))
        .on_click((_e, cx) => { this.pin({ key: shown.parent, summary: shown.parentSummary ?? "", type: shown.parentType ?? "", level: (shown.parentType ?? "").toLowerCase() === "epic" ? 1 : 0, status: "", category: "" }, cx); cx.notify(); })
        .child(typeTile({ type: shown.parentType ?? "", level: (shown.parentType ?? "").toLowerCase() === "epic" ? 1 : 0 }, 14)).child(shown.parent))
        .child(div().text_color(t.dim).child("/")))
      .child(h_flex().gap(5).items_center().child(typeTile(shown, 14)).child(div().text_color(t.foreground).font_medium().child(issue.key)));

    const tools = h_flex().gap(2).items_center()
      .when(kind === "epic", (el) => el.child(button(t, "jira-star", { iconName: shown.starred || (this.data.epics?.starredKeys ?? []).includes(issue.key) ? "star-fill" : "star", kind: "ghost", tooltip: "Star (s)", onClick: (_e, cx) => { this.star(shown, cx); cx.notify(); } })))
      .child(button(t, "jira-copy", { iconName: "copy", kind: "ghost", tooltip: "Copy key (c)", onClick: (_e, cx) => { this.copyKey(issue.key, cx); cx.notify(); } }))
      .child(button(t, "jira-open", { iconName: "external", kind: "ghost", tooltip: "Open in Jira (o)", onClick: (_e, cx) => this.browse(issue.key, cx) }));

    const menuOpen = this.menu?.key === issue.key;
    const statusTrigger = div().id("jira-status").cursor_pointer().hover((s) => s.opacity(0.85))
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .on_click((_e, cx) => { if (menuOpen) this.closeMenu(); else this.openMenu(issue.key, cx); cx.notify(); })
      .child(busy === "status" ? h_flex().h(24).px(9).gap(6).items_center().rounded(6).bg(t.raised).text_size(11.5).text_color(t.muted).child(icon("spinner").size(12).spin(900)).child("Moving…")
        : statusPill(t, shown.status, shown.category, { size: "md", caret: true }));

    const actions = h_flex().gap(8).items_center().flex_wrap()
      .child(statusTrigger)
      .when(d ? !d.mine : false, (el) => el.child(button(t, "jira-assign", { label: "Assign to me", iconName: "user-plus", busy: busy === "assign", onClick: (_e, cx) => { this.assign(issue.key, cx); cx.notify(); } })))
      .child(button(t, "jira-start", { label: "Start work", iconName: "play", kind: "primary", onClick: (_e, cx) => { this.openStart(shown, cx); cx.notify(); } }));

    const field = (label, value) => h_flex().gap(10).items_center().min_h(26)
      .child(div().w(84).flex_none().text_size(12).text_color(t.dim).child(label))
      .child(h_flex().flex_1().min_w(0).gap(7).items_center().text_size(12.5).child(value));
    const person = (name, none) => name ? h_flex().gap(7).items_center().min_w(0).child(avatar(name, 20)).child(div().truncate().child(name)) : div().text_color(t.dim).child(none);
    const fields = v_flex().gap(2).p(12).rounded(9).border_1().border_color(t.border).bg(t.background)
      .child(field("Assignee", d ? person(d.assignee, "Unassigned") : this.shimmer(t, 120)))
      .child(field("Reporter", d ? person(d.reporter, "—") : this.shimmer(t, 100)))
      .child(field("Priority", shown.priority ? h_flex().gap(6).items_center().child(priorityMark(shown.priority, 14)).child(shown.priority) : div().text_color(t.dim).child(d ? "None" : "")))
      .child(field("Updated", div().text_color(t.muted).child(shown.updated ? `${M.ago(shown.updated)} ago · ${M.dateLabel(shown.updated)}` : "")))
      .when(Boolean(d?.created), (el) => el.child(field("Created", div().text_color(t.muted).child(M.dateLabel(d.created)))))
      .when((d?.labels?.length ?? 0) > 0, (el) => el.child(field("Labels", h_flex().gap(4).flex_wrap().children(d.labels.map((l) => div().px(6).h(18).rounded(4).bg(t.raised).flex().items_center().text_size(11).text_color(t.muted).child(l))))))
      .when(byTicket.length > 0, (el) => el.child(field("In spaces", h_flex().gap(4).flex_wrap().children(byTicket.map((s) =>
        div().id(`jira-space-${s.id}`).cursor_pointer().on_click(() => { run("space.select", { id: s.id }); if (this.overlay) this.close(); }).child(spaceChip(t, s)))))));

    const section = (title, count, content) => v_flex().gap(8)
      .child(h_flex().gap(6).items_center().text_size(11).font_semibold().text_color(t.dim)
        .child(title.toUpperCase()).when(count !== null && count !== undefined, (el) => el.child(div().font_normal().child(String(count)))))
      .child(content);

    const miniRow = (i, id) => h_flex().id(id).h(32).px(8).gap(8).items_center().rounded(7).cursor_pointer().bg(t.background).border_1().border_color(t.border)
      .hover((s) => s.border_color(t.accent))
      .on_click((_e, cx) => { this.pin(i, cx); cx.notify(); })
      .child(typeTile(i, 14)).child(div().flex_none().text_size(12).text_color(t.muted).child(i.key))
      .child(div().flex_1().min_w(0).truncate().text_size(12.5).child(i.summary))
      .child(statusPill(t, i.status, i.category));

    let descr;
    if (!d) descr = entry?.error ? this.inlineError(t, entry.error, () => this.loadDetail(issue.key, cx)) : v_flex().gap(6).child(this.shimmer(t, 320)).child(this.shimmer(t, 260)).child(this.shimmer(t, 290));
    else if (d.description) descr = div().text_size(13).text_color(t.foreground).child(TextView.markdown(`jira-desc-${issue.key}`, d.description).selectable(true)
      .on_link_click((url, cx) => cx.open_url(url)));
    else descr = div().text_size(12.5).text_color(t.dim).italic().child("No description.");

    return v_flex().id(`jira-detail-${issue.key}`).flex_1().min_h(0).overflow_y_scrollbar().px(this.wide ? 24 : 16).py(18).gap(16)
      .child(h_flex().gap(8).items_center().child(div().flex_1().min_w(0).child(crumbs)).child(tools))
      .child(div().text_size(19).font_semibold().line_height(1.3).child(shown.summary || issue.key))
      .child(actions)
      .when(menuOpen, (el) => el.child(this.statusMenuView(t, issue.key)))
      .when(Boolean(entry?.error && d), (el) => el.child(this.inlineError(t, entry.error, () => this.loadDetail(issue.key, cx))))
      .child(fields)
      .child(section("Description", null, descr))
      .when((d?.subtasks?.length ?? 0) > 0, (el) => el.child(section("Sub-tasks", d.subtasks.length, v_flex().gap(4).children(d.subtasks.map((s) => miniRow(s, `jira-sub-${s.key}`))))))
      .when(kind === "epic", (el) => el.child(section("Child issues", kids?.list?.length ?? null,
        kids?.list ? (kids.list.length ? v_flex().gap(4).children(kids.list.slice(0, 50).map((s) => miniRow(s, `jira-kid-${s.key}`))) : div().text_size(12.5).text_color(t.dim).child("No child issues."))
          : kids?.error ? this.inlineError(t, kids.error, () => this.loadChildren(issue.key, cx, true)) : this.shimmer(t, 240))))
      .child(this.commentsView(t, issue.key, d, section));
  }

  commentsView(t, key, d, section) {
    const all = M.mergeComments(d?.comments, this.pendingComments.get(key));
    const list = all.length === 0 ? null : v_flex().gap(10).children(all.slice().reverse().map((c, n) =>
      h_flex().gap(10).items_start().when(Boolean(c.pending), (el) => el.opacity(0.65))
        .child(avatar(c.author || "?", 24))
        .child(v_flex().flex_1().min_w(0).gap(4).p(10).rounded(9).bg(t.background).border_1().border_color(t.border)
          .child(h_flex().gap(6).items_center().text_size(12).child(div().font_semibold().child(c.author || "Someone"))
            .child(div().text_color(t.dim).child(c.pending ? "just now" : `${M.ago(c.created)} ago`)))
          .child(div().text_size(12.5).child(TextView.markdown(`jira-c-${key}-${c.pending ? "p" : ""}${n}`, c.body || "_(empty)_").selectable(true).on_link_click((url, cx) => cx.open_url(url)))))));
    const box = this.commentBox(key);
    const st = this.commentSt.get(key);
    const focused = this.commenting === key;
    const ready = M.canPost(box.value());
    const compose = h_flex().gap(10).items_start()
      .child(avatar(this.me?.name ?? "Me", 24))
      .child(v_flex().flex_1().min_w(0).gap(6)
        .child(div().id(`jira-comment-box-${key}`).px(10).py(8).rounded(9).bg(t.background).border_1().border_color(st?.error ? t.blocked : focused ? t.accent : t.border)
          .child(Textarea.new(box).w_full().text_size(12.5).text_color(t.foreground)))
        .when(Boolean(st?.error), (el) => el.child(h_flex().gap(8).px(10).py(8).rounded(8).bg(t.blocked + "1a").text_size(12)
          .child(icon("alert").size(13).flex_none().text_color(t.blocked)).child(div().flex_1().min_w(0).child(st.error))))
        .child(h_flex().gap(8).items_center()
          .child(h_flex().flex_1().gap(5).items_center().text_size(11).text_color(t.dim).child(kbd(t, "⌘⏎")).child("post").child(kbd(t, "esc")).child("leave the draft"))
          .child(button(t, `jira-comment-post-${key}`, { label: "Comment", kind: "brand", size: "sm", disabled: !ready, busy: Boolean(st?.busy), onClick: (_e, cx) => { this.postComment(key, cx); cx.notify(); } }))));
    return section("Comments", all.length || null, v_flex().gap(12).when(Boolean(list), (el) => el.child(list)).child(compose));
  }

  statusMenuView(t, key) {
    const tr = this.transitions.get(key);
    const at = this.menu?.at ?? 0;
    let body;
    if (!tr || tr.loading) body = h_flex().h(34).px(10).gap(8).items_center().text_color(t.dim).text_size(12).child(icon("spinner").size(12).spin(900)).child("Loading transitions…");
    else if (tr.error) body = div().p(10).max_w(280).text_size(12).text_color(t.blocked).child(tr.error.message);
    else if (!tr.list.length) body = div().p(10).text_size(12).text_color(t.dim).child("No transitions available.");
    else body = v_flex().gap(1).children(tr.list.map((x, i) => {
      const c = statusColors(t, x.category);
      return h_flex().id(`jira-tr-${x.id}`).h(32).px(10).gap(10).items_center().rounded(6).cursor_pointer()
        .when(i === at, (el) => el.bg(t.hover)).hover((s) => s.bg(t.hover))
        .on_click((_e, cx) => { this.transition(key, x, cx); cx.notify(); })
        .child(div().size(8).flex_none().rounded_full().bg(c.dot))
        .child(div().flex_1().min_w(0).truncate().text_size(12.5).child(x.name))
        .when(x.name.toLowerCase() !== x.to.toLowerCase(), (el) => el.child(h_flex().gap(4).items_center().text_size(11).text_color(t.dim).child(icon("arrow-right").size(11)).child(x.to.toUpperCase())))
        .when(x.hasScreen, (el) => el.child(div().text_size(10.5).text_color(t.dim).tooltip("Jira asks for more fields on this one; it may need the web").child("…")));
    }));
    return v_flex().id("jira-status-menu").track_focus(this.menuHandle).on_key_down((e, cx) => this.onMenuKey(e, cx))
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .mt(-6).w(340).max_w_full().p(5).gap(2).rounded(10).bg(t.popover).border_1().border_color(t.border).shadow_md().text_color(t.foreground)
      .child(h_flex().px(10).pt(4).pb(6).items_center().text_size(10.5).font_semibold().text_color(t.dim)
        .child(div().flex_1().child("MOVE TO")).child(h_flex().gap(4).items_center().font_normal().child(kbd(t, "↑↓")).child(kbd(t, "⏎")).child(kbd(t, "esc"))))
      .child(body);
  }

  shimmer(t, w) {
    return div().w(w).max_w_full().h(10).rounded(3).bg(t.raised).loop("opacity", { from: 0.45, to: 1, period: 1100 });
  }

  inlineError(t, error, retry) {
    return h_flex().gap(8).px(10).py(8).rounded(8).bg(t.blocked + "1a").text_size(12)
      .child(icon("alert").size(13).flex_none().text_color(t.blocked))
      .child(div().flex_1().min_w(0).child(error?.message ?? "Something went wrong."))
      .child(button(t, `jira-retry-${Math.abs(M.hashIndex(error?.message ?? "", 1e6))}`, { label: "Retry", size: "sm", onClick: (_e, cx) => { retry(); cx.notify(); } }));
  }

  footer(t) {
    const legend = this.commenting
      ? [["⌘⏎", "post"], ["esc", "leave the draft"]]
      : this.typing
      ? [["↑↓", "move"], ["⏎", "open"], ["esc", "clear"]]
      : this.wide
        ? [["↑↓", "move"], ["⏎", this.current() && M.typeKind(this.current()) === "epic" && !this.drill && !this.pinned ? "children" : "open"], ["/", "search"], ["t", "status"], ["a", "assign"], ["b", "start work"], ["m", "comment"], ["?", "keys"]]
        : [["↑↓", "move"], ["⏎", "open"], ["/", "search"], ["?", "keys"]];
    const n = this.notice;
    const color = n?.level === "error" ? t.blocked : n?.level === "success" ? t.done : t.muted;
    return h_flex().flex_none().h(32).px(14).gap(12).items_center().border_t(1).border_color(t.border).text_size(11).text_color(t.dim)
      .when(Boolean(n), (el) => el.child(h_flex().flex_1().min_w(0).gap(6).items_center().text_color(color)
        .child(icon(n.level === "error" ? "alert" : "check").size(12).flex_none()).child(div().truncate().text_color(t.foreground).child(n.text))))
      .when(!n, (el) => el.child(h_flex().flex_1().min_w(0).gap(12).items_center().overflow_hidden().children(legend.map(([k, label]) =>
        h_flex().id(`jira-key-${k}`).flex_none().gap(5).items_center().child(kbd(t, k)).child(label)
          .when(k === "m", (el) => el.cursor_pointer().hover((s) => s.text_color(t.foreground)).on_click((_e, cx) => { this.focusComment(cx); cx.notify(); }))))));
  }

  // ---- sheets --------------------------------------------------------------------

  sheetView(t, cx) {
    const s = this.sheet;
    const scrim = div().id("jira-scrim").absolute().inset_0().bg("#00000066").flex().items_center().justify_center().p(20)
      .on_mouse_down("left", (_e, cx) => { cx.stop_propagation(); if (!s.busy) { this.sheet = null; this.handle.focus(); cx.notify(); } });
    const card = v_flex().id("jira-sheet").w(s.kind === "help" ? 440 : 480).max_w_full().max_h_full()
      .rounded(12).bg(t.popover).border_1().border_color(t.border).shadow_xl().p(20).gap(14)
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation());
    return scrim.child(s.kind === "help" ? this.helpSheet(t, card) : this.startSheet(t, card, cx));
  }

  helpSheet(t, card) {
    const keys = [
      ["↑ ↓  j k", "move through the list"], ["⏎", "open (an epic lists its children)"], ["/", "search: filters at once, then asks Jira"],
      ["t", "change status (the issue's transitions, kept for a day; r refreshes)"], ["a", "assign to me"], ["b", "start work: branch or worktree, and an agent"], ["m", "comment (⌘⏎ posts, esc leaves the draft)"],
      ["s", "star or unstar an epic"], ["c", "copy the key"], ["o", "open in Jira"], ["i", "this space's ticket"],
      ["1  2  tab", "Issues / Epics"], ["r", "refresh"], ["p", "open as a pane (dialog only)"], ["esc", "back, clear, close"],
    ];
    return card
      .child(h_flex().items_center().child(div().flex_1().text_size(15).font_semibold().child("Keys")).child(kbd(t, "esc")))
      .child(v_flex().gap(6).children(keys.map(([k, what]) => h_flex().gap(12).items_center()
        .child(h_flex().w(96).flex_none().gap(3).children(k.split("  ").map((x) => kbd(t, x))))
        .child(div().text_size(12.5).text_color(t.muted).child(what)))))
      .child(h_flex().pt(6).gap(8).items_center().border_t(1).border_color(t.border)
        .child(div().flex_1().text_size(11.5).text_color(t.dim).child(this.me ? `Signed in as ${this.me.name}${this.siteHost() ? ` on ${this.siteHost()}` : ""}` : ""))
        .child(button(t, "jira-signout", { label: "Sign out", iconName: "logout", kind: "ghost", size: "sm", onClick: (_e, cx) => { this.signOut(cx); cx.notify(); } })));
  }

  startSheet(t, card, cx) {
    const s = this.sheet;
    const issue = s.issue;
    const category = this.currentCategory(issue);
    const tr = this.progressTransition(issue.key, category);
    const trLoading = this.transitions.get(issue.key)?.loading;
    const progressNote = category === "indeterminate" ? "It's already in progress" : category === "done" ? "It's done" : trLoading ? "Checking the workflow…" : "No in-progress transition from here";
    const looking = s.repo === undefined;
    const choice = (id, on, iconName, title, sub, onClick, disabled = false) => h_flex().id(id).flex_1().min_w(0).p(10).gap(10).items_start().rounded(9).border_1()
      .border_color(on ? t.accent : t.border).bg(on ? t.selected : t.background)
      .when(!disabled, (el) => el.cursor_pointer().hover((x) => x.border_color(t.accent)).on_click((_e, cx) => { onClick(); cx.notify(); }))
      .when(disabled, (el) => el.opacity(0.5))
      .child(icon(iconName).size(16).flex_none().mt(1).text_color(on ? t.accent : t.muted))
      .child(v_flex().min_w(0).gap(2).child(div().text_size(12.5).font_medium().child(title)).child(div().text_size(11.5).text_color(t.dim).child(sub)));
    const check = (id, on, label, sub, onClick, disabled = false) => h_flex().id(id).gap(10).items_center().py(4)
      .when(!disabled, (el) => el.cursor_pointer().on_click((_e, cx) => { onClick(); cx.notify(); }))
      .when(disabled, (el) => el.opacity(0.5))
      .child(div().size(16).flex_none().rounded(4).border_1().border_color(on ? t.accent : t.input ?? t.border).bg(on ? t.accent : t.background)
        .flex().items_center().justify_center().when(on, (el) => el.child(icon("check").size(12).text_color(t.accent_foreground))))
      .child(v_flex().min_w(0).child(div().text_size(12.5).child(label)).when(Boolean(sub), (el) => el.child(div().text_size(11).text_color(t.dim).child(sub))));
    const repoName = s.repo?.name ?? "";
    // A new branch starts from what this space has checked out.
    const base = s.repo?.branch ? `, new branch from ${s.repo.branch}` : ", new branch from the current commit";
    const mine = Boolean(this.details.get(issue.key)?.data?.mine);
    const spaceName = s.space?.name ?? "this space";
    const agentChip = (a, on) => h_flex().id(`jira-agent-${a?.key ?? "none"}`).h(28).px(10).gap(6).items_center().rounded(7).cursor_pointer().border_1()
      .border_color(on ? t.accent : t.border).bg(on ? t.selected : t.background).text_size(12)
      .hover((x) => x.border_color(t.accent))
      .on_click((_e, cx) => { s.agent = a; cx.notify(); })
      .when(Boolean(a?.icon), (el) => el.child(svgOrImage(a)))
      .child(a ? a.name : "No agent");

    return card
      .child(h_flex().gap(10).items_center()
        .child(div().size(30).rounded(8).bg(t.solid).flex().items_center().justify_center().child(icon("play").size(15).text_color(t.solid_foreground)))
        .child(v_flex().flex_1().min_w(0).child(div().text_size(15).font_semibold().child("Start work")).child(div().text_size(12).text_color(t.muted).truncate().child(`${issue.key} · ${issue.summary}`)))
        .child(kbd(t, "esc")))
      .child(v_flex().gap(6)
        .child(div().text_size(11).font_semibold().text_color(t.dim).child("BRANCH"))
        .child(h_flex().h(34).px(10).gap(8).items_center().rounded(8).border_1().border_color(t.accent).bg(t.background)
          .child(icon("branch").size(14).flex_none().text_color(t.dim))
          .child(Input.new(s.input).flex_1().h_full().text_size(12.5).font_family("JetBrains Mono"))))
      .child(looking
        ? h_flex().gap(8).items_center().text_size(12).text_color(t.dim).child(icon("spinner").size(12).spin(900)).child("Looking at this space's repository…")
        : h_flex().gap(8).items_stretch()
          .child(choice("jira-mode-wt", s.mode === "worktree", "folder-branch", "New worktree", s.repo ? `A new space on its own checkout of ${repoName}${base}` : "No git repository here", () => { s.mode = "worktree"; }, !s.repo))
          .child(choice("jira-mode-co", s.mode === "checkout", "branch", "Switch branch", s.repo ? s.repo.dirty ? `Here, in this checkout (it has uncommitted changes)${base}` : `Here, in this checkout${base}` : "No git repository here", () => { s.mode = "checkout"; }, !s.repo)))
      .child(v_flex().gap(2)
        .child(mine
          ? check("jira-opt-assign", true, "Assigned to you", null, () => {}, true)
          : check("jira-opt-assign", s.assign, "Assign to me", null, () => { s.assign = !s.assign; }))
        .child(check("jira-opt-progress", s.progress && Boolean(tr), tr ? `Move to ${tr.to}` : "Move to In Progress",
          tr ? null : progressNote, () => { s.progress = !s.progress; }, !tr)))
      .child(v_flex().gap(6)
        .child(div().text_size(11).font_semibold().text_color(t.dim).child("START AN AGENT ON IT"))
        .child(h_flex().gap(6).flex_wrap().child(agentChip(null, !s.agent)).children(s.agents.slice(0, 6).map((a) => agentChip(a, s.agent?.key === a.key))))
        .when(Boolean(s.agent), (el) => el.child(div().text_size(11).text_color(t.dim).child(`${s.agent.name} starts in a new tab with the issue's summary, link and description as its first message.`))))
      .when(Boolean(s.error), (el) => el.child(h_flex().gap(8).px(10).py(8).rounded(8).bg(t.blocked + "1a").text_size(12)
        .child(icon("alert").size(13).flex_none().text_color(t.blocked)).child(div().flex_1().min_w(0).child(s.error))))
      .child(h_flex().pt(4).gap(8).items_center()
        .child(div().flex_1())
        .child(button(t, "jira-start-cancel", { label: "Cancel", kind: "ghost", onClick: (_e, cx) => { this.sheet = null; this.handle.focus(); cx.notify(); } }))
        .child(button(t, "jira-start-go", { label: s.mode === "worktree" ? "Create worktree" : s.mode === "checkout" ? "Create branch" : "Start", iconName: "play", kind: "primary", busy: s.busy, disabled: looking && !s.agent, onClick: (_e, cx) => this.runStart(cx) })));
  }
}

// An agent's icon as Herdr's program table describes it.
function svgOrImage(a) {
  return a.tint ? svg(a.icon).size(14).flex_none() : image(a.icon).size(14).flex_none();
}
