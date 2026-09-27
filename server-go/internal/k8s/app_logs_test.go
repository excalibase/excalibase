package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestParseAppLogLines_KeepsOnlyWhatIsNewer(t *testing.T) {
	since := time.Date(2026, 9, 27, 10, 0, 0, 500, time.UTC)
	body := "2026-09-27T10:00:00.000000400Z old line\n" +
		"2026-09-27T10:00:00.000000600Z new line\n" +
		"no timestamp at all\n" +
		"2026-09-27T10:00:01Z " + strings.Repeat("x", maxAppLogLineBytes+10) + "\n"
	lines, err := parseAppLogLines("web-1", strings.NewReader(body), &since)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Text != "new line" || lines[0].Pod != "web-1" {
		t.Errorf("first = %+v", lines[0])
	}
	if len(lines[1].Text) != maxAppLogLineBytes {
		t.Errorf("a long line must be cut to %d bytes, got %d", maxAppLogLineBytes, len(lines[1].Text))
	}
}

func TestParseAppLogLines_NoSinceKeepsAll(t *testing.T) {
	lines, _ := parseAppLogLines("p", strings.NewReader("2026-09-27T10:00:00Z a\n2026-09-27T10:00:01Z b\n"), nil)
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
}

func texts(lines []AppLogLine) string {
	got := []string{}
	for _, line := range lines {
		got = append(got, line.Text)
	}
	return strings.Join(got, ",")
}

var logAt = func(s int) time.Time { return time.Date(2026, 9, 27, 10, 0, s, 0, time.UTC) }

func twoPods(limitedA bool) []podLog {
	return []podLog{
		{lines: []AppLogLine{{Pod: "a", Time: logAt(1), Text: "a1"}, {Pod: "a", Time: logAt(4), Text: "a4"}}},
		{lines: []AppLogLine{{Pod: "b", Time: logAt(2), Text: "b2"}, {Pod: "b", Time: logAt(3), Text: "b3"}}, limited: limitedA},
	}
}

func TestPageAppLogLines_ATailReadKeepsTheNewest(t *testing.T) {
	page := pageAppLogLines(twoPods(false), 3, false)
	if texts(page.Lines) != "b2,b3,a4" || page.Truncated {
		t.Fatalf("page = %+v", page)
	}
}

// A cursor read keeps the oldest lines, so the next read from the last one skips nothing.
func TestPageAppLogLines_ACursorReadKeepsTheOldestAndSaysSo(t *testing.T) {
	page := pageAppLogLines(twoPods(false), 3, true)
	if texts(page.Lines) != "a1,b2,b3" || !page.Truncated {
		t.Fatalf("page = %+v", page)
	}
}

func TestPageAppLogLines_StopsWhereAPodsBudgetRanOut(t *testing.T) {
	page := pageAppLogLines(twoPods(true), 100, true)
	if texts(page.Lines) != "a1,b2,b3" || !page.Truncated {
		t.Fatalf("a line after the cut pod's last one would move the cursor past its unread lines: %+v", page)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("stream reset") }

func TestParseAppLogLines_AStreamCutShortIsAnError(t *testing.T) {
	if _, err := parseAppLogLines("p", brokenReader{}, nil); err == nil {
		t.Fatal("a broken stream must not read as the end of the logs")
	}
}

func TestLogPods_NewestLivePodsOnly(t *testing.T) {
	pods := []corev1.Pod{}
	for i := 0; i < maxAppLogPods+2; i++ {
		pod := *appPod("app-1", fmt.Sprintf("p%d", i), i%2 == 0)
		pod.CreationTimestamp = metav1.NewTime(logAt(i))
		pods = append(pods, pod)
	}
	pods[len(pods)-1].Status.Phase = corev1.PodFailed
	got := logPods(pods, false)
	if len(got) != maxAppLogPods || got[0].Name != fmt.Sprintf("p%d", maxAppLogPods) {
		t.Fatalf("pods = %v", podNames(got))
	}
	for _, pod := range logPods(pods, true) {
		if !restarted(pod) {
			t.Fatalf("previous logs read from %s, which never restarted", pod.Name)
		}
	}
}

func podNames(pods []*corev1.Pod) []string {
	names := []string{}
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names
}

func appPod(appID, name string, started bool) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: map[string]string{
			"excalibase.io/app": appID, "app.kubernetes.io/managed-by": appManagedByValue,
		}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name}}},
	}
	if started {
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: name, RestartCount: 1}}
	}
	return pod
}

