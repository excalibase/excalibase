#!/usr/bin/env sh
# EXC-682: init.sh --grow-subids makes room in /etc/subuid and /etc/subgid for
# every app's own user range, and only ever adds a line: it never shrinks,
# rewrites or clobbers an entry. Run on temp copies of the files.
#
#   sh deploy/single-host/tests/subids.sh
set -eu

DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
me=$(id -un)
export SUBID_DIR="$WORK/etc"
mkdir "$SUBID_DIR"
grow() { sh "$DIR/../init.sh" --grow-subids "$me" "$@"; }

for file in subuid subgid; do
  printf 'other:100000:65536\n%s:165536:65536\nlast:231072:65536\n' "$me" >"$SUBID_DIR/$file"
done
cp "$SUBID_DIR/subuid" "$WORK/before"

# 3 apps and the shared range: 4 x 65536 ids; 65536 are held, 196608 are added after everything else.
grow --max-apps 3 >/dev/null || fail "growing for 3 apps"
for file in subuid subgid; do
  [ "$(tail -n1 "$SUBID_DIR/$file")" = "$me:296608:196608" ] || fail "$file: last line is $(tail -n1 "$SUBID_DIR/$file")"
done
[ "$(head -n3 "$SUBID_DIR/subuid")" = "$(cat "$WORK/before")" ] || fail "an existing entry changed"
[ "$(wc -l <"$SUBID_DIR/subuid")" = 4 ] || fail "more than one line added"

# Idempotent: a second run, and a smaller one, change nothing.
cp "$SUBID_DIR/subuid" "$WORK/grown"
grow --max-apps 3 >/dev/null || fail "second run"
grow --max-apps 1 >/dev/null || fail "a smaller max"
cmp -s "$WORK/grown" "$SUBID_DIR/subuid" || fail "a repeat or a smaller max changed the file"

# A larger max adds only what is missing.
grow --max-apps 5 >/dev/null || fail "growing for 5 apps"
[ "$(tail -n1 "$SUBID_DIR/subuid")" = "$me:493216:131072" ] || fail "second growth: $(tail -n1 "$SUBID_DIR/subuid")"
[ "$(head -n4 "$SUBID_DIR/subuid")" = "$(cat "$WORK/grown")" ] || fail "growing rewrote an earlier line"

# A user the files do not name gets the whole pool; a file without a final newline keeps its last entry.
printf 'other:100000:65536' >"$SUBID_DIR/subuid"
cp "$SUBID_DIR/subuid" "$WORK/nonl"
grow --max-apps 2 >/dev/null || fail "an unnamed user"
[ "$(sed -n 1p "$SUBID_DIR/subuid")" = "other:100000:65536" ] || fail "the entry before the new line was damaged"
[ "$(tail -n1 "$SUBID_DIR/subuid")" = "$me:165536:196608" ] || fail "unnamed user: $(tail -n1 "$SUBID_DIR/subuid")"

# An empty or missing file starts at 100000.
rm -f "$SUBID_DIR/subuid"
grow --max-apps 1 >/dev/null || fail "a missing file"
[ "$(cat "$SUBID_DIR/subuid")" = "$me:100000:131072" ] || fail "missing file: $(cat "$SUBID_DIR/subuid")"

# Refusals write nothing.
cp "$SUBID_DIR/subgid" "$WORK/gid"
for bad in "--max-apps 0" "--max-apps x" "--max-apps 100000000"; do
  # shellcheck disable=SC2086
  if grow $bad >/dev/null 2>&1; then fail "'$bad' was accepted"; fi
done
if sh "$DIR/../init.sh" --grow-subids no-such-user-exc682 --max-apps 2 >/dev/null 2>&1; then fail "an unknown user was accepted"; fi
cmp -s "$WORK/gid" "$SUBID_DIR/subgid" || fail "a refused run changed a file"
if [ "$(id -u)" != 0 ]; then
  if env -u SUBID_DIR sh "$DIR/../init.sh" --grow-subids "$me" --max-apps 2 2>"$WORK/err" >/dev/null; then fail "a non-root run wrote the real files"; fi
  grep -q "as root" "$WORK/err" || fail "no word about root: $(cat "$WORK/err")"
fi
# The mode writes no .env.
[ ! -e "$WORK/.env" ] && [ ! -e .env ] || fail "growing wrote an env file"
echo "ok: subids"
