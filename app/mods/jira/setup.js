// First-run setup: a short, skippable sheet that asks which Jira site (only when
// the account reaches several), the default project for new issues, and which
// tabs to show. It runs after sign-in and from ? then "Set up".
//
// The helper does the writing (`setup-save` merges into config.json); nothing
// here touches a file. The step logic is in model.js (setupSteps, stepAfter,
// setupViews …) so it is tested without Herdr.
//
// Mixed into the Jira view (main.js): `setupMethods` become its methods and
// `setupSheet` draws the card.

import { div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input } from "gpui-base";
import { api } from "./api.js";
import * as M from "./model.js";
import { icon, kbd, button } from "./ui.js";

const LIST_ROWS = 7;

const TITLES = {
  site: ["Which Jira site?", "Your account reaches more than one."],
  project: ["Default project", "New issues (n) start here."],
  tabs: ["Your tabs", "My work is always on. Add what else you look at."],
};

export const setupMethods = {
  // ---- opening and moving ------------------------------------------------------

  openSetup(cx, auto = false) {
    if (!this.session?.signedIn) return;
    const input = (placeholder) => InputState.new({ placeholder });
    const s = {
      kind: "setup", auto, steps: [], step: null, did: { project: false, tabs: false },
      sites: [], site: null, currentSite: null, siteBusy: false,
      projects: null, project: this.session.setup?.defaultProject || null, at: 0, loadingProjects: false, projectError: "",
      epics: this.views.some((v) => v.kind === "epics") || Boolean(this.session.setup?.needed),
      custom: this.views.filter((v) => v.kind === "jql").map((v) => ({ id: v.id, name: v.name, jql: v.jql, groupBy: v.groupBy })),
      adding: false, field: "name", checking: false, tabError: "",
      saving: false, error: "", typing: false, lastEnter: 0,
      filter: input("Search projects by key or name"),
      tabName: input("Name, e.g. Team bugs"),
      tabJql: input("JQL, e.g. project = WEB AND type = Bug"),
    };
    s.filter.on("change", (_e, cx) => { s.at = 0; cx.notify(); });
    for (const [inp, field] of [[s.filter, "filter"], [s.tabName, "name"], [s.tabJql, "jql"]]) {
      inp.on("submit", (_e, cx) => { this.setupEnter(cx); cx.notify(); });
      inp.on("focus", (_e, cx) => { s.typing = true; if (field !== "filter") s.field = field; cx.notify(); });
      inp.on("blur", (_e, cx) => { s.typing = false; cx.notify(); });
    }
    for (const inp of [s.tabName, s.tabJql]) inp.on("change", (_e, cx) => { s.tabError = ""; cx.notify(); });
    this.sheet = s;
    this.menu = null;
    cx.spawn(async (cx) => {
      const r = await api("sites");
      if (this.sheet !== s) return;
      if (r.ok) {
        s.sites = r.data.sites;
        s.site = s.currentSite = r.data.current || null;
      } else if (this.authFailed(r.error)) { this.sheet = null; return cx.notify(); }
      s.steps = M.setupSteps(s.sites);
      this.enterStep(s, s.steps[0], cx);
      cx.notify();
    });
  },

  enterStep(s, step, cx) {
    s.step = step;
    s.error = "";
    s.lastEnter = Date.now(); // the same Enter can arrive as a key and as a field's submit: it moves one step
    if (step === "project") {
      if (!s.projects) this.loadSetupProjects(s, cx);
      cx.timer.after(60, () => { if (this.sheet === s && s.step === "project") s.filter.focus(); });
    } else this.handle.focus();
  },

  loadSetupProjects(s, cx) {
    s.loadingProjects = true;
    s.projectError = "";
    cx.spawn(async (cx) => {
      const r = await api("projects");
      if (this.sheet !== s) return;
      s.loadingProjects = false;
      if (!r.ok) {
        if (this.authFailed(r.error)) this.sheet = null;
        else s.projectError = r.error.message;
      } else {
        s.projects = r.data.projects;
        if (s.project && !s.projects.some((p) => p.key === s.project)) s.project = null; // one you can't create in
        s.at = Math.max(0, s.projects.findIndex((p) => p.key === s.project));
        if (!s.projects.length) s.projectError = "You can't create issues in any project on this site.";
      }
      cx.notify();
    });
  },

  // Next (or Finish). Skipping a step leaves what it would have written alone.
  setupNext(cx, skip = false) {
    const s = this.sheet;
    if (!s || s.kind !== "setup" || !s.step || s.saving || s.siteBusy) return;
    if (s.step === "site" && !skip && s.site && s.site !== s.currentSite) return this.applySite(s, cx);
    if (!skip && s.step === "project") s.did.project = Boolean(s.project);
    if (!skip && s.step === "tabs") s.did.tabs = true;
    const next = M.stepAfter(s.steps, s.step);
    if (next) this.enterStep(s, next, cx);
    else this.finishSetup(cx);
  },

  setupBack(cx) {
    const s = this.sheet;
    const prev = M.stepAfter(s.steps, s.step, -1);
    if (prev && !s.saving) this.enterStep(s, prev, cx);
  },

  // Chooses the site: the helper stores it the way a one-site account's is, then
  // everything on screen is for the new site.
  applySite(s, cx) {
    s.siteBusy = true;
    s.error = "";
    cx.spawn(async (cx) => {
      const r = await api("site-select", s.site);
      if (this.sheet !== s) return;
      s.siteBusy = false;
      if (!r.ok) {
        if (this.authFailed(r.error)) this.sheet = null;
        else s.error = r.error.message;
        return cx.notify();
      }
      s.currentSite = s.site;
      s.projects = null;
      this.session = { ...this.session, siteURL: r.data.siteURL, cloudId: r.data.cloudId };
      this.gen.bump("session");
      this.loading = {}; this.data = {};
      this.details.clear(); this.children.clear(); this.transitions.clear();
      this.tickets = {}; this.me = null; this.pinned = null;
      this.loadAll(cx);
      this.setupNext(cx);
      cx.notify();
    });
    cx.notify();
  },

  finishSetup(cx) {
    const s = this.sheet;
    const payload = M.setupPayload({
      project: s.did.project ? s.project : null,
      tabs: s.did.tabs ? { existing: this.views, epics: s.epics, custom: s.custom } : null,
    });
    s.saving = true;
    s.error = "";
    cx.spawn(async (cx) => {
      const r = await api("setup-save", "--json", JSON.stringify(payload));
      if (this.sheet !== s) return;
      s.saving = false;
      if (!r.ok) {
        if (this.authFailed(r.error)) this.sheet = null;
        else s.error = r.error.message;
        return cx.notify();
      }
      const ses = await api("session");
      if (this.sheet !== s) return;
      if (ses.ok) this.applySession(ses.data);
      // Done: close, and land on My work with the new tabs loading.
      this.sheet = null;
      this.tab = (this.views.find((v) => v.kind === "my-work") ?? this.views[0]).id;
      this.store("tab", this.tab);
      this.gen.bump("session");
      this.loading = {}; this.data = {};
      this.loadAll(cx);
      this.say("You're set up. Here's your work.", "success", cx);
      this.handle.focus();
      cx.notify();
    });
    cx.notify();
  },

  // Closes setup without saving what is left, and remembers so it isn't offered again.
  skipSetup(cx) {
    const s = this.sheet;
    if (!s || s.saving) return;
    this.sheet = null;
    this.handle.focus();
    cx.spawn(async () => { await api("setup-skip"); });
    if (this.session?.setup) this.session = { ...this.session, setup: { ...this.session.setup, needed: false } };
  },

  // ---- the tabs step ----------------------------------------------------------

  openAddTab(s, cx) {
    s.adding = true;
    s.tabError = "";
    s.field = "name";
    cx.timer.after(60, () => { if (this.sheet === s && s.adding) s.tabName.focus(); });
  },

  addTab(s, cx) {
    if (s.checking) return;
    const name = s.tabName.value();
    const jql = s.tabJql.value();
    const problem = M.validateTab({ name, jql }, [...this.views.filter((v) => v.kind !== "jql"), ...s.custom]);
    if (problem) { s.tabError = problem; return cx.notify(); }
    s.checking = true;
    s.tabError = "";
    cx.spawn(async (cx) => {
      const r = await api("jql-check", "--query", jql.trim());
      if (this.sheet !== s) return;
      s.checking = false;
      if (!r.ok) {
        if (this.authFailed(r.error)) this.sheet = null;
        else s.tabError = r.error.code === "invalid_jql" ? r.error.message : `Couldn't check it: ${r.error.message}`;
        return cx.notify();
      }
      s.custom.push({ name: name.trim(), jql: jql.trim(), count: r.data.count, more: r.data.more });
      s.tabName.set_value("");
      s.tabJql.set_value("");
      s.adding = false;
      this.handle.focus();
      cx.notify();
    });
    cx.notify();
  },

  // ---- keys -------------------------------------------------------------------

  // Enter, from the key or from a field's submit (whichever arrives first wins).
  setupEnter(cx) {
    const s = this.sheet;
    if (!s || s.kind !== "setup") return;
    const now = Date.now();
    if (now - s.lastEnter < 250) return;
    s.lastEnter = now;
    if (s.adding) {
      if (s.field === "name") s.tabJql.focus();
      else this.addTab(s, cx);
    } else if (s.step === "project") {
      const pick = M.rankProjects(s.projects, s.filter.value())[s.at];
      if (pick) s.project = pick.key;
      if (pick || s.project) this.setupNext(cx);
    } else this.setupNext(cx);
  },

  setupKey(k, cx) {
    const s = this.sheet;
    if (k === "escape") {
      if (s.saving || s.siteBusy) return true;
      if (s.adding) { s.adding = false; s.tabError = ""; this.handle.focus(); } else this.skipSetup(cx);
      return true;
    }
    if (!s.step || s.saving || s.siteBusy) return true;
    if (k === "enter") { this.setupEnter(cx); return true; }
    if (s.step === "site") {
      const open = s.sites.filter((x) => x.cloud !== false);
      const i = open.findIndex((x) => x.id === s.site);
      if (k === "down" || k === "j") s.site = open[M.clampMove(i, 1, open.length)]?.id ?? s.site;
      else if (k === "up" || k === "k") s.site = open[M.clampMove(i, -1, open.length)]?.id ?? s.site;
      else return false;
      return true;
    }
    if (s.step === "project") {
      const n = M.rankProjects(s.projects, s.filter.value()).length;
      if (k === "down") s.at = M.clampMove(s.at, 1, n);
      else if (k === "up") s.at = M.clampMove(s.at, -1, n);
      else return false;
      return true;
    }
    if (s.step === "tabs" && !s.adding && !s.typing) {
      if (k === "e") s.epics = !s.epics;
      else if (k === "a" || k === "+") this.openAddTab(s, cx);
      else if (k === "left" || k === "backspace") this.setupBack(cx);
      else return false;
      return true;
    }
    return false; // typing
  },
};

