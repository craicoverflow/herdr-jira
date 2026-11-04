#!/bin/sh
# Produces ./bin/herdr-jira. herdr runs this as the manifest's [[build]] step
# after `herdr plugin install` clones the repo; you can also run it by hand
# from anywhere.
set -eu
cd "$(dirname "$0")/.."

mkdir -p bin

if ! command -v go >/dev/null 2>&1; then
	echo "herdr-jira: Go is required to build this plugin (no prebuilt releases yet). Install Go (https://go.dev/dl) and retry." >&2
	exit 1
fi

echo "herdr-jira: building from source…" >&2
exec go build -trimpath -ldflags "-s -w" -o bin/herdr-jira .