// logRequests names the containers read; the fake records log options, not the pod, so containers are named after their pods.
func logRequests(clientset *fake.Clientset) []string {
	var pods []string
	for _, action := range clientset.Actions() {
		if action.GetSubresource() == "log" {
			pods = append(pods, action.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions).Container)
		}
	}
	return pods
}

func TestAppLogs_ReadsOnlyTheAppsOwnPods(t *testing.T) {
	stranger := appPod("app-other", "other-1", true)
	stranger.Labels["app.kubernetes.io/managed-by"] = "someone-else"
	clientset := fake.NewSimpleClientset(
		appPod("app-1", "web-1", true),
		appPod("app-2", "api-1", true),
		stranger,
	)
	c := NewClientFromInterfaces(clientset, nil)

	if _, err := c.AppLogs(context.Background(), testNamespace, "app-1", AppLogOptions{TailLines: 10}); err != nil {
		t.Fatalf("AppLogs: %v", err)
	}
	if got := logRequests(clientset); len(got) != 1 || got[0] != "web-1" {
		t.Fatalf("logs read from %v, want web-1 only", got)
	}
}

func TestAppLogs_NoPodsIsEmpty(t *testing.T) {
	c := NewClientFromInterfaces(fake.NewSimpleClientset(), nil)
	page, err := c.AppLogs(context.Background(), testNamespace, "app-1", AppLogOptions{TailLines: 10})
	if err != nil || len(page.Lines) != 0 {
		t.Fatalf("AppLogs = %v, %v", page, err)
	}
}

func TestAppLogs_PreviousOnlyAsksPodsThatRestarted(t *testing.T) {
	clientset := fake.NewSimpleClientset(appPod("app-1", "web-1", true), appPod("app-1", "web-2", false))
	c := NewClientFromInterfaces(clientset, nil)
	if _, err := c.AppLogs(context.Background(), testNamespace, "app-1", AppLogOptions{TailLines: 10, Previous: true}); err != nil {
		t.Fatalf("AppLogs: %v", err)
	}
	if got := logRequests(clientset); len(got) != 1 || got[0] != "web-1" {
		t.Fatalf("previous logs read from %v", got)
	}
}

func TestAppLogs_SinceAndAPodWithNoContainer(t *testing.T) {
	empty := appPod("app-1", "empty-1", true)
	empty.Spec.Containers = nil
	clientset := fake.NewSimpleClientset(appPod("app-1", "web-1", true), empty)
	c := NewClientFromInterfaces(clientset, nil)
	since := time.Date(2026, 9, 27, 10, 0, 0, 700, time.UTC)
	if _, err := c.AppLogs(context.Background(), testNamespace, "app-1", AppLogOptions{TailLines: 10, Since: &since}); err != nil {
		t.Fatalf("AppLogs: %v", err)
	}
	for _, action := range clientset.Actions() {
		if action.GetSubresource() != "log" {
			continue
		}
		opts := action.(k8stesting.GenericAction).GetValue().(*corev1.PodLogOptions)
		if opts.SinceTime == nil || !opts.SinceTime.Time.Equal(since.Truncate(time.Second)) || !opts.Timestamps || opts.TailLines != nil {
			t.Fatalf("options = %+v", opts)
		}
	}
	if got := logRequests(clientset); len(got) != 1 {
		t.Fatalf("read %v", got)
	}
}