// ---- the card ---------------------------------------------------------------------

export function setupSheet(view, t, card, cx) {
  const s = view.sheet;
  const label = (text) => div().text_size(11).font_semibold().text_color(t.dim).child(text);
  const field = (child) => h_flex().h(34).px(10).gap(8).items_center().rounded(8).border_1().border_color(t.border).bg(t.background).child(child);
  const note = (text, error = false) => h_flex().gap(8).px(10).py(8).rounded(8).bg((error ? t.blocked : t.raised) + (error ? "1a" : "")).text_size(12)
    .child(icon(error ? "alert" : "check").size(13).flex_none().text_color(error ? t.blocked : t.done)).child(div().flex_1().min_w(0).child(text));
  const radio = (on) => div().size(16).flex_none().rounded(8).border_1().border_color(on ? t.accent : t.border).bg(t.background)
    .flex().items_center().justify_center().when(on, (el) => el.child(div().size(8).rounded(4).bg(t.accent)));
  const checkbox = (on) => div().size(16).flex_none().rounded(4).border_1().border_color(on ? t.accent : t.border).bg(on ? t.accent : t.background)
    .flex().items_center().justify_center().when(on, (el) => el.child(icon("check").size(12).text_color(t.accent_foreground)));

  const [title, sub] = TITLES[s.step] ?? ["Setting up", "Checking your Jira sites…"];
  const at = s.steps.indexOf(s.step);
  const head = h_flex().gap(10).items_center()
    .child(div().size(30).rounded(8).bg(t.solid).flex().items_center().justify_center().child(icon("sparkles").size(15).text_color(t.solid_foreground)))
    .child(v_flex().flex_1().min_w(0).child(div().text_size(15).font_semibold().child(title)).child(div().text_size(12).text_color(t.muted).truncate().child(sub)))
    .child(h_flex().gap(4).items_center().when(at >= 0, (el) => el.children(s.steps.map((x) => div().w(x === s.step ? 16 : 6).h(6).rounded(3).bg(x === s.step ? t.accent : t.border)))))
    .child(h_flex().id("jira-setup-esc").gap(5).items_center().cursor_pointer().text_size(11).text_color(t.dim).hover((x) => x.text_color(t.foreground))
      .on_click((_e, cx) => { view.skipSetup(cx); cx.notify(); }).child(kbd(t, "esc")).child("skip"));

  if (!s.step) {
    return card.child(head).child(h_flex().gap(8).items_center().py(20).justify_center().text_color(t.dim)
      .child(icon("spinner").size(14).spin(900)).child("Checking your Jira sites…"));
  }

  let body;
  if (s.step === "site") {
    body = v_flex().gap(6).children(s.sites.map((x) => {
      const on = x.id === s.site;
      const usable = x.cloud !== false;
      return h_flex().id(`jira-setup-site-${x.id}`).h(44).px(10).gap(10).items_center().rounded(8).border_1().border_color(on ? t.accent : t.border)
        .bg(on ? t.selected : t.background).when(usable, (el) => el.cursor_pointer().hover((e) => e.border_color(t.accent)).on_click((_e, cx) => { s.site = x.id; cx.notify(); }))
        .child(radio(on))
        .child(v_flex().flex_1().min_w(0).child(div().text_size(12.5).font_medium().truncate().text_color(usable ? t.foreground : t.dim).child(x.name))
          .child(div().text_size(11.5).text_color(t.dim).truncate().child(usable ? x.host : `${x.host} · not Jira Cloud`)))
        .when(x.id === s.currentSite, (el) => el.child(div().text_size(11).text_color(t.dim).child("in use")));
    }));
  } else if (s.step === "project") {
    const ranked = M.rankProjects(s.projects, s.filter.value());
    const win = M.windowAround(ranked, s.at, LIST_ROWS);
    body = v_flex().gap(8)
      .child(field(icon("search").size(14).flex_none().text_color(t.dim)).child(Input.new(s.filter).flex_1().h_full().text_size(12.5)))
      .child(s.loadingProjects && !s.projects
        ? h_flex().gap(8).items_center().py(12).justify_center().text_size(12).text_color(t.dim).child(icon("spinner").size(12).spin(900)).child("Loading projects…")
        : v_flex().gap(2).p(4).rounded(8).border_1().border_color(t.border).bg(t.background)
          .when(win.before > 0, (el) => el.child(div().h(18).px(8).text_size(11).text_color(t.dim).child(`${win.before} more above`)))
          .children(win.items.map((p, i) => {
            const hot = win.start + i === s.at;
            return h_flex().id(`jira-setup-p-${p.key}`).h(30).px(8).gap(8).items_center().rounded(6).cursor_pointer()
              .bg(hot ? t.selected : t.background).hover((x) => x.bg(t.hover))
              .on_click((_e, cx) => { s.project = p.key; s.at = win.start + i; view.setupNext(cx); cx.notify(); })
              .child(div().w(52).flex_none().text_size(12).font_medium().child(p.key))
              .child(div().flex_1().min_w(0).truncate().text_size(12.5).text_color(t.muted).child(p.name ?? ""))
              .when(p.key === s.project, (el) => el.child(icon("check").size(13).flex_none().text_color(t.done)));
          }))
          .when(win.after > 0, (el) => el.child(div().h(18).px(8).text_size(11).text_color(t.dim).child(`${win.after} more: keep typing to narrow`)))
          .when(Boolean(s.projects) && ranked.length === 0 && !s.projectError, (el) => el.child(div().py(10).px(8).text_size(12).text_color(t.dim).child("No project matches."))))
      .when(Boolean(s.projectError), (el) => el.child(note(s.projectError, true)));
  } else {
    const row = (id, on, title, subtitle, onClick, trailing) => h_flex().id(id).h(40).px(10).gap(10).items_center().rounded(8).border_1().border_color(t.border).bg(t.background)
      .when(Boolean(onClick), (el) => el.cursor_pointer().hover((e) => e.border_color(t.accent)).on_click(onClick))
      .child(on === null ? icon("check").size(14).flex_none().text_color(t.done) : checkbox(on))
      .child(v_flex().flex_1().min_w(0).child(div().text_size(12.5).font_medium().truncate().child(title)).child(div().text_size(11.5).text_color(t.dim).truncate().child(subtitle)))
      .when(Boolean(trailing), (el) => el.child(trailing));
    body = v_flex().gap(6)
      .child(row("jira-setup-mywork", null, "My work", "Everything assigned to you that isn't done", null, div().text_size(11).text_color(t.dim).child("always on")))
      .child(row("jira-setup-epics", s.epics, "Epics", "Your epics and the ones you starred", (_e, cx) => { s.epics = !s.epics; cx.notify(); }, h_flex().gap(4).items_center().child(kbd(t, "e"))))
      .children(s.custom.map((c, i) => row(`jira-setup-c-${i}`, null, c.name,
        c.count === undefined ? c.jql : `${c.jql} · ${c.count}${c.more ? "+" : ""} now`, null,
        button(t, `jira-setup-rm-${i}`, { iconName: "x", kind: "ghost", size: "sm", tooltip: "Remove this tab", onClick: (_e, cx) => { s.custom.splice(i, 1); cx.notify(); } }))))
      .child(s.adding
        ? v_flex().gap(8).p(10).rounded(8).border_1().border_color(t.accent).bg(t.background)
          .child(label("TAB NAME")).child(field(Input.new(s.tabName).flex_1().h_full().text_size(12.5)))
          .child(label("JQL (checked once against Jira)")).child(field(Input.new(s.tabJql).flex_1().h_full().text_size(12.5)))
          .when(Boolean(s.tabError), (el) => el.child(note(s.tabError, true)))
          .child(h_flex().gap(8).items_center().justify_end()
            .child(button(t, "jira-setup-add-cancel", { label: "Cancel", kind: "ghost", size: "sm", onClick: (_e, cx) => { s.adding = false; s.tabError = ""; view.handle.focus(); cx.notify(); } }))
            .child(button(t, "jira-setup-add-go", { label: s.checking ? "Checking…" : "Check and add", iconName: "check", kind: "primary", size: "sm", busy: s.checking, onClick: (_e, cx) => { view.addTab(s, cx); cx.notify(); } })))
        : h_flex().id("jira-setup-add").h(36).px(10).gap(10).items_center().rounded(8).border_1().border_color(t.border).cursor_pointer()
          .hover((e) => e.border_color(t.accent)).on_click((_e, cx) => { view.openAddTab(s, cx); cx.notify(); })
          .child(icon("plus").size(14).flex_none().text_color(t.muted)).child(div().flex_1().text_size(12.5).child("Add a JQL tab"))
          .child(kbd(t, "a")))
      .when(view.viewNotes.length > 0, (el) => el.child(div().text_size(11.5).text_color(t.dim).child("Some entries in config.json were skipped as invalid. Saving writes this list in their place.")));
  }

  const last = M.stepAfter(s.steps, s.step) === null;
  const first = M.stepAfter(s.steps, s.step, -1) === null;
  const hint = s.step === "project" ? [["↑↓", "move"], ["⏎", "pick"]] : s.step === "site" ? [["↑↓", "move"], ["⏎", "use it"]] : [["e", "epics"], ["a", "add a tab"], ["⏎", "finish"]];
  return card.child(head)
    .child(body)
    .when(Boolean(s.error), (el) => el.child(note(s.error, true)))
    .child(h_flex().pt(2).gap(8).items_center()
      .child(h_flex().gap(12).items_center().text_size(11).text_color(t.dim).children(hint.map(([k, what]) => h_flex().gap(5).items_center().child(kbd(t, k)).child(what))))
      .child(div().flex_1())
      .when(!first, (el) => el.child(button(t, "jira-setup-back", { label: "Back", iconName: "chevron-left", kind: "ghost", onClick: (_e, cx) => { view.setupBack(cx); cx.notify(); } })))
      .child(button(t, "jira-setup-skip", { label: last ? "Skip this" : "Skip", kind: "ghost", onClick: (_e, cx) => { view.setupNext(cx, true); cx.notify(); } }))
      .child(button(t, "jira-setup-next", { label: last ? "Finish" : "Next", iconName: last ? "check" : "arrow-right", kind: "primary", busy: s.saving || s.siteBusy,
        disabled: s.step === "project" && !s.project && M.rankProjects(s.projects, s.filter.value()).length === 0,
        onClick: (_e, cx) => { if (s.step === "project" && (!s.project || s.filter.value())) s.project = M.rankProjects(s.projects, s.filter.value())[s.at]?.key ?? null; view.setupNext(cx); cx.notify(); } })));
}
