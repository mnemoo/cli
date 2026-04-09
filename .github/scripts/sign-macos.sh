#!/usr/bin/env bash
# sign-macos.sh — post-build hook from goreleaser. Ad-hoc signs darwin binaries.
set -e

# Skip non-darwin build targets (goreleaser sets GOOS for each build).
[[ "${GOOS:-}" = "darwin" ]] || exit 0

# Skip if codesign isn't available (e.g. Linux CI snapshot job).
command -v codesign >/dev/null || exit 0

sudo codesign --force --sign - "$1"
