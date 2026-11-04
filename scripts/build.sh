#!/bin/sh
# Produces ./bin/herdr-jira. herdr runs this as the manifest's [[build]] step
# after `herdr plugin install` clones the repo; you can also run it by hand
# from anywhere.
#
# With Go installed it builds from source. Without Go it downloads the
# prebuilt binary for this platform from the GitHub release matching the
# version in herdr-plugin.toml, and installs it only if its SHA-256 matches
# checksums.txt.
set -eu
cd "$(dirname "$0")/.."

mkdir -p bin

if command -v go >/dev/null 2>&1 && [ -z "${HERDR_JIRA_FORCE_DOWNLOAD:-}" ]; then
	echo "herdr-jira: building from source..." >&2
	exec go build -trimpath -ldflags "-s -w" -o bin/herdr-jira .
fi

# Anchored, because min_herdr_version also contains the word "version".
version=$(sed -n 's/^version *= *"\(.*\)"/\1/p' herdr-plugin.toml | head -1)
if [ -z "$version" ]; then
	echo "herdr-jira: can't read the version from herdr-plugin.toml" >&2
	exit 1
fi

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*)
	echo "herdr-jira: no prebuilt binary for $(uname -s); install Go (https://go.dev/dl) and retry." >&2
	exit 1
	;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*)
	echo "herdr-jira: no prebuilt binary for $(uname -m); install Go (https://go.dev/dl) and retry." >&2
	exit 1
	;;
esac

name="herdr-jira-$os-$arch"
# HERDR_JIRA_RELEASE_BASE lets tests point at a local directory (file://).
base="${HERDR_JIRA_RELEASE_BASE:-https://github.com/craicoverflow/herdr-jira/releases/download/v$version}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "herdr-jira: downloading ${name} v${version}..." >&2
curl -fsSL -o "$tmp/$name" "$base/$name" || {
	echo "herdr-jira: couldn't download $base/$name" >&2
	exit 1
}
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || {
	echo "herdr-jira: couldn't download $base/checksums.txt" >&2
	exit 1
}

# checksums.txt lines are "<hash>  <filename>".
want=$(awk -v f="$name" '$2 == f { print $1 }' "$tmp/checksums.txt")
if [ -z "$want" ]; then
	echo "herdr-jira: $name is not listed in checksums.txt" >&2
	exit 1
fi
if command -v shasum >/dev/null 2>&1; then
	got=$(shasum -a 256 "$tmp/$name" | awk '{ print $1 }')
elif command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$name" | awk '{ print $1 }')
else
	echo "herdr-jira: need shasum or sha256sum to verify the download" >&2
	exit 1
fi
if [ "$got" != "$want" ]; then
	echo "herdr-jira: checksum mismatch for $name (got $got, want $want); not installing" >&2
	exit 1
fi

chmod +x "$tmp/$name"
mv "$tmp/$name" bin/herdr-jira
echo "herdr-jira: installed bin/herdr-jira" >&2
