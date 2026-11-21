#!/usr/bin/env bash
# Build release binaries and publish them as a GitHub release.
#
#   scripts/release.sh v0.2.0
#
# Needs: go, gh (logged in), a clean working tree on main, pushed.
set -euo pipefail

tag="${1:?usage: scripts/release.sh vX.Y.Z}"
cd "$(dirname "$0")/.."

[ -z "$(git status --porcelain)" ] || { echo "working tree is not clean" >&2; exit 1; }
git fetch -q origin
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || { echo "HEAD is not origin/main; push first" >&2; exit 1; }

go test ./...

rm -rf dist
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os="${target%/*}" arch="${target#*/}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$tag" -o "dist/lazymage-$os-$arch" .
done
(cd dist && shasum -a 256 lazymage-* > checksums.txt)

# gh creates the tag on GitHub at this commit, so no git push credentials are needed.
gh release create "$tag" dist/* --target "$(git rev-parse HEAD)" \
  --title "lazymage $tag" --generate-notes
git fetch -q --tags origin
