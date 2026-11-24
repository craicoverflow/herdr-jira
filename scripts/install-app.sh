#!/bin/sh
# Installs, or updates, Jira for the Herdr app:
#
#   curl -fsSL https://raw.githubusercontent.com/craicoverflow/herdr-jira/main/scripts/install-app.sh | sh
#
# It downloads the herdr-jira release binary for this platform, checks its
# SHA-256 against the release's checksums.txt, and runs `herdr-jira app
# install` with it. That copies the mod into the Herdr app's folder with the
# binary as its helper, and binds cmd-j (ctrl-shift-j on Linux) unless
# something already opens the mod. The download is deleted afterwards. Run it
# again to update; a second run with nothing new changes nothing.
#
# Arguments go to `app install`, e.g. `| sh -s -- --demo` or `--no-key`.
# HERDR_JIRA_VERSION installs a given release (e.g. v0.5.0) instead of the
# latest.
set -eu

# Everything runs from main, called on the last line, so a download cut off
# partway runs nothing.
main() {
	repo=craicoverflow/herdr-jira

	case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) die "no prebuilt binary for $(uname -s); see https://github.com/$repo/blob/main/app/README.md" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "no prebuilt binary for $(uname -m); see https://github.com/$repo/blob/main/app/README.md" ;;
	esac
	command -v curl >/dev/null 2>&1 || die "need curl to download the release"

	# HERDR_JIRA_RELEASE_BASE lets tests point at a local directory (file://).
	if [ -n "${HERDR_JIRA_RELEASE_BASE:-}" ]; then
		tag=${HERDR_JIRA_VERSION:-test}
		base=$HERDR_JIRA_RELEASE_BASE
	else
		if [ -n "${HERDR_JIRA_VERSION:-}" ]; then
			tag=v${HERDR_JIRA_VERSION#v}
		else
			# The latest release's page redirects to .../releases/tag/<tag>.
			# Resolving it once keeps the binary and checksums.txt from the
			# same release, without the rate-limited API.
			url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
				die "couldn't find the latest release of $repo"
			tag=${url##*/}
			case "$tag" in
			v*) ;;
			*) die "couldn't find the latest release of $repo (got $url)" ;;
			esac
		fi
		base=https://github.com/$repo/releases/download/$tag
	fi

	name=herdr-jira-$os-$arch
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' HUP INT TERM

	say "downloading $name $tag..."
	curl -fsSL -o "$tmp/$name" "$base/$name" || die "couldn't download $base/$name"
	curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "couldn't download $base/checksums.txt"

	# checksums.txt lines are "<hash>  <filename>".
	want=$(awk -v f="$name" '$2 == f { print $1 }' "$tmp/checksums.txt")
	[ -n "$want" ] || die "$name is not listed in $tag's checksums.txt"
	if command -v shasum >/dev/null 2>&1; then
		got=$(shasum -a 256 "$tmp/$name" | awk '{ print $1 }')
	elif command -v sha256sum >/dev/null 2>&1; then
		got=$(sha256sum "$tmp/$name" | awk '{ print $1 }')
	else
		die "need shasum or sha256sum to verify the download"
	fi
	[ "$got" = "$want" ] || die "checksum mismatch for $name (got $got, want $want); not installing"

	bin=$tmp/herdr-jira
	mv "$tmp/$name" "$bin"
	chmod +x "$bin"

	# Releases before the app mod don't have `app install`; their usage
	# doesn't list it.
	if ! "$bin" </dev/null 2>/dev/null | grep -q 'app install'; then
		die "$tag predates the Herdr app mod; it needs a later release"
	fi

	"$bin" app install "$@" </dev/null
}

say() {
	echo "herdr-jira: $*" >&2
}

die() {
	say "$*"
	exit 1
}

main "$@"
