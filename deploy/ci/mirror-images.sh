#!/usr/bin/env sh
# EXC-881: CI used to pull every third-party image from Docker Hub and hit its pull rate limit, even
# logged in. The images are copied once a day into ghcr.io/excalibase/mirror/ (the Mirror images
# workflow) and CI pulls them from there. deploy/ci/mirror-images.txt is the one list.
#
#   sh deploy/ci/mirror-images.sh ref <ref>            the reference CI pulls for <ref>
#   sh deploy/ci/mirror-images.sh rewrite <file>...    point the Docker Hub images in a file at the mirror
#   sh deploy/ci/mirror-images.sh sync [list]          copy the list into the mirror (needs crane and a login)
#   sh deploy/ci/mirror-images.sh guard                fail when CI names a Docker Hub image the list lacks
#
# A script may also `. deploy/ci/mirror-images.sh` and call mirror_ref. With IMAGE_MIRROR unset
# (a developer's machine) a reference stays what it was, so nothing needs GHCR locally.
#   IMAGE_MIRROR=ghcr.io/excalibase/mirror   what CI sets
#
# Mirror names: the Docker Hub path without the host and without library/ (the paths testcontainers
# builds from TESTCONTAINERS_HUB_IMAGE_NAME_PREFIX), the tag kept. A reference with only a digest
# gets the tag d-<12 digest characters>.

# hub_path <ref>: the Docker Hub path of a reference, or nothing when it names another registry.
hub_path() {
  ref=$1
  case $ref in
    docker.io/*|index.docker.io/*|registry-1.docker.io/*) ref=${ref#*/} ;;
    */*)
      host=${ref%%/*}
      case $host in *.*|*:*|localhost) return 0 ;; esac
      ;;
  esac
  printf '%s\n' "${ref#library/}"
}

# mirror_ref <ref>: the reference CI pulls, the mirror's when IMAGE_MIRROR is set.
mirror_ref() {
  path=$(hub_path "$1")
  if [ -z "${IMAGE_MIRROR:-}" ] || [ -z "$path" ]; then
    printf '%s\n' "$1"
  else
    printf '%s/%s\n' "${IMAGE_MIRROR%/}" "$path"
  fi
}

# list_entries <file>: the references of a list, comments and blank lines dropped.
list_entries() {
  sed -e 's/[[:space:]]*#.*$//' -e '/^[[:space:]]*$/d' "$1"
}

# entry_repo <entry>: the path without tag or digest. entry_tag: its tag, or d-<digest> when it has none.
entry_repo() {
  e=${1%%@*}
  case ${e##*/} in *:*) e=${e%:*} ;; esac
  printf '%s\n' "$e"
}
entry_tag() {
  e=${1%%@*}
  case ${e##*/} in
    *:*) printf '%s\n' "${e##*:}" ;;
    *) d=${1#*@sha256:}; printf 'd-%s\n' "$(printf '%s' "$d" | cut -c1-12)" ;;
  esac
}
entry_digest() {
  case $1 in *@sha256:*) printf 'sha256:%s\n' "${1#*@sha256:}" ;; esac
}

