// Package promexport writes one project's stored metrics in the Prometheus
// text exposition format, for a customer's Grafana or Datadog to scrape (EXC-657).
package promexport

import (
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/requestmetrics"
)

// ContentType is the text exposition format version Encode writes.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

const (
	// databaseFreshFor is how old the newest database sample may be before it
	// no longer describes the database; the sampler runs once a minute.
	databaseFreshFor = 5 * time.Minute
	// realtimeFreshFor is how old a realtime reading may be; a minute with
	// nothing open writes no row, so one missed sample means none are open.
	realtimeFreshFor = 90 * time.Second
	bytesPerMB       = 1024 * 1024
)

const (
	gauge     = "gauge"
	counter   = "counter"
	histogram = "histogram"
)

// Snapshot is what one project's endpoint answers at Now.
type Snapshot struct {
	ProjectID string
	Now       time.Time
	// Database is the newest database_metrics sample, nil when there is none.
	Database *domain.DatabaseMetrics
	// Requests are the project's running totals per family.
	Requests []domain.RequestTotalsRow
}

type label struct{ name, value string }

type sample struct {
	suffix string
	labels []label
	value  float64
}

type family struct {
	name, help, kind string
	samples          []sample
}

func (f *family) add(suffix string, value float64, labels ...label) {
	f.samples = append(f.samples, sample{suffix: suffix, labels: labels, value: value})
}

// Encode writes the snapshot; a family with nothing to report is left out.
func Encode(w io.Writer, snapshot Snapshot) error {
	project := label{"project", snapshot.ProjectID}
	families := databaseFamilies(snapshot, project)
	families = append(families, requestFamilies(snapshot, project)...)
	var out strings.Builder
	for _, f := range families {
		writeFamily(&out, f)
	}
	_, err := io.WriteString(w, out.String())
	return err
}

func databaseFamilies(snapshot Snapshot, project label) []*family {
	database := snapshot.Database
	if database == nil || database.Timestamp == nil || snapshot.Now.Sub(database.Timestamp.Time) > databaseFreshFor {
		return nil
	}
	available := &family{name: "excalibase_database_metrics_available", kind: gauge,
		help: "1 when the newest database sample was measured, 0 when it could not be."}
	if !database.MetricsAvailable {
		available.add("", 0, project)
		return []*family{available}
	}
	available.add("", 1, project)
	families := []*family{available}
	gauges := []struct {
		name, help string
		value      *float64
	}{
		{"excalibase_database_cpu_usage_cores", "CPU the database uses, in cores.", database.CPUUsageCores},
		{"excalibase_database_cpu_limit_cores", "CPU the database may use, in cores.", database.CPULimitCores},
		{"excalibase_database_cpu_usage_ratio", "CPU used over the limit, 0 to 1.", ratio(database.CPUUsagePercent)},
		{"excalibase_database_memory_usage_bytes", "Memory the database uses, in bytes.", megabytes(database.MemoryUsageMB)},
		{"excalibase_database_memory_limit_bytes", "Memory the database may use, in bytes.", megabytes(database.MemoryLimitMB)},
		{"excalibase_database_memory_usage_ratio", "Memory used over the limit, 0 to 1.", ratio(database.MemoryUsagePercent)},
		{"excalibase_database_disk_usage_ratio", "Disk used over the volume size, 0 to 1.", ratio(database.DiskUsagePercent)},
		{"excalibase_database_max_connections", "Connections the database accepts.", count(database.MaxConnections)},
	}
	for _, g := range gauges {
		if g.value != nil {
			f := &family{name: g.name, help: g.help, kind: gauge}
			f.add("", *g.value, project)
			families = append(families, f)
		}
	}
	if connections := connectionFamily(database, project); connections != nil {
		families = append(families, connections)
	}
	sampled := &family{name: "excalibase_database_sample_timestamp_seconds", kind: gauge,
		help: "Unix time of the database sample these gauges come from."}
	sampled.add("", float64(database.Timestamp.Unix()), project)
	return append(families, sampled)
}

func connectionFamily(database *domain.DatabaseMetrics, project label) *family {
	f := &family{name: "excalibase_database_connections", kind: gauge, help: "Open database connections by state."}
	if active := count(database.ActiveConnections); active != nil {
		f.add("", *active, project, label{"state", "active"})
	}
	if idle := count(database.IdleConnections); idle != nil {
		f.add("", *idle, project, label{"state", "idle"})
	}
	if len(f.samples) == 0 {
		return nil
	}
	return f
}

