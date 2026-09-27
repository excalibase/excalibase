package k8s

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	maxAppLogLineBytes = 16 * 1024
	// maxAppLogBytes bounds what one request reads from the API server, shared across its pods.
	maxAppLogBytes = 2 << 20
	// maxAppLogPods bounds how many pods one request reads: a rollout's surge beside the largest replica count.
	maxAppLogPods     = 4
	appLogReadTimeout = 15 * time.Second
)

type AppLogLine struct {
	Pod  string    `json:"pod"`
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// AppLogPage is what one read returns. Truncated means more lines followed the
// last one returned; the caller asks again from that line.
type AppLogPage struct {
	Lines     []AppLogLine
	Truncated bool
}

type AppLogOptions struct {
	// Since keeps only lines strictly newer, so a poller never sees a line twice.
	Since     *time.Time
	TailLines int64
	// Previous reads the container that ran before the last restart.
	Previous bool
}

// appPodSelector matches the pods the platform rendered for one app and nothing else.
func appPodSelector(appID string) string {
	return "excalibase.io/app=" + appID + ",app.kubernetes.io/managed-by=" + appManagedByValue
}

// AppLogs reads the log lines of the app's current pods, oldest first. With
// Since it returns the oldest lines after the cursor, so a poller never skips
// any; without it, the last TailLines.
func (c *Client) AppLogs(ctx context.Context, namespace, appID string, opts AppLogOptions) (AppLogPage, error) {
	ctx, cancel := context.WithTimeout(ctx, appLogReadTimeout)
	defer cancel()
	pods, err := c.clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: appPodSelector(appID)})
	if err != nil {
		return AppLogPage{}, fmt.Errorf("list app pods: %w", err)
	}
	readable := logPods(pods.Items, opts.Previous)
	perPod := make([]podLog, 0, len(readable))
	for _, pod := range readable {
		read, err := c.podLogLines(ctx, namespace, pod, opts, int64(maxAppLogBytes/len(readable)))
		if err != nil {
			return AppLogPage{}, err
		}
		perPod = append(perPod, read)
	}
	return pageAppLogLines(perPod, int(opts.TailLines), opts.Since != nil), nil
}

// podLog is one pod's lines and whether its byte budget cut the read short.
type podLog struct {
	lines   []AppLogLine
	limited bool
}

// logPods keeps the newest pods that still have a container to read: a
// finished or evicted pod only has one for previous=false if it is still running.
func logPods(pods []corev1.Pod, previous bool) []*corev1.Pod {
	sort.SliceStable(pods, func(i, j int) bool {
		return pods[j].CreationTimestamp.Before(&pods[i].CreationTimestamp)
	})
	out := make([]*corev1.Pod, 0, maxAppLogPods)
	for i := range pods {
		pod := &pods[i]
		finished := pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
		if len(pod.Spec.Containers) == 0 || (previous && !restarted(pod)) || (!previous && finished) {
			continue
		}
		out = append(out, pod)
		if len(out) == maxAppLogPods {
			break
		}
	}
	return out
}

func restarted(pod *corev1.Pod) bool {
	for _, status := range pod.Status.ContainerStatuses {
		if status.RestartCount > 0 {
			return true
		}
	}
	return false
}

// podLogLines treats a container that has not started yet as having no lines.
// A cursor read asks from the cursor with no tail, so nothing between is skipped.
func (c *Client) podLogLines(ctx context.Context, namespace string, pod *corev1.Pod, opts AppLogOptions, limit int64) (podLog, error) {
	logOpts := &corev1.PodLogOptions{
		Container: pod.Spec.Containers[0].Name, Timestamps: true, Previous: opts.Previous, LimitBytes: &limit,
	}
	if opts.Since != nil {
		since := metav1.NewTime(opts.Since.Truncate(time.Second))
		logOpts.SinceTime = &since
	} else {
		tail := opts.TailLines
		logOpts.TailLines = &tail
	}
	stream, err := c.clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, logOpts).Stream(ctx)
	if apierrors.IsBadRequest(err) || apierrors.IsNotFound(err) {
		return podLog{}, nil
	}
	if err != nil {
		return podLog{}, fmt.Errorf("read logs of %s: %w", pod.Name, err)
	}
	defer stream.Close()
	counted := &countingReader{r: stream}
	lines, err := parseAppLogLines(pod.Name, counted, opts.Since)
	if err != nil {
		return podLog{}, fmt.Errorf("read logs of %s: %w", pod.Name, err)
	}
	return podLog{lines: lines, limited: counted.n >= limit}, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// parseAppLogLines drops a line without a timestamp: with timestamps on, only
// a line cut by the byte limit lacks one.
func parseAppLogLines(pod string, body io.Reader, since *time.Time) ([]AppLogLine, error) {
	var lines []AppLogLine
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), maxAppLogBytes)
	for scanner.Scan() {
		stamp, text, found := strings.Cut(scanner.Text(), " ")
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if !found || err != nil || (since != nil && !at.After(*since)) {
			continue
		}
		if len(text) > maxAppLogLineBytes {
			text = text[:maxAppLogLineBytes]
		}
		lines = append(lines, AppLogLine{Pod: pod, Time: at, Text: text})
	}
	return lines, scanner.Err()
}

// pageAppLogLines merges by time. A cursor read keeps the oldest lines, and
// stops at the last line of any pod its byte budget cut short, so the next
// read from the cursor misses none of that pod's lines; a tail read keeps the newest.
func pageAppLogLines(perPod []podLog, limit int, fromCursor bool) AppLogPage {
	merged := make([]AppLogLine, 0)
	var cutoff *time.Time
	for _, read := range perPod {
		merged = append(merged, read.lines...)
		if read.limited && len(read.lines) > 0 {
			last := read.lines[len(read.lines)-1].Time
			if cutoff == nil || last.Before(*cutoff) {
				cutoff = &last
			}
		}
	}
	sort.SliceStable(merged, func(i, j int) bool { return merged[i].Time.Before(merged[j].Time) })
	truncated := false
	if fromCursor && cutoff != nil {
		kept := sort.Search(len(merged), func(i int) bool { return merged[i].Time.After(*cutoff) })
		truncated = kept < len(merged)
		merged = merged[:kept]
	}
	if limit <= 0 || len(merged) <= limit {
		return AppLogPage{Lines: merged, Truncated: truncated}
	}
	if fromCursor {
		return AppLogPage{Lines: merged[:limit], Truncated: true}
	}
	return AppLogPage{Lines: merged[len(merged)-limit:]}
}
