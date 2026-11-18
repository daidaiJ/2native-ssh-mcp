#!/usr/bin/env bash
# Build the per-platform archive that ships the binary together with both
# Agent Skills. Called by .github/workflows/release.yml after each build.
set -euo pipefail

if [ "$#" -lt 4 ] || [ "$#" -gt 5 ]; then
  echo "usage: $0 <goos> <arch> <binary> <output-dir> [source-root]" >&2
  exit 2
fi

goos=$1
arch=$2
binary=$3
outdir=$4

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_root=${5:-$repo_root}
source_root="$(cd "$source_root" && pwd)"
name="2native-ssh-mcp-${goos}-${arch}"

[ -f "$binary" ] || { echo "binary not found: $binary" >&2; exit 1; }
mkdir -p "$outdir"
outdir="$(cd "$outdir" && pwd)"

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
root="$stage/$name"
mkdir -p "$root/skills"
cp "$binary" "$root/"
cp -R "$source_root/skills/." "$root/skills/"
cp "$source_root/README.md" "$source_root/README.en.md" "$source_root/LICENSE" "$root/"
chmod 0644 "$root/README.md" "$root/README.en.md" "$root/LICENSE"
find "$root/skills" -type f -exec chmod 0644 {} +
if [ "$goos" != windows ]; then
  chmod 0755 "$root/$(basename "$binary")"
fi

case "$goos" in
  windows)
    archive="$outdir/$name-with-skills.zip"
    rm -f "$archive"
    (cd "$stage" && zip -qr "$archive" "$name")
    ;;
  *)
    archive="$outdir/$name-with-skills.tar.gz"
    rm -f "$archive"
    tar -czf "$archive" -C "$stage" "$name"
    ;;
esac

echo "created $archive"
