package k8s

import (
	"io"
	"time"
)

// The single-host runtime (EXC-575) applies the same rendered workload as
// containers instead of objects; these name what it reads from it.
const (
	AppDeployAnnotation = appDeployAnnotation
	AppLabelApp         = "excalibase.io/app"
	AppLabelProject     = "excalibase.io/project"
	AppLabelName        = "app.kubernetes.io/name"
	AppLabelTier        = "excalibase.io/tier"
	AppHTTPPortName     = appServicePortName
	// MaxAppLogBytes and MaxAppLogSources bound one log read, shared across its sources.
	MaxAppLogBytes   = maxAppLogBytes
	MaxAppLogSources = maxAppLogPods
)

// AppLogRead is one source's lines and whether its byte budget cut it short.
type AppLogRead struct {
	Lines   []AppLogLine
	Limited bool
}

// ParseAppLogLines reads "<RFC3339Nano> <text>" lines, as the kubelet and the
// Docker and Podman logs APIs write them with timestamps on.
func ParseAppLogLines(source string, body io.Reader, since *time.Time) ([]AppLogLine, error) {
	return parseAppLogLines(source, body, since)
}

// PageAppLogLines merges reads by time the way AppLogs does.
func PageAppLogLines(reads []AppLogRead, limit int, fromCursor bool) AppLogPage {
	perSource := make([]podLog, 0, len(reads))
	for _, read := range reads {
		perSource = append(perSource, podLog{lines: read.Lines, limited: read.Limited})
	}
	return pageAppLogLines(perSource, limit, fromCursor)
}
