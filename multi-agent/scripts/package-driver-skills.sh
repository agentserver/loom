#!/usr/bin/env bash
# Build the Claude driver skill bundle from the committed git tree.
#
# The archive intentionally uses the contents of <tag>:skills as its root, so
# extracting it into .claude/skills creates .claude/skills/<skill>/... directly.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
TAG="HEAD"
OUT=""

usage() {
  cat <<'EOF'
Usage: package-driver-skills.sh [--repo-root PATH] [--tag REF] [--out PATH]

Options:
  --repo-root PATH   repository root containing skills/ (default: auto-detect)
  --tag REF          git ref to package, e.g. v0.0.5 or HEAD (default: HEAD)
  --out PATH         output tarball path (default: <repo-root>/dist/driver-skills.tar.gz)
EOF
}

while (( $# )); do
  case "$1" in
    --repo-root) REPO_ROOT="$2"; shift 2 ;;
    --tag)       TAG="$2"; shift 2 ;;
    --out)       OUT="$2"; shift 2 ;;
    -h|--help)   usage; exit 0 ;;
    *)           echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
  esac
done

REPO_ROOT="$(cd "$REPO_ROOT" && pwd)"
OUT="${OUT:-$REPO_ROOT/dist/driver-skills.tar.gz}"
mkdir -p "$(dirname "$OUT")"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

git -C "$REPO_ROOT" rev-parse --verify "$TAG:skills" >/dev/null
git -C "$REPO_ROOT" archive --format=tar --output "$tmpdir/driver-skills.tar" "$TAG:skills"
gzip -n -c "$tmpdir/driver-skills.tar" > "$tmpdir/driver-skills.tar.gz"

git -C "$REPO_ROOT" ls-tree -d --name-only "$TAG:skills" | sort > "$tmpdir/expected-skills.txt"
if [[ ! -s "$tmpdir/expected-skills.txt" ]]; then
  echo "no skill directories found in $TAG:skills" >&2
  exit 1
fi

tar -tzf "$tmpdir/driver-skills.tar.gz" \
  | awk -F/ 'NF == 2 && $2 == "SKILL.md" { print $1 }' \
  | sort -u > "$tmpdir/actual-skills.txt"

if ! diff -u "$tmpdir/expected-skills.txt" "$tmpdir/actual-skills.txt"; then
  echo "driver skill bundle does not match $TAG:skills" >&2
  exit 1
fi

if tar -tzf "$tmpdir/driver-skills.tar.gz" | grep -q '^skills/'; then
  echo "driver skill bundle must not contain an extra skills/ path prefix" >&2
  exit 1
fi

mv "$tmpdir/driver-skills.tar.gz" "$OUT"
echo "wrote $OUT"
