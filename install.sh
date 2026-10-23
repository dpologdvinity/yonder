#!/bin/sh
# Install yonder: curl -fsSL https://raw.githubusercontent.com/dpologdvinity/yonder/master/install.sh | sh
# Downloads the latest release for this OS and CPU, checks its SHA-256, and installs
# to /usr/local/bin if writable, otherwise ~/.local/bin. Set YONDER_INSTALL_DIR to override.
set -eu

repo="dpologdvinity/yonder"

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "yonder: unsupported OS $(uname -s); see https://github.com/$repo/releases" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "yonder: unsupported CPU $(uname -m)" >&2; exit 1 ;;
esac

tag=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" | sed -n 's/.*"tag_name": *"\(v[^"]*\)".*/\1/p')
[ -n "$tag" ] || { echo "yonder: could not find the latest release" >&2; exit 1; }
ver=${tag#v}
file="yonder_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$file" "$base/$file"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

want=$(awk -v f="$file" '$2 == f {print $1}' "$tmp/checksums.txt")
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$file" | awk '{print $1}')
else
  got=$(shasum -a 256 "$tmp/$file" | awk '{print $1}')
fi
[ -n "$want" ] && [ "$want" = "$got" ] || { echo "yonder: checksum mismatch for $file" >&2; exit 1; }

tar -xzf "$tmp/$file" -C "$tmp" yonder

dir=${YONDER_INSTALL_DIR:-}
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
install -m 755 "$tmp/yonder" "$dir/yonder" 2>/dev/null || { cp "$tmp/yonder" "$dir/yonder" && chmod 755 "$dir/yonder"; }

echo "yonder $ver installed to $dir/yonder"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Add $dir to your PATH to run it: export PATH=\"$dir:\$PATH\"" ;;
esac
