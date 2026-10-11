package provisioner

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A MySQL project on a single Docker or Podman host (EXC-592) runs the
// catalogue's MOCO image, the one Kubernetes runs: it carries mysqldump and
// mysqlbinlog, which backups and point-in-time restores need, and the
// official image lacks the latter (EXC-591). The image has no init of its
// own, so the container's command initialises an empty datadir and runs
// mysqld with the platform's settings on its command line.

const (
	// MySQLDataDir is the image's datadir, owned by its user, so the
	// anonymous volume there starts writable.
	MySQLDataDir = "/var/lib/mysql"
	// MySQLSocket is where mysqld listens for the platform's own sessions:
	// root has no password and is reachable only through this socket,
	// inside the container, as Postgres's local socket is trusted.
	MySQLSocket = "/tmp/mysqld.sock"
	// mysqlImageUID is the uid and gid the catalogue image runs as.
	mysqlImageUID = 10000
	// mysqlBinlogExpiry keeps binlogs far longer than the 6-hour interval
	// at which backups ship them.
	mysqlBinlogExpiry = "604800"
	// mysqlMaxPacket replays a large transaction from its binlog; at the
	// 64MiB default a one-million-row insert failed to replay (EXC-591).
	mysqlMaxPacket = "1073741824"
	// mysqlBufferPoolPercent is MOCO's share of memory for the buffer pool.
	mysqlBufferPoolPercent = 70
)

// MySQLImageOwner owns files the platform writes into the container.
var MySQLImageOwner = FileOwner{UID: mysqlImageUID, GID: mysqlImageUID}

// MySQLContainerName is a project's MySQL container. It is also the
// project's namespace on a single host: a name, unlike an id, survives the
// container being replaced by an in-place restore.
func MySQLContainerName(projectID string) string {
	return fmt.Sprintf("excalibase-%s-mysql", projectID)
}

// MySQLContainerSpec is the container of a MySQL project, new or restored.
// MySQLVolumeName is the project's data volume, under the engine proxy's
// volume prefix; it outlives the container so a new image can open it.
func MySQLVolumeName(projectID string) string {
	return "excalibase-vol-" + projectID + "-mysql"
}

func MySQLContainerSpec(projectID, image string, plan config.TierConfig) (ContainerSpec, error) {
	plan, err := config.MySQLPlan(plan)
	if err != nil {
		return ContainerSpec{}, err
	}
	limits, err := LimitsForTier(plan)
	if err != nil {
		return ContainerSpec{}, err
	}
	args, err := mysqlServerArgs(plan)
	if err != nil {
		return ContainerSpec{}, err
	}
	return ContainerSpec{
		Name:           MySQLContainerName(projectID),
		Image:          image,
		Entrypoint:     []string{"sh", "-c"},
		Cmd:            []string{mysqlStartScript(args)},
		Ports:          map[string]string{strconv.Itoa(mysqlPort): ""},
		Limits:         limits,
		DataVolume:     MySQLDataDir,
		DataVolumeName: MySQLVolumeName(projectID),
	}, nil
}

// mysqlStartScript initialises an empty datadir, root with no password on the
// socket alone, and runs mysqld. mysqld writes its TLS certificate into the
// datadir on first start.
func mysqlStartScript(args []string) string {
	return `set -eu
if [ ! -d ` + MySQLDataDir + `/mysql ]; then
  mysqld --initialize-insecure --datadir=` + MySQLDataDir + `
fi
exec mysqld ` + strings.Join(args, " ")
}

// mysqlServerArgs are MOCO's settings that matter here and the plan's,
// sorted so the command line is stable.
func mysqlServerArgs(plan config.TierConfig) ([]string, error) {
	settings, err := k8s.MySQLSettings(plan)
	if err != nil {
		return nil, err
	}
	if _, set := settings["innodb_buffer_pool_size"]; !set {
		memory, err := resource.ParseQuantity(plan.Memory)
		if err != nil {
			return nil, fmt.Errorf("plan memory %q: %w", plan.Memory, err)
		}
		settings["innodb_buffer_pool_size"] = strconv.FormatInt(memory.Value()*mysqlBufferPoolPercent/100, 10)
	}
	for key, value := range map[string]string{
		"datadir":                    MySQLDataDir,
		"socket":                     MySQLSocket,
		"skip_name_resolve":          "ON",
		"mysqlx":                     "OFF",
		"require_secure_transport":   "ON",
		"gtid_mode":                  "ON",
		"enforce_gtid_consistency":   "ON",
		"log_bin":                    "binlog",
		"binlog_format":              "ROW",
		"binlog_row_image":           "FULL",
		"binlog_expire_logs_seconds": mysqlBinlogExpiry,
		"max_allowed_packet":         mysqlMaxPacket,
		"character_set_server":       "utf8mb4",
		"collation_server":           "utf8mb4_unicode_ci",
		"default_time_zone":          "+00:00",
		"transaction_isolation":      "READ-COMMITTED",
		"disabled_storage_engines":   "MyISAM",
		"secure_file_priv":           "NULL",
		"innodb_numa_interleave":     "OFF",
	} {
		settings[key] = value
	}
	args := make([]string, 0, len(settings))
	for key, value := range settings {
		args = append(args, "--"+strings.ReplaceAll(key, "_", "-")+"="+value)
	}
	sort.Strings(args)
	return args, nil
}
