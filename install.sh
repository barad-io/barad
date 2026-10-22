#!/bin/sh
# Installs barad, Barad in the terminal.
#
#   curl -fsSL https://github.com/barad-io/barad/releases/latest/download/install.sh | sh
#
# It downloads the binary for this computer from the same release as this
# script, checks it against the release's SHA256SUMS, and, when the GitHub CLI
# is signed in, verifies the build's provenance attestation. It installs to
# ~/.local/bin, or to $BARAD_INSTALL_DIR. Nothing runs as root.
set -eu

# The release workflow writes its tag here.
VERSION="__VERSION__"
REPO="barad-io/barad"

say() { printf '%s\n' "$*"; }
fail() { printf 'barad install: %s\n' "$*" >&2; exit 1; }

main() {
	case "$VERSION" in
	__*) fail "this script is a template; use the one attached to a release" ;;
	esac

	case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) fail "$(uname -s) is not supported here; download barad from https://github.com/$REPO/releases" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) fail "$(uname -m) is not supported" ;;
	esac

	command -v curl >/dev/null 2>&1 || fail "curl is required"
	if command -v sha256sum >/dev/null 2>&1; then
		sum() { sha256sum "$1" | cut -d' ' -f1; }
	elif command -v shasum >/dev/null 2>&1; then
		sum() { shasum -a 256 "$1" | cut -d' ' -f1; }
	else
		fail "sha256sum or shasum is required"
	fi

	name="barad-$os-$arch"
	base="https://github.com/$REPO/releases/download/$VERSION"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	trap 'exit 1' INT TERM

	say "Downloading barad $VERSION for $os/$arch"
	curl -fsSL "$base/$name" -o "$tmp/$name" || fail "could not download $base/$name"
	curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || fail "could not download $base/SHA256SUMS"

	want=$(awk -v f="$name" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
	[ -n "$want" ] || fail "SHA256SUMS has no entry for $name"
	[ "$(sum "$tmp/$name")" = "$want" ] || fail "$name does not match SHA256SUMS"
	say "Checksum matches"

	if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
		gh attestation verify "$tmp/$name" --repo "$REPO" \
			--signer-workflow "$REPO/.github/workflows/release.yml" >/dev/null ||
			fail "$name has no valid provenance attestation from $REPO"
		say "Provenance verified: built by $REPO's release workflow"
	else
		say "To verify where it was built: gh attestation verify \$(command -v barad) --repo $REPO"
	fi

	dir="${BARAD_INSTALL_DIR:-$HOME/.local/bin}"
	mkdir -p "$dir"
	chmod 755 "$tmp/$name"
	mv "$tmp/$name" "$dir/barad"
	say "Installed $dir/barad"

	case ":$PATH:" in
	*":$dir:"*) say "Run: barad" ;;
	*) say "Add $dir to your PATH, then run: barad" ;;
	esac
}

main "$@"
