// The Jira helper: the herdr-jira binary, which signs in to Atlassian and
// talks to Jira (`herdr-jira api <op>`, one JSON reply on stdout). `herdr-jira
// app install` puts a copy in this mod's hidden .bin folder; one on PATH or in
// ~/.local/bin works too.

import { exec, appHome, home } from "herdr";

const candidates = () => [
  `${appHome()}/mods/jira/.bin/herdr-jira`,
  `${home()}/.local/bin/herdr-jira`,
  "herdr-jira",
];

let found = null;

// Runs one op. Resolves with { ok, data } or { ok: false, error: { code,
// message } }; never rejects. Codes the mod acts on: signed_out, no_helper,
// helper (an old or broken helper), network, consent_blocked, multiple_sites.
export async function api(...args) {
  const order = found ? [found, ...candidates().filter((b) => b !== found)] : candidates();
  for (const bin of order) {
    let r;
    try {
      r = await exec(bin, ["api", ...args.map(String)]);
    } catch {
      continue; // not there; try the next place
    }
    found = bin;
    const line = r.stdout.trim().split("\n").filter(Boolean).pop() ?? "";
    try {
      const reply = JSON.parse(line);
      if (typeof reply?.ok === "boolean") return reply;
    } catch {}
    const said = (r.stderr || r.stdout || `exited with ${r.code}`).trim().split("\n").slice(-3).join(" ");
    return { ok: false, error: { code: "helper", message: `The Jira helper (${bin}) didn't answer as expected: ${said.slice(0, 300)}` } };
  }
  found = null;
  return { ok: false, error: { code: "no_helper", message: "The herdr-jira helper isn't installed." } };
}

export const helperPath = () => found;
