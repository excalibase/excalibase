#!/usr/bin/env bash
# Proves migration-numbers-test.sh refuses a duplicate and a number at or below main's highest.
set -euo pipefail
here=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dir=server-go/internal/storage/postgres/migrations
cd "$work"
git init -q -b main . && git config user.email t@t && git config user.name t
mkdir -p "$dir" tests
cp "$here/tests/migration-numbers-test.sh" tests/
touch "$dir/000001_a.up.sql" "$dir/000002_b.up.sql"
git add . && git commit -qm base
run() { BASE_REF=main bash tests/migration-numbers-test.sh >/dev/null 2>&1; }
git checkout -qb ok && touch "$dir/000003_c.up.sql" && git add . && git commit -qm ok
run || { echo "a number above main was refused" >&2; exit 1; }
git checkout -qb low main && touch "$dir/000001_x.up.sql" && git add . && git commit -qm low
! run || { echo "a duplicate number was accepted" >&2; exit 1; }
git checkout -qb below main && git mv "$dir/000002_b.up.sql" "$dir/000002_b2.up.sql" && touch "$dir/000000_z.up.sql" && git add . && git commit -qm below
! run || { echo "a number below main's highest was accepted" >&2; exit 1; }
echo "migration number check behaves"
