package provisioner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// A single host's Postgres moved to a newer major (EXC-668): the old server
// is fenced and dumped, and a new container of the new image loads the dump
// from its first start, before its server is on the network.

// ContainerRenamer renames a container; the upgrade frees the project's
// container name for the new one while the old one is kept.
type ContainerRenamer interface {
	RenameContainer(ctx context.Context, id, name string) error
}

// RenameContainer gives the container another name.
func (r *RealDockerClient) RenameContainer(ctx context.Context, id, name string) error {
	if err := r.c.ContainerRename(ctx, id, name); err != nil {
		return fmt.Errorf("rename container %s: %w", id, err)
	}
	return nil
}

// UpgradeDir holds the dump and the load's result in both containers.
const UpgradeDir = "/tmp/excalibase-upgrade"

// The dump: the roles, then one archive per database. UpgradeDatabasesFile
// lists "oid<TAB>create<TAB>name" per database; create is 1 for one the load
// creates, 0 for postgres and template1, which every server already has.
const (
	UpgradeGlobalsFile   = UpgradeDir + "/globals.sql"
	UpgradeDatabasesFile = UpgradeDir + "/databases"
)

// UpgradeStateFile, in the new container's data directory, says how the load
// ended: "loaded" or "failed". The container restarts by itself after a
// failed first start and then serves what it has; the state file, not the
// server answering, is what tells success.
const UpgradeStateFile = "excalibase-upgrade.state"

const (
	UpgradeLoaded = "loaded"
	UpgradeFailed = "failed"
)

// upgradeFenceLine refuses every network connection while the data moves; the
// platform reaches the server over its socket.
const upgradeFenceLine = "host all all all reject # excalibase-major-upgrade"

// PreUpgradeContainerName is what the old container is renamed to while the
// new one takes its name.
func PreUpgradeContainerName(projectID, runID string) string {
	return DatabaseContainerName(projectID) + "-pre-upgrade-" + runID
}

// hbaPrepend puts line first in the server's pg_hba once. The file is
// rewritten in place, so it keeps its owner.
func hbaPrepend(hbaExpr, line string) string {
	return `hba=` + hbaExpr + `
line='` + line + `'
if ! grep -qxF "$line" "$hba"; then
  { echo "$line"; cat "$hba"; } > "$hba.excalibase"
  cat "$hba.excalibase" > "$hba"
  rm -f "$hba.excalibase"
fi
`
}

const showHBA = `$(psql -X -U ` + defaultPostgresSuperuser + ` -d postgres -tAc 'SHOW hba_file')`

// FenceScript refuses network connections to the running server and ends
// the ones it has, so nothing is written after the dump starts.
func FenceScript() string {
	return "set -eu\n" + hbaPrepend(showHBA, upgradeFenceLine) +
		`psql -X -U ` + defaultPostgresSuperuser + ` -d postgres -tAc 'SELECT pg_reload_conf()' >/dev/null
psql -X -U ` + defaultPostgresSuperuser + ` -d postgres -tAc "SELECT count(pg_catalog.pg_terminate_backend(pid)) FROM pg_catalog.pg_stat_activity WHERE pid <> pg_catalog.pg_backend_pid() AND backend_type = 'client backend' AND client_addr IS NOT NULL" >/dev/null
`
}

// UnfenceScript lets network connections in again.
func UnfenceScript() string {
	return `set -eu
hba=` + showHBA + `
grep -vxF '` + upgradeFenceLine + `' "$hba" > "$hba.excalibase" || true
cat "$hba.excalibase" > "$hba"
rm -f "$hba.excalibase"
psql -X -U ` + defaultPostgresSuperuser + ` -d postgres -tAc 'SELECT pg_reload_conf()' >/dev/null
`
}

// DumpScript writes the whole server to UpgradeDir, readable by the server's
// user only: the new container's first start loads it as that user.
func DumpScript() string {
	return `set -eu
umask 077
rm -rf ` + UpgradeDir + `
mkdir -p ` + UpgradeDir + `
export PGOPTIONS='-c statement_timeout=0 -c search_path=pg_catalog,pg_temp'
tab=$(printf '\t')
pg_dumpall -U ` + defaultPostgresSuperuser + ` --quote-all-identifiers --globals-only -f ` + UpgradeGlobalsFile + `
psql -X -A -t -q -v ON_ERROR_STOP=1 -U ` + defaultPostgresSuperuser + ` -d postgres -F "$tab" \
  -c "SELECT oid, CASE WHEN datname OPERATOR(pg_catalog.=) ANY (ARRAY['postgres', 'template1']::pg_catalog.name[]) THEN 0 ELSE 1 END, datname FROM pg_catalog.pg_database WHERE datallowconn AND datname OPERATOR(pg_catalog.<>) 'template0' ORDER BY oid" > ` + UpgradeDatabasesFile + `
while IFS="$tab" read -r oid create name; do
  pg_dump -U ` + defaultPostgresSuperuser + ` -Fc --create --quote-all-identifiers -f "` + UpgradeDir + `/db-$oid.dump" -d "$name"
done < ` + UpgradeDatabasesFile + `
chown -R postgres:postgres ` + UpgradeDir + `
`
}

// CleanupScript removes the dump and the load's state.
func CleanupScript() string {
	return `rm -rf ` + UpgradeDir + `; [ -n "${PGDATA:-}" ] && rm -f "$PGDATA/` + UpgradeStateFile + `" "$PGDATA/` + UpgradeStateFile + `.reason"; true
`
}

