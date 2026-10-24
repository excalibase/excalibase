package requestmetrics

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	bucketMetric      = "excalibase_requests_seconds_bucket"
	connectionsMetric = "excalibase_realtime_connections"
	openedMetric      = "excalibase_realtime_connections_opened_total"
	messagesMetric    = "excalibase_realtime_messages_total"
	infoMetric        = "excalibase_request_metrics_info"
)

var outcomes = map[string]bool{"ok": true, "client_error": true, "server_error": true}

// SeriesKey is one histogram of a process.
type SeriesKey struct {
	Project string
	Family  string
	Outcome string
}

// Realtime is a project's WebSocket readings in one process.
type Realtime struct {
	Connections float64
	Opened      float64
	Messages    float64
}

// Reading is one scrape of one process: cumulative histograms (one count per
// bound, then +Inf) and realtime counters, as they stood.
type Reading struct {
	Instance   string
	Histograms map[SeriesKey][]float64
	Realtime   map[string]Realtime
}

// Parse reads the Prometheus text format, keeping only the series the charts use.
func Parse(text string) (Reading, error) {
	reading := Reading{Histograms: map[SeriesKey][]float64{}, Realtime: map[string]Realtime{}}
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, err := parseLine(line)
		if err != nil {
			continue
		}
		reading.apply(name, labels, value)
	}
	if err := scanner.Err(); err != nil {
		return Reading{}, fmt.Errorf("read request metrics: %w", err)
	}
	if reading.Instance == "" {
		return Reading{}, errors.New("request metrics carry no instance id")
	}
	return reading, nil
}

func (r *Reading) apply(name string, labels map[string]string, value float64) {
	project := labels["project"]
	switch name {
	case infoMetric:
		r.Instance = labels["instance"]
	case bucketMetric:
		r.applyBucket(labels, value)
	case connectionsMetric, openedMetric, messagesMetric:
		if project == "" {
			return
		}
		current := r.Realtime[project]
		switch name {
		case connectionsMetric:
			current.Connections = value
		case openedMetric:
			current.Opened = value
		default:
			current.Messages = value
		}
		r.Realtime[project] = current
	}
}

func (r *Reading) applyBucket(labels map[string]string, value float64) {
	key := SeriesKey{Project: labels["project"], Family: labels["family"], Outcome: labels["outcome"]}
	if key.Project == "" || !requestFamilies[key.Family] || !outcomes[key.Outcome] {
		return
	}
	index, ok := bucketIndex(labels["le"])
	if !ok {
		return
	}
	buckets, seen := r.Histograms[key]
	if !seen {
		buckets = make([]float64, BucketCount)
		r.Histograms[key] = buckets
	}
	buckets[index] = value
}

func bucketIndex(le string) (int, bool) {
	if le == "+Inf" {
		return BucketCount - 1, true
	}
	bound, err := strconv.ParseFloat(le, 64)
	if err != nil {
		return 0, false
	}
	for i, known := range Bounds {
		if math.Abs(bound-known) <= known*1e-9 {
			return i, true
		}
	}
	return 0, false
}

// parseLine splits `name{a="b",...} value [timestamp]`.
func parseLine(line string) (string, map[string]string, float64, error) {
	labels := map[string]string{}
	name, rest := line, ""
	if open := strings.IndexByte(line, '{'); open >= 0 {
		name = line[:open]
		var err error
		rest, err = parseLabels(line[open+1:], labels)
		if err != nil {
			return "", nil, 0, err
		}
	} else if space := strings.IndexByte(line, ' '); space >= 0 {
		name, rest = line[:space], line[space:]
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "", nil, 0, errors.New("no value")
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", nil, 0, err
	}
	return name, labels, value, nil
}

// parseLabels fills labels from `a="b",c="d"} rest` and returns rest.
func parseLabels(text string, labels map[string]string) (string, error) {
	for {
		text = strings.TrimLeft(text, " ,")
		if strings.HasPrefix(text, "}") {
			return text[1:], nil
		}
		equals := strings.IndexByte(text, '=')
		if equals < 0 || len(text) < equals+2 || text[equals+1] != '"' {
			return "", errors.New("malformed labels")
		}
		name := strings.TrimSpace(text[:equals])
		value, remaining, err := quoted(text[equals+2:])
		if err != nil {
			return "", err
		}
		labels[name] = value
		text = remaining
	}
}

func quoted(text string) (string, string, error) {
	var value strings.Builder
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\\':
			if i+1 >= len(text) {
				return "", "", errors.New("dangling escape")
			}
			i++
			if text[i] == 'n' {
				value.WriteByte('\n')
			} else {
				value.WriteByte(text[i])
			}
		case '"':
			return value.String(), text[i+1:], nil
		default:
			value.WriteByte(text[i])
		}
	}
	return "", "", errors.New("unterminated label value")
}
