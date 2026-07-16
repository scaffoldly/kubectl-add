#!/usr/bin/env bash
# Render the Homebrew tap formula for a release tag: fetch that release's
# archives, compute their sha256s, and fill the template. Prints the formula to
# stdout. Requires: gh (authenticated), sha256sum, sed.
#
#   packaging/homebrew/render.sh v0.2.0 > Formula/kubectl-add.rb
set -euo pipefail

tag="${1:?usage: render.sh <tag>}"
ver="${tag#v}"
repo="${REPO:-scaffoldly/kubectl-add}"
here="$(cd "$(dirname "$0")" && pwd)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# sha256 of a release archive for the given os_arch.
archive_sha() {
  gh release download "$tag" -R "$repo" --pattern "kubectl-add_${1}.zip" --dir "$tmp" --clobber >/dev/null
  sha256sum "$tmp/kubectl-add_${1}.zip" | cut -d' ' -f1
}

sed -e "s/@@VERSION@@/${ver}/g" \
  -e "s/@@SHA_DARWIN_ARM64@@/$(archive_sha darwin_arm64)/g" \
  -e "s/@@SHA_DARWIN_AMD64@@/$(archive_sha darwin_amd64)/g" \
  -e "s/@@SHA_LINUX_ARM64@@/$(archive_sha linux_arm64)/g" \
  -e "s/@@SHA_LINUX_AMD64@@/$(archive_sha linux_amd64)/g" \
  "$here/formula.rb.tmpl"
