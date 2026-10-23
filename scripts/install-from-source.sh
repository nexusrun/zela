#!/usr/bin/env bash
# Build the checked-out source, then run its Linux installer without a published release.
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ $(uname -s) != Linux ]]; then
  printf '%s\n' 'Run make install on the Linux manager server. To build elsewhere, use make release and copy the archive.' >&2
  exit 1
fi
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) printf '%s\n' 'Only AMD64 and ARM64 are supported.' >&2; exit 1 ;;
esac
for tool in go python3 git tar; do
  command -v "$tool" >/dev/null || { printf 'Install %s first. Required Go version: %s\n' "$tool" "$(cat "$root/.go-version")" >&2; exit 1; }
done
if [[ $EUID != 0 ]]; then
  command -v sudo >/dev/null || { printf '%s\n' 'sudo is required to install system packages and services.' >&2; exit 1; }
fi
build_dir=$(mktemp -d)
trap 'rm -rf -- "$build_dir"' EXIT
python3 "$root/scripts/release.py" --version v0.0.0-local --output "$build_dir/release"
tar -xzf "$build_dir/release/zela-v0.0.0-local-linux-$arch.tar.gz" -C "$build_dir"
bundle="$build_dir/zela-v0.0.0-local-linux-$arch"
if [[ $EUID == 0 ]]; then
  bash "$bundle/install.sh" --bundle-dir "$bundle"
else
  sudo bash "$bundle/install.sh" --bundle-dir "$bundle"
fi
