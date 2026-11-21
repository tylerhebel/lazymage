#!/bin/sh
# Install the latest lazymage release binary.
#
#   curl -fsSL https://raw.githubusercontent.com/tylerhebel/lazymage/main/install.sh | sh
#
# Options (environment variables):
#   LAZYMAGE_VERSION      release tag to install, e.g. v0.1.0 (default: latest)
#   LAZYMAGE_INSTALL_DIR  where to put the binary (default: ~/.local/bin)
set -eu

repo="tylerhebel/lazymage"
dir="${LAZYMAGE_INSTALL_DIR:-$HOME/.local/bin}"
version="${LAZYMAGE_VERSION:-latest}"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "lazymage: unsupported OS $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "lazymage: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

asset="lazymage-$os-$arch"
if [ "$version" = latest ]; then
  url="https://github.com/$repo/releases/latest/download/$asset"
else
  url="https://github.com/$repo/releases/download/$version/$asset"
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
echo "Downloading $url"
curl -fsSL "$url" -o "$tmp"
chmod +x "$tmp"

mkdir -p "$dir"
mv "$tmp" "$dir/lazymage"
trap - EXIT
# Files fetched by a browser get quarantined by Gatekeeper; curl downloads
# don't, but clear the flag anyway in case.
if [ "$os" = darwin ]; then
  xattr -d com.apple.quarantine "$dir/lazymage" 2>/dev/null || true
fi

echo "Installed $("$dir/lazymage" --version) to $dir/lazymage"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    echo
    echo "$dir is not on your PATH. Add it, then open a new terminal:"
    echo "  echo 'export PATH=\"$dir:\$PATH\"' >> ~/.zshrc"
    ;;
esac