// LoadErrorsScript prints why the load failed and its first errors.
func LoadErrorsScript() string {
	return `cat "$PGDATA/` + UpgradeStateFile + `.reason" 2>/dev/null
grep -v 'role "` + defaultPostgresSuperuser + `" already exists' ` + UpgradeDir + `/load.log 2>/dev/null | grep -A2 'ERROR:\|FATAL:\|error:' | head -n 9; true
`
}

// UpgradeStateScript prints the load's state, "" before it ended.
func UpgradeStateScript() string {
	return `cat "$PGDATA/` + UpgradeStateFile + `" 2>/dev/null || true
`
}

// upgradeInitScript runs once, from the official entrypoint, against its
// socket-only first server: fence and keep the superuser off the network
// first, then load the roles (the image already made the postgres role, the
// one error allowed) and each database, then archive WAL. Any other error
// leaves "failed". Rows load as their table's owner, so a tenant's check
// constraint that runs on them never runs as the superuser; the schema and
// what comes after the rows are not tenant code that runs (index builds run
// as the table owner since CVE-2022-1552).
func upgradeInitScript() string {
	return `#!/bin/sh
set -u
state="$PGDATA/` + UpgradeStateFile + `"
fail() { echo "excalibase upgrade: $1" > "$state.reason"; echo "` + UpgradeFailed + `" > "$state"; echo "excalibase upgrade: $1" >&2; exit 1; }
# Whatever ends this script early, the platform must read "failed", not wait.
trap '[ -s "$state" ] || echo "` + UpgradeFailed + `" > "$state"' EXIT
` + hbaPrepend(`"$PGDATA/pg_hba.conf"`, superuserNetworkReject) + hbaPrepend(`"$PGDATA/pg_hba.conf"`, upgradeFenceLine) + `
log=` + UpgradeDir + `/load.log
: > "$log"
tab=$(printf '\t')
load() { psql -X -q -v ON_ERROR_STOP=0 -U ` + defaultPostgresSuperuser + ` "$@" >/dev/null 2>>"$log"; }
restore() { pg_restore -U ` + defaultPostgresSuperuser + ` --exit-on-error "$@" 2>>"$log"; }
[ -r ` + UpgradeGlobalsFile + ` ] && [ -r ` + UpgradeDatabasesFile + ` ] || fail "the dump is incomplete"
load -d postgres -f ` + UpgradeGlobalsFile + ` || fail "the roles did not load"
while IFS="$tab" read -r oid create name; do
  dump="` + UpgradeDir + `/db-$oid.dump"
  if [ "$create" = 1 ]; then
    restore --section=pre-data --create -d postgres "$dump" || fail "a database's schema did not load"
  else
    restore --section=pre-data -d "$name" "$dump" || fail "a database's schema did not load"
  fi
  restore --section=data --use-set-session-authorization -d "$name" "$dump" || fail "a database's rows did not load"
  restore --section=post-data -d "$name" "$dump" || fail "a database's indexes and constraints did not load"
done < ` + UpgradeDatabasesFile + `
if grep 'ERROR:' "$log" | grep -vqF 'role "` + defaultPostgresSuperuser + `" already exists'; then
  fail "the dump did not load: $(grep -c 'ERROR:' "$log") error(s)"
fi
psql -X -q -v ON_ERROR_STOP=1 -U ` + defaultPostgresSuperuser + ` -d postgres \
  -c "ALTER SYSTEM SET wal_level = 'logical'" \
  -c "ALTER SYSTEM SET archive_mode = 'on'" \
  -c "ALTER SYSTEM SET archive_command = ` + strings.ReplaceAll(quoteSQLString(walArchiveCommand), `"`, `\"`) + `" \
  -c "ALTER SYSTEM SET archive_timeout = ` + quoteSQLString(walArchiveTimeout) + `" || fail "WAL archiving could not be set"
echo "` + UpgradeLoaded + `" > "$state"
`
}

// UpgradeInitTar is the tar CopyToContainer extracts into the new
// container's /docker-entrypoint-initdb.d before its first start.
func UpgradeInitTar() ([]byte, error) {
	return singleFileTar("excalibase-major-upgrade.sh", []byte(upgradeInitScript()), 0o755, FileOwner{})
}

// UpgradeInitDir is where the official image runs first-start scripts.
const UpgradeInitDir = "/docker-entrypoint-initdb.d"

// WALArchiveDirTar is the archive directory, owned by the server.
func WALArchiveDirTar(owner FileOwner) ([]byte, error) { return archiveDirTar(owner) }

func singleFileTar(name string, body []byte, mode int64, owner FileOwner) ([]byte, error) {
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	header := &tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Uid: owner.UID, Gid: owner.GID, ModTime: time.Now(), Typeflag: tar.TypeReg}
	if err := writer.WriteHeader(header); err != nil {
		return nil, err
	}
	if _, err := writer.Write(body); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExtensionDir is where the official image keeps a major's extensions.
func ExtensionDir(major string) string { return "/usr/share/postgresql/" + major + "/extension" }

// ErrNoExtensions is an extension directory with no extension in it.
var ErrNoExtensions = errors.New("the image lists no extensions")

// ExtensionNames reads the extensions a tar of ExtensionDir offers: one per
// .control file.
func ExtensionNames(archive io.Reader) ([]string, error) {
	reader := tar.NewReader(archive)
	seen := map[string]bool{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read extension list: %w", err)
		}
		base := path.Base(header.Name)
		if header.Typeflag == tar.TypeReg && strings.HasSuffix(base, ".control") {
			seen[strings.TrimSuffix(base, ".control")] = true
		}
	}
	if len(seen) == 0 {
		return nil, ErrNoExtensions
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
