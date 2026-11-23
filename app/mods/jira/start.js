// Start work on an issue: a branch named after it, either as a new worktree
// space (the way Herdr's own worktree dialog makes them, so the sidebar nests
// it under its repo) or checked out in the space you're in, optionally with
// an agent started on the issue.

import { exec, panes, spaces, run } from "herdr";
import { worktreeSlug } from "./model.js";

// Where a space is now: its first terminal's live folder, else where it was made.
export function spaceFolder(space) {
  if (!space) return null;
  const tabs = new Set((space.tabs ?? []).map((c) => c.id));
  return panes().find((p) => tabs.has(p.tab) && p.cwd)?.cwd || space.folder || null;
}

// The space to start from: the one shown.
export const currentSpace = () => spaces().find((s) => s.active) ?? null;

const git = async (cwd, args, on) => {
  const r = await exec("git", args, { cwd, on: on ?? undefined });
  return { ok: r.code === 0, out: r.stdout.trim(), err: r.stderr.trim() };
};

// The repo a folder is in: { top, main, name }, or null outside git.
export async function repoOf(folder, on) {
  if (!folder) return null;
  try {
    const top = await git(folder, ["rev-parse", "--show-toplevel"], on);
    const common = await git(folder, ["rev-parse", "--path-format=absolute", "--git-common-dir"], on);
    if (!top.ok || !common.ok) return null;
    const main = common.out.endsWith("/.git") ? common.out.slice(0, -5) : common.out;
    const current = await git(top.out, ["symbolic-ref", "--short", "-q", "HEAD"], on);
    const dirty = (await git(top.out, ["--no-optional-locks", "status", "--porcelain", "--untracked-files=no"], on)).out !== "";
    return { top: top.out, main, name: main.split("/").pop(), branch: current.ok ? current.out : null, dirty };
  } catch {
    return null;
  }
}

// The folder of the worktree that has branch checked out, or null.
async function worktreeFor(repo, branch, on) {
  const r = await git(repo.top, ["worktree", "list", "--porcelain"], on);
  if (!r.ok) return null;
  let path = null;
  for (const line of r.out.split("\n")) {
    if (line.startsWith("worktree ")) path = line.slice(9);
    else if (line === `branch refs/heads/${branch}` && path) return path;
  }
  return null;
}

export async function remoteHome(on) {
  if (!on) return null;
  const r = await exec("sh", ["-c", 'printf %s "$HOME"'], { on });
  if (r.code !== 0 || !r.stdout.trim()) throw new Error("Couldn't find the home folder on " + on + ".");
  return r.stdout.trim();
}

// Makes the branch. plan: { mode: "worktree" | "checkout", branch, repo, on,
// home, space, agent: { run } | null, task, tabName }. Resolves with a short
// description of what it did; rejects with a message for the person.
export async function startBranch(plan) {
  const { mode, branch, repo, on } = plan;
  const check = await git(repo.top, ["check-ref-format", "--branch", branch], on);
  if (!check.ok) throw new Error(`"${branch}" isn't a valid branch name.`);
  const exists = (await git(repo.main, ["show-ref", "--verify", "--quiet", `refs/heads/${branch}`], on)).ok;
  const agent = plan.agent ? { argv: plan.agent.run, task: plan.task } : {};

  // Already on it here: nothing to make, just the agent if one was asked for.
  if (repo.branch === branch) {
    if (plan.agent) run("tab.new", { space: plan.space?.id, folder: repo.top, name: plan.tabName, ...agent });
    return `already on ${branch} here`;
  }

  if (mode === "worktree") {
    // Started before: go to that checkout's space (or open one on it).
    const existing = await worktreeFor(repo, branch, on);
    if (existing) {
      const open = spaces().find((sp) => (sp.machine ?? null) === (on ?? null) && spaceFolder(sp) === existing);
      if (open && !plan.agent) run("space.select", { id: open.id });
      else if (open) run("tab.new", { space: open.id, folder: existing, name: plan.tabName, ...agent });
      else run("space.new", { folder: existing, machine: on ?? "", tab: plan.tabName, ...agent });
      return `went back to the ${branch} worktree`;
    }
    const path = `${plan.home}/.herdr/worktrees/${repo.name}/${worktreeSlug(branch)}`;
    const add = exists ? ["worktree", "add", path, branch] : ["worktree", "add", "-b", branch, path, "HEAD"];
    const r = await git(repo.top, add, on);
    if (!r.ok) throw new Error(r.err || "git worktree add failed.");
    // Unnamed, so the space follows its checkout and nests under the repo.
    run("space.new", { folder: path, machine: on ?? "", tab: plan.tabName, ...agent });
    return exists ? `opened ${branch} in a new worktree` : `created ${branch} in a new worktree`;
  }

  const r = await git(repo.top, exists ? ["checkout", branch] : ["checkout", "-b", branch], on);
  if (!r.ok) throw new Error(r.err || "git checkout failed.");
  if (plan.agent) run("tab.new", { space: plan.space?.id, folder: repo.top, name: plan.tabName, ...agent });
  return exists ? `switched to ${branch}` : `created and switched to ${branch}`;
}
