# AGENTS.md

Notes for agents working in this repo.

- Demo mode: run `./bin/herdr-jira api --demo <op>` with `HERDR_PLUGIN_STATE_DIR` set to a temp dir to try ops offline; `herdr-app do jira.key:2` / `herdr-app do jira.search:foo` drive the open dialog and `herdr-app shot` screenshots it.
- Demo create: `HERDR_PLUGIN_STATE_DIR=$(mktemp -d) ./bin/herdr-jira api --demo create --project WEB --type Task --summary x`. Installing the demo mod overwrites `~/.config/herdr-app/mods/jira`.
- The helper binary embeds the mod: run `sh scripts/build.sh` before `herdr-jira app install --demo`, or the app keeps the old JS. Each reinstall closes the dialog; reopen it with `herdr-app do mod.open --name jira`.