# Hub repositories without a namespace live under library/.
source_repo() {
  case $1 in */*) printf '%s\n' "$1" ;; *) printf 'library/%s\n' "$1" ;; esac
}

# crane copies the index and every manifest byte for byte, so the digests survive. (skopeo refuses
# to write a Docker manifest list to GHCR without converting it.) `crane digest` is a HEAD request.
registry_digest() { # <ref>: the digest the registry serves for the reference, empty when it has none
  crane digest "$1" 2>/dev/null || true
}

# sync_entry copies one image unless the mirror already holds it, so a run after a rate-limit window
# copies only what is missing. Returns 75 when Docker Hub refuses for rate: the caller stops, no retries.
sync_entry() {
  entry=$1
  repo=$(entry_repo "$entry")
  tag=$(entry_tag "$entry")
  digest=$(entry_digest "$entry")
  source="docker.io/$(source_repo "$repo")"
  if [ -n "$digest" ]; then source="$source@$digest"; else source="$source:$tag"; fi
  target="${MIRROR_REGISTRY:-ghcr.io/excalibase/mirror}/$repo:$tag"
  have=$(registry_digest "$target")
  if [ -n "$digest" ]; then want=$digest; else want=$(registry_digest "$source"); fi
  if [ -n "$have" ] && [ "$have" = "$want" ]; then echo "  already mirrored"; return 0; fi
  out=$(crane copy "$source" "$target" 2>&1) || {
    echo "$out" >&2
    # Only the registry's own words count as a rate limit; anything else is a real failure.
    case $out in *[Tt][Oo][Oo][Mm][Aa][Nn][Yy][Rr][Ee][Qq][Uu][Ee][Ss][Tt][Ss]*|*"429 Too Many Requests"*) return 75 ;; esac
    echo "could not copy $entry" >&2
    return 1
  }
  [ -n "$digest" ] || return 0
  # A copy that changed the manifest would break every digest pin, so it is checked, not trusted:
  # the tag and the original digest must both resolve in the mirror, to the same manifest.
  got=$(registry_digest "$target")
  [ "$got" = "$digest" ] || { echo "$entry: the mirror holds ${got:-nothing}, not $digest" >&2; return 1; }
  [ "$(registry_digest "${target%:*}@$digest")" = "$digest" ] || { echo "$entry: $digest does not resolve in the mirror" >&2; return 1; }
}

cmd_sync() {
  list=${1:-$MIRROR_LIST_FILE}
  failed=0
  entries=$(list_entries "$list")
  while IFS= read -r entry; do
    echo "mirror $entry"
    sync_entry "$entry" && continue
    status=$?
    if [ "$status" -eq 75 ]; then
      echo "Docker Hub is rate limiting this run; stopping. Run the workflow again after the window; what is copied stays." >&2
      return 1
    fi
    failed=$((failed + 1))
  done <<EOF
$entries
EOF
  [ "$failed" -eq 0 ] || { echo "$failed image(s) not mirrored" >&2; return 1; }
}

# rewrite_stream: stdin to stdout, each explicit docker.io reference whose image the list names
# pointed at the mirror. Images CI builds itself keep their names.
rewrite_stream() {
  [ -n "${IMAGE_MIRROR:-}" ] || { cat; return; }
  awk -v mirror="${IMAGE_MIRROR%/}" -v list="$MIRROR_LIST_FILE" '
    BEGIN {
      while ((getline line < list) > 0) {
        sub(/[ \t]*#.*$/, "", line)
        if (line == "") continue
        sub(/@.*/, "", line)
        if (line ~ /:[^\/]*$/) line = substr(line, 1, match(line, /:[^\/]*$/) - 1)
        names[line] = 1
      }
    }
    {
      out = ""; rest = $0
      while (match(rest, "docker[.]io/(library/)?[a-z0-9][a-z0-9._/-]*")) {
        token = substr(rest, RSTART, RLENGTH)
        path = token; sub(/^docker[.]io[/]/, "", path); sub(/^library[/]/, "", path)
        # provisioning and studio are built by CI under their release names: never pulled, never mirrored.
        if ((path in names) && path !~ /^excalibase[/](provisioning|studio)$/) token = mirror "/" path
        out = out substr(rest, 1, RSTART - 1) token
        rest = substr(rest, RSTART + RLENGTH)
      }
      print out rest
    }'
}

cmd_rewrite() {
  for file in "$@"; do
    tmp=$(mktemp)
    rewrite_stream < "$file" > "$tmp" && cat "$tmp" > "$file"
    rm -f "$tmp"
  done
}


