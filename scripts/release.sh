#!/usr/bin/env bash
# Releases are published by .github/workflows/release.yml: every push to main
# ships the next patch version automatically. Use this script only to bump
# the minor or major version (or to re-run a patch release by hand):
#
#   scripts/release.sh minor     # v0.1.7 → v0.2.0
#   scripts/release.sh major     # v0.2.3 → v1.0.0
#
# Needs: gh (logged in). Releases the current origin/main.
set -euo pipefail

bump="${1:?usage: scripts/release.sh patch|minor|major}"
case "$bump" in patch | minor | major) ;; *) echo "usage: scripts/release.sh patch|minor|major" >&2; exit 1 ;; esac
cd "$(dirname "$0")/.."

gh workflow run release.yml --ref main -f bump="$bump"
echo "Started a $bump release of origin/main. Follow it with: gh run watch"
