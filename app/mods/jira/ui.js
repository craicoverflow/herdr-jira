// The Jira mod's visual atoms: type tiles, status lozenges, priority marks,
// avatars, buttons and key caps. Colors come from the theme where they mean
// something in Herdr (text, surfaces, borders); issue types and statuses use
// Jira's own hues so they read the same as on the web.

import { div, svg } from "gpui-kit";
import { h_flex } from "gpui-base";
import { settings, spaces, shared, setShared } from "herdr";
import { typeKind, priorityRank, initials, hashIndex } from "./model.js";

export const icon = (name) => svg(`mods/jira/icons/${name}.svg`);

const TYPE_COLORS = {
  bug: "#e5493a", story: "#63ba3c", task: "#4bade8", epic: "#904ee2",
  subtask: "#2fa6b8", initiative: "#f08c2e", other: "#8993a4",
};

export function typeTile(issue, size = 16) {
  const kind = typeKind(issue);
  return div().size(size).flex_none().rounded(Math.max(3, size / 4)).bg(TYPE_COLORS[kind])
    .flex().items_center().justify_center()
    .child(icon(`type-${kind}`).size(Math.round(size * 0.7)).text_color("#ffffff"));
}

const dark = () => settings()?.dark !== false;

// Status category -> { fg, bg, dot } for a lozenge.
export function statusColors(t, category) {
  const d = dark();
  if (category === "indeterminate") return { fg: d ? "#8cb4ff" : "#1d4fb8", bg: d ? "#4c8df62e" : "#4c8df61f", dot: "#4c8df6" };
  if (category === "done") return { fg: d ? "#7fd69b" : "#1d7a3e", bg: d ? "#3fae6a2e" : "#3fae6a1f", dot: "#3fae6a" };
  return { fg: t.muted, bg: t.raised, dot: t.dim };
}

// A Jira-style status lozenge. opts: { size: "sm" | "md", caret: bool }.
export function statusPill(t, status, category, opts = {}) {
  const c = statusColors(t, category);
  const md = opts.size === "md";
  return h_flex().flex_none().h(md ? 24 : 19).px(md ? 9 : 7).gap(md ? 6 : 5).items_center().rounded(md ? 6 : 5)
    .bg(c.bg).text_color(c.fg).text_size(md ? 11.5 : 10.5).font_semibold()
    .when(category === "new", (el) => el.border_1().border_color(t.border))
    .child(div().size(md ? 7 : 6).rounded_full().bg(c.dot))
    .child(div().max_w(md ? 220 : 120).truncate().child((status || "—").toUpperCase()))
    .when(Boolean(opts.caret), (el) => el.child(icon("chevron-down").size(12).text_color(c.fg)));
}

const PRIORITY_COLORS = ["#e5493a", "#f0703c", "#e8a530", "#3a8ee6", "#5aa0e8"];

export function priorityMark(name, size = 14) {
  const r = priorityRank(name);
  if (r === null) return null;
  return icon(`prio-${r}`).size(size).flex_none().text_color(PRIORITY_COLORS[r]);
}

const AVATAR_COLORS = ["#5b8def", "#8b7cf6", "#22a99a", "#e3814f", "#d9649a", "#3fae6a"];

export function avatar(name, size = 20) {
  return div().size(size).flex_none().rounded_full().bg(AVATAR_COLORS[hashIndex(name, AVATAR_COLORS.length)])
    .flex().items_center().justify_center()
    .text_size(Math.max(8, size * 0.42)).font_semibold().text_color("#ffffff")
    .child(initials(name));
}

export function kbd(t, label) {
  return div().flex_none().h(18).min_w(18).px(5).rounded(4).border_1().border_color(t.border).bg(t.raised)
    .flex().items_center().justify_center().text_size(10.5).text_color(t.muted).child(label);
}

// A button. kind: "primary" (solid), "accent", "brand" (Jira blue), "outline", "ghost".
export function button(t, id, { label, iconName, kind = "outline", disabled = false, busy = false, onClick, tooltip, size = "md" } = {}) {
  const sm = size === "sm";
  const colors = {
    primary: { bg: t.solid, fg: t.solid_foreground, border: t.solid, hover: t.solid },
    accent: { bg: t.accent, fg: t.accent_foreground, border: t.accent, hover: t.accent },
    brand: { bg: "#2684ff", fg: "#ffffff", border: "#2684ff", hover: "#2684ff" },
    outline: { bg: t.background, fg: t.foreground, border: t.border, hover: t.hover },
    ghost: { bg: null, fg: t.muted, border: null, hover: t.hover },
  }[kind];
  const off = disabled || busy;
  let el = h_flex().id(id).flex_none().h(sm ? 24 : 28).px(label ? (sm ? 8 : 10) : 0).when(!label, (e) => e.w(sm ? 24 : 28).justify_center())
    .gap(6).items_center().rounded(sm ? 6 : 7).text_size(sm ? 11.5 : 12).font_medium()
    .text_color(off ? t.dim : colors.fg);
  if (colors.bg) el = el.bg(off && kind !== "ghost" ? t.raised : colors.bg);
  if (colors.border) el = el.border_1().border_color(off ? t.border : colors.border);
  if (!off) {
    el = el.cursor_pointer().hover((s) => (kind === "primary" || kind === "accent" || kind === "brand" ? s.opacity(0.88) : s.bg(colors.hover).text_color(t.foreground)));
    if (onClick) el = el.on_mouse_down("left", (_e, cx) => cx.stop_propagation()).on_click((e, cx) => { cx.stop_propagation(); onClick(e, cx); });
  }
  if (tooltip) el = el.tooltip(tooltip);
  if (busy) el = el.child(icon("spinner").size(13).spin(900));
  else if (iconName) el = el.child(icon(iconName).size(sm ? 13 : 14));
  if (label) el = el.child(label);
  return el;
}

// Space colors, as Herdr's own lists keep them (shared "space.colors"), so a
// space's chip here matches its mark in the sidebar.
const MARK = { ansi6: "#22a99a", ansi3: "#e3814f", ansi4: "#5b8def", ansi5: "#8b7cf6", ansi2: "#3fae6a", ansi1: "#d9649a" };
const PROJECT_COLORS = ["ansi6", "ansi3", "ansi4", "ansi5", "ansi2", "ansi1"];

export function spaceColor(t, space) {
  if (!space || space.name === "~") return t.dim;
  const kept = shared("space.colors") ?? {};
  let c = kept[space.id];
  if (!c) {
    // The same rule the shipped mods use: the color fewest spaces have.
    const ids = new Set(spaces().map((p) => p.id));
    const live = Object.fromEntries(Object.entries(kept).filter(([id]) => ids.has(id)));
    const uses = (x) => Object.values(live).filter((v) => v === x).length;
    c = PROJECT_COLORS.reduce((best, x) => (uses(x) < uses(best) ? x : best));
    setShared("space.colors", { ...live, [space.id]: c });
  }
  return c.startsWith("#") ? c : (MARK[c] ?? t[c] ?? t.accent);
}

export function spaceChip(t, space) {
  const color = spaceColor(t, space);
  return h_flex().flex_none().h(18).max_w(140).px(6).gap(5).items_center().rounded(5).bg(t.raised)
    .text_size(10.5).text_color(t.muted)
    .child(div().size(7).flex_none().rounded(2).bg(color))
    .child(div().truncate().child(space.name));
}
