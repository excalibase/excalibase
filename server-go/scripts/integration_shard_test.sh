#!/usr/bin/env bash
# Checks integration-shard.sh and merge-coverprofiles.sh: every test lands in
# exactly one shard of its package, and merged coverage sums the shards.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
shard="$here/integration-shard.sh"
merge="$here/merge-coverprofiles.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0
fail() { echo "FAIL: $*"; failures=$((failures + 1)); }

mkdir -p "$work/internal/a" "$work/internal/b/c" "$work/pkg/d"
cat > "$work/internal/a/a_test.go" <<'GO'
func TestMain(m *testing.M) {}
func TestAlpha(t *testing.T) {}
func TestBeta(t *testing.T) {}
func TestGamma(t *testing.T) {}
func helperTest(t *testing.T) {}
GO
cat > "$work/internal/a/more_test.go" <<'GO'
func TestDelta(t *testing.T) {}
func ExampleThing() {}
GO
cat > "$work/internal/b/c/c_test.go" <<'GO'
func TestAlpha(t *testing.T) {}
func FuzzParse(f *testing.F) {}
GO
cat > "$work/pkg/d/d_test.go" <<'GO'
func TestOnly(t *testing.T) {}
GO

names_of() { sed -E 's/^\^\((.*)\)\$$/\1/' | tr '|' '\n' | grep -v '^$' | sort -u; }

# Every name of the fixture is selected by some shard, and none is invented.
for total in 1 2 3 5; do
  all=""
  for i in $(seq 1 "$total"); do
    all+=$("$shard" "$i" "$total" "$work" | names_of)$'\n'
  done
  got=$(echo "$all" | grep -v '^$' | sort -u | tr '\n' ' ')
  want="ExampleThing FuzzParse TestAlpha TestBeta TestDelta TestGamma TestOnly "
  [[ "$got" == "$want" ]] || fail "total=$total union is '$got', want '$want'"
done

# Within one package a test runs in exactly one shard: package a's four tests and
# one example split over two shards are five names, none of them twice.
count=0
for i in 1 2; do
  count=$((count + $("$shard" "$i" 2 "$work" | names_of | grep -cE '^(TestBeta|TestDelta|TestGamma|ExampleThing)$' || true)))
done
[[ $count -eq 4 ]] || fail "package a's other names were selected $count times over 2 shards, want 4"

"$shard" 1 3 "$work" | names_of | grep -qx TestMain && fail "TestMain is not a test and must not be selected"
"$shard" 1 1 "$work" | grep -qE '^\^\(.+\)\$$' || fail "the pattern is not anchored as ^(...)\$"

for bad in "0 2" "3 2" "x 2" "1 0" "1"; do
  # shellcheck disable=SC2086
  if "$shard" $bad "$work" >/dev/null 2>&1; then fail "arguments '$bad' were accepted"; fi
done

# The real tree: the shards together select every test function there is.
root="$here/.."
want=$(grep -rhoE '^func (Test|Example|Fuzz)[A-Za-z0-9_]*\(' --include='*_test.go' "$root/internal" "$root/pkg" \
  | sed -E 's/^func //; s/\($//' | grep -vx TestMain | sort -u)
got=$(for i in 1 2 3 4; do "$shard" "$i" 4 "$root" | names_of; done | sort -u)
[[ "$got" == "$want" ]] || fail "the four shards of the real tree do not cover every test"

# Merging: one mode line, counts of the same block summed, order kept.
printf 'mode: atomic\nx.go:1.1,2.2 1 0\ny.go:3.1,4.2 2 5\n' > "$work/c1.out"
printf 'mode: atomic\nx.go:1.1,2.2 1 3\nz.go:5.1,6.2 1 0\n' > "$work/c2.out"
"$merge" "$work/merged.out" "$work/c1.out" "$work/c2.out"
want_merged=$'mode: atomic\nx.go:1.1,2.2 1 3\ny.go:3.1,4.2 2 5\nz.go:5.1,6.2 1 0'
[[ "$(cat "$work/merged.out")" == "$want_merged" ]] || fail "merged profile is: $(cat "$work/merged.out")"
if "$merge" "$work/none.out" >/dev/null 2>&1; then fail "a merge of no profiles was accepted"; fi

[[ $failures -eq 0 ]] || { echo "$failures failure(s)"; exit 1; }
echo "integration shard checks passed"