# Words after `docker run|pull|create` that take a value, so the image is the first word that is not
# a flag or a flag's value.
DOCKER_RUN_AWK='
function image_after(words, n, start,   j, w) {
  for (j = start; j <= n; j++) {
    w = words[j]
    if (w ~ /^-/) { if (w !~ /=/ && w ~ FLAGS_WITH_VALUE) j++; continue }
    gsub(/["\x27]/, "", w)
    if (w == "" || w ~ /[$(){}`]/) return ""
    return w
  }
  return ""
}
BEGIN { FLAGS_WITH_VALUE = "^(-e|--env|-v|--volume|--name|--network|--net|-p|--publish|--env-file|--entrypoint|--stop-signal|--restart|-w|--workdir|--label|-l|--user|-u|--cap-add|--cap-drop|--security-opt|--tmpfs|--hostname|-h|--memory|-m|--cpus|--platform|--pull|--add-host|--device|--dns|--log-driver|--mount|--pid|--ipc|--runtime|--network-alias|--group-add|--ulimit|--sysctl|--health-cmd)$" }
{
  line = $0
  while (line ~ /\\[ \t]*$/ && (getline nxt) > 0) { sub(/\\[ \t]*$/, "", line); line = line " " nxt }
  n = split(line, words, /[ \t]+/)
  for (i = 1; i < n; i++) {
    tool = words[i]; gsub(/["\x27]/, "", tool)
    if (tool ~ /^(docker|podman|[$]DOCKER|[$]ENGINE|[$][{]ENGINE[}]|[$][{]DOCKER[}])$/ && words[i + 1] ~ /^(run|pull|create)$/) {
      ref = image_after(words, n, i + 2)
      if (ref != "") print ref
    }
  }
}'

# guard_candidates: every Docker Hub image the files CI runs name, one normalized path per line.
# Three sources: explicit docker.io references, `image:` lines, and FROM lines of the Dockerfiles.
guard_candidates() {
  (
    # A grep without a match is fine here; under set -e it would end the subshell and hide the rest.
    set +e
    cd "$MIRROR_ROOT"
    # What CI runs: workflows, deploy scripts and compose, chart tests, the Dockerfiles it builds, and
    # the Go files with an integration or live build tag plus the constants provisioning pins.
    scanned=$(git ls-files .github deploy 'charts/*/tests' deno-server/scripts excalibase-vault frontend/Dockerfile deno-server/Dockerfile \
      server-go/Dockerfile | grep -vE '(^|/)(mirror-images\.(txt|sh)|mirror-images-test\.sh)$|\.md$' || true)
    go_files=$(git ls-files server-go | grep -E '\.go$' | while read -r file; do
      if grep -qE '^//go:build .*(integration|live)' "$file" ||
         echo "$file" | grep -qE '/internal/(config/(app_builds|sites_git)|sitebuilds/settings)\.go$'; then echo "$file"; fi
    done)
    go_tagged=$(for file in $go_files; do grep -qE '^//go:build .*(integration|live)' "$file" && echo "$file"; done)
    # shellcheck disable=SC2086 # one word per file
    {
      grep -hoE 'docker\.io/[A-Za-z0-9][A-Za-z0-9._/-]*(:[A-Za-z0-9._-]+)?(@sha256:[0-9a-f]+)?' $scanned $go_files 2>/dev/null
      # Unqualified images in the tagged Go tests: "name:tag" with a version-like tag, not host:port.
      [ -z "$go_tagged" ] || grep -hoE '"[a-z0-9][a-z0-9._/-]*:(v?[0-9][A-Za-z0-9._-]*|latest|main|pg[0-9]+)(@sha256:[0-9a-f]+)?"' $go_tagged 2>/dev/null |
        tr -d '"' | grep -vE '^[0-9.]+:|^localhost:|^[a-z]:[0-9]|^(acme|a|x)/|^[a-z0-9-]+([.][a-z0-9-]+)+:[0-9]+$|^sha256:'
      grep -hE '^[[:space:]]*(-[[:space:]]+)?image:[[:space:]]+[^$[:space:]]+[[:space:]]*$' $scanned 2>/dev/null |
        sed -E 's/^[[:space:]]*(-[[:space:]]+)?image:[[:space:]]+//; s/[[:space:]]+$//' | tr -d "\"'"
      for dockerfile in $(printf '%s\n' $scanned | grep -E '(^|/)Dockerfile[^/]*$'); do
        awk 'toupper($1) == "FROM" {
               ref = $2; if (ref ~ /^--platform/) ref = $3
               gsub(/[$][{]HUB_MIRROR[}]/, "", ref)
               if (ref !~ /[$]/ && !(ref in stage)) print ref
               for (i = 2; i < NF; i++) if (toupper($i) == "AS") stage[$(i + 1)] = 1
             }
             # COPY --from=<image> names an image unless it is an earlier stage
             toupper($1) == "COPY" && $2 ~ /^--from=/ {
               ref = substr($2, 8); gsub(/[$][{]HUB_MIRROR[}]/, "", ref)
               if (ref !~ /[$]/ && !(ref in stage) && ref !~ /^[0-9]+$/) print ref
             }' "$dockerfile"
      done
      # A bare `docker run|pull|create <image>` in a script or a workflow run: line.
      # shellcheck disable=SC2086
      [ -z "$scanned" ] || awk "$DOCKER_RUN_AWK" $scanned
      # An image named in a variable: IMAGE-ish names in workflows (KEY: value) and scripts (KEY=value).
      grep -hE '^[[:space:]]*(export[[:space:]]+)?[A-Za-z0-9_]*IMAGE[A-Za-z0-9_]*[:=][[:space:]]*["'"'"']?[a-z0-9][A-Za-z0-9._/-]*(:[A-Za-z0-9._-]+)?(@sha256:[0-9a-f]+)?["'"'"']?[[:space:]]*$' $scanned 2>/dev/null |
        sed -E 's/^[[:space:]]*(export[[:space:]]+)?[A-Za-z0-9_]*IMAGE[A-Za-z0-9_]*[:=][[:space:]]*//; s/[[:space:]]+$//' | tr -d "\"'"
    } | while read -r ref; do
      path=$(hub_path "$ref")
      [ -n "$path" ] || continue
      # A reference with neither tag nor digest carries a variable (${TAG}): nothing to compare.
      case $path in *:*|*@sha256:*) ;; *) continue ;; esac
      # Built by CI itself, or a name a test uses to prove a pull fails.
      case $path in *:ci|*-candidate:*|*:ci@*|excalibase/does-not-exist:*|*:9.9.9|*never-published)  continue ;; esac
      printf '%s\n' "$path"
    done | sort -u
  )
}

# in_list <path>: 0 when the list names the path: exactly when it has a digest, by name and tag otherwise.
in_list() {
  candidate=$1
  case $candidate in
    *@sha256:*) list_entries "$MIRROR_LIST_FILE" | grep -qxF "$candidate" && return 0
                # tagless digest entries match a tagged candidate with the same digest
                list_entries "$MIRROR_LIST_FILE" | grep -qF "@${candidate#*@}" ;;
    *) list_entries "$MIRROR_LIST_FILE" | sed 's/@sha256:.*$//' | grep -qxF "$candidate" ;;
  esac
}

# set_candidates: the references the CI image sets pull (third-party-images.sh builds them from the code).
set_candidates() {
  for set_name in go-integration single-host-fence single-host-smoke single-host-gvisor single-host-mysql; do
    sh "$MIRROR_ROOT/deploy/ci/third-party-images.sh" list "$set_name"
  done | while read -r ref; do hub_path "$ref"; done | sed '/^$/d' | sort -u
}

cmd_guard() {
  missing=$( { guard_candidates; set_candidates; } | sort -u | while read -r path; do
    in_list "$path" || echo "$path"
  done)
  [ -z "$missing" ] || {
    echo "CI names Docker Hub images that deploy/ci/mirror-images.txt does not list:" >&2
    echo "$missing" | sed 's/^/  /' >&2
    echo "Docker Hub rate-limits CI, so CI pulls these from ghcr.io/excalibase/mirror/ instead (EXC-881)." >&2
    echo "Add each line above to deploy/ci/mirror-images.txt and run the 'Mirror images' workflow once;" >&2
    echo "the daily run keeps it fresh afterwards." >&2
    return 1
  }
  echo "every Docker Hub image CI names is in deploy/ci/mirror-images.txt"
}

# Run as a command, not when sourced.
case ${0##*/} in
  mirror-images.sh)
    set -eu
    MIRROR_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
    MIRROR_LIST_FILE=${MIRROR_LIST:-$MIRROR_ROOT/deploy/ci/mirror-images.txt}
    cmd=${1:-}
    [ "$#" -eq 0 ] || shift
    case $cmd in
      ref) mirror_ref "${1:?usage: ref <ref>}" ;;
      rewrite) cmd_rewrite "$@" ;;
      sync) cmd_sync "$@" ;;
      guard) cmd_guard ;;
      *) echo "usage: $0 ref <ref> | rewrite <file>... | sync [list] | guard" >&2; exit 2 ;;
    esac
    ;;
esac