func requestFamilies(snapshot Snapshot, project label) []*family {
	requests := &family{name: "excalibase_requests_total", kind: counter,
		help: "Requests served, by surface and status class, since the endpoint was turned on."}
	latency := &family{name: "excalibase_request_duration_seconds", kind: histogram,
		help: "Request latency in seconds. No _sum: total latency is not recorded."}
	connections := &family{name: "excalibase_realtime_connections", kind: gauge, help: "Open realtime connections."}
	opened := &family{name: "excalibase_realtime_connections_opened_total", kind: counter, help: "Realtime connections opened."}
	messages := &family{name: "excalibase_realtime_messages_total", kind: counter, help: "Realtime messages delivered."}
	for _, row := range ownRows(snapshot) {
		if row.Family == requestmetrics.FamilyRealtime {
			open := row.Connections
			if snapshot.Now.Sub(row.UpdatedAt) > realtimeFreshFor {
				open = 0
			}
			connections.add("", float64(open), project)
			opened.add("", float64(row.ConnectionsOpened), project)
			messages.add("", float64(row.Messages), project)
			continue
		}
		surface := label{"surface", row.Family}
		ok := max(row.Requests-row.ClientErrors-row.ServerErrors, 0)
		requests.add("", float64(ok), project, surface, label{"status", "ok"})
		requests.add("", float64(row.ClientErrors), project, surface, label{"status", "client_error"})
		requests.add("", float64(row.ServerErrors), project, surface, label{"status", "server_error"})
		addHistogram(latency, row.LatencyBuckets, project, surface)
	}
	return []*family{requests, latency, connections, opened, messages}
}

// ownRows are the snapshot project's rows, in the order Studio charts them.
func ownRows(snapshot Snapshot) []domain.RequestTotalsRow {
	rows := make([]domain.RequestTotalsRow, 0, len(snapshot.Requests))
	for _, row := range snapshot.Requests {
		if row.ProjectID == snapshot.ProjectID {
			rows = append(rows, row)
		}
	}
	slices.SortFunc(rows, func(a, b domain.RequestTotalsRow) int {
		return slices.Index(requestmetrics.Families, a.Family) - slices.Index(requestmetrics.Families, b.Family)
	})
	return rows
}

// addHistogram turns per-bucket totals into cumulative le buckets; a layout
// other than requestmetrics.Bounds is left out rather than guessed at.
func addHistogram(f *family, buckets []int64, project, surface label) {
	if len(buckets) != requestmetrics.BucketCount {
		return
	}
	var cumulative int64
	for i, bucketCount := range buckets {
		cumulative += bucketCount
		le := "+Inf"
		if i < len(requestmetrics.Bounds) {
			le = formatFloat(requestmetrics.Bounds[i])
		}
		f.add("_bucket", float64(cumulative), project, surface, label{"le", le})
	}
	f.add("_count", float64(cumulative), project, surface)
}

func writeFamily(out *strings.Builder, f *family) {
	if len(f.samples) == 0 {
		return
	}
	out.WriteString("# HELP " + f.name + " " + f.help + "\n")
	out.WriteString("# TYPE " + f.name + " " + f.kind + "\n")
	for _, s := range f.samples {
		out.WriteString(f.name + s.suffix)
		writeLabels(out, s.labels)
		out.WriteString(" " + formatFloat(s.value) + "\n")
	}
}

func writeLabels(out *strings.Builder, labels []label) {
	out.WriteByte('{')
	for i, l := range labels {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(l.name + `="` + escapeLabelValue(l.value) + `"`)
	}
	out.WriteByte('}')
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabelValue(value string) string { return labelEscaper.Replace(value) }

func formatFloat(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }

func ratio(percent *float64) *float64 {
	if percent == nil {
		return nil
	}
	value := *percent / 100
	return &value
}

func megabytes(mb *int64) *float64 {
	if mb == nil {
		return nil
	}
	value := float64(*mb) * bytesPerMB
	return &value
}

func count(n *int) *float64 {
	if n == nil {
		return nil
	}
	value := float64(*n)
	return &value
}
