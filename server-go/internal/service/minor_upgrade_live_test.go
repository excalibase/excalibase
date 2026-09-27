//go:build live

package service

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// An older 17 minor than the catalogue's, so re-pinning to the catalogue is a minor upgrade.
const olderMinorImage = "ghcr.io/cloudnative-pg/postgresql:17.10-standard-bookworm@sha256:f94c0eea2a7e9d40880adebd27a912d8473d5805a99c4dbc6eb6244ad9770620"

// Run with: EXCALIBASE_LIVE_KUBECONFIG=<k3d with 3 nodes> go test ./internal/service/ -tags=live -run TestLiveMinorUpgrade -v -count=1 -timeout 60m
func TestLiveMinorUpgradeOfAStandardProjectSwitchesOver(t *testing.T) {
	lab := &backupLab{documentDBLab: startExternalLab(t, multiNodeKubeconfigEnv)}
	lab.freshNamespaces(t, "backup-store")
	lab.installBackupStack(t)
	svc := liveService(t, lab.client, lab.store)
	tier, err := svc.TierConfig(lab.ctx, domain.Standard)
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	outage := lab.measureMinorUpgrade(t, svc, "haupg", tier)
	if outage.failed > 0 && outage.longest > 15*time.Second {
		t.Errorf("a switchover kept clients out for %s", outage.longest)
	}
}

// Run with: EXCALIBASE_LIVE_KUBECONFIG=<k3d> go test ./internal/service/ -tags=live -run TestLiveMinorUpgradeOfAFreeProject -v -count=1 -timeout 60m
func TestLiveMinorUpgradeOfAFreeProjectRestartsInPlace(t *testing.T) {
	lab := &backupLab{documentDBLab: startExternalLab(t, multiNodeKubeconfigEnv)}
	lab.freshNamespaces(t, "backup-store")
	lab.installBackupStack(t)
	svc := liveService(t, lab.client, lab.store)
	tier, err := svc.TierConfig(lab.ctx, domain.Free)
	if err != nil {
		t.Fatalf("tier: %v", err)
	}
	lab.measureMinorUpgrade(t, svc, "freeupg", tier)
}

type outage struct {
	probes, failed int
	longest        time.Duration
}

// measureMinorUpgrade puts a project on the older minor, then runs the
// platform's minor upgrade under a loop of SELECT 1 through the rw service.
func (lab *backupLab) measureMinorUpgrade(t *testing.T, svc *ProvisioningService, project string, tier config.TierConfig) outage {
	t.Helper()
	lab.freshNamespaces(t, backupLiveOrg+"-"+project)
	lab.provisionAt(t, project, tier)
	namespace := backupLiveOrg + "-" + project
	lab.source.Tier = domain.Free
	if tier.Instances > 1 {
		lab.source.Tier = domain.Standard
	}
	lab.source.Host = project + "-postgres-rw." + namespace + ".svc.cluster.local"
	if err := svc.store.Create(lab.source); err != nil {
		t.Fatalf("register project: %v", err)
	}
	lab.repin(t, project, olderMinorImage)
	lab.waitRolledOnto(t, project, olderMinorImage, tier.Instances)
	t.Logf("%s on %s", project, strings.TrimSpace(lab.psql(t, project, "SHOW server_version")))

	lab.startSelectLoop(t, project)
	time.Sleep(10 * time.Second)
	primaryBefore := lab.currentPrimary(t, project)
	started := time.Now()
	if err := svc.UpgradeVersion(lab.ctx, project, "17"); err != nil {
		t.Fatalf("UpgradeVersion: %v", err)
	}
	catalogue, err := config.PostgresImage("17")
	if err != nil {
		t.Fatalf("catalogue image: %v", err)
	}
	lab.waitRolledOnto(t, project, catalogue, tier.Instances)
	rolled := time.Since(started).Round(time.Second)
	time.Sleep(10 * time.Second)
	result := lab.stopSelectLoop(t, project)
	t.Logf("%s (%d instance(s)): upgrade rolled in %s; primary %s -> %s; %d probes, %d failed, longest outage %s; now %s",
		project, tier.Instances, rolled, primaryBefore, lab.currentPrimary(t, project), result.probes, result.failed,
		result.longest.Round(100*time.Millisecond), strings.TrimSpace(lab.psqlPrimary(t, project, "SHOW server_version")))
	t.Logf("%s events:\n%s", project, lab.kubectl(t, "get", "events", "-n", namespace, "--sort-by=.lastTimestamp", "--field-selector", "involvedObject.kind=Cluster"))
	return result
}

func (lab *backupLab) repin(t *testing.T, project, image string) {
	t.Helper()
	namespace := backupLiveOrg + "-" + project
	cluster, err := lab.client.GetCRD(lab.ctx, k8s.CNPGClusterGVR, namespace, project+"-postgres")
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	if err := unstructured.SetNestedField(cluster.Object, image, "spec", "imageName"); err != nil {
		t.Fatalf("set image: %v", err)
	}
	if err := lab.client.ApplyCRD(lab.ctx, k8s.CNPGClusterGVR, namespace, cluster); err != nil {
		t.Fatalf("apply: %v", err)
	}
}

func (lab *backupLab) currentPrimary(t *testing.T, project string) string {
	t.Helper()
	cluster, err := lab.client.GetCRD(lab.ctx, k8s.CNPGClusterGVR, backupLiveOrg+"-"+project, project+"-postgres")
	if err != nil {
		t.Fatalf("read cluster: %v", err)
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	return primary
}

// psqlPrimary runs SQL on whichever instance is primary now.
func (lab *backupLab) psqlPrimary(t *testing.T, project, sql string) string {
	t.Helper()
	out, err := lab.client.ExecInPod(lab.ctx, backupLiveOrg+"-"+project, lab.currentPrimary(t, project), "postgres",
		[]string{"psql", "-U", "postgres", "-d", "app", "-tAc", sql})
	if err != nil {
		t.Fatalf("psql: %v %s", err, out)
	}
	return out
}

// waitRolledOnto waits for every instance to run the image, ready, with the cluster healthy.
func (lab *backupLab) waitRolledOnto(t *testing.T, project, image string, instances int) {
	t.Helper()
	namespace := backupLiveOrg + "-" + project
	eventuallyLive(t, project+" rolled onto "+image, 15*time.Minute, func() bool {
		cluster, err := lab.client.GetCRD(lab.ctx, k8s.CNPGClusterGVR, namespace, project+"-postgres")
		if err != nil {
			return false
		}
		phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
		if phase != "Cluster in healthy state" {
			return false
		}
		pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "cnpg.io/cluster=" + project + "-postgres,cnpg.io/podRole=instance"})
		if err != nil {
			return false
		}
		onImage := 0
		for _, pod := range pods.Items {
			if pod.DeletionTimestamp == nil && podReady(pod.Status.Conditions) && postgresImage(pod) == image {
				onImage++
			}
		}
		return onImage == instances
	})
}

func postgresImage(pod corev1.Pod) string {
	for _, container := range pod.Spec.Containers {
		if container.Name == "postgres" {
			return container.Image
		}
	}
	return ""
}

func selectLoopPod(project string) string { return project + "-select-loop" }

// startSelectLoop runs a client in the project namespace that opens a new
// connection through the rw service for every SELECT 1, logging each result.
func (lab *backupLab) startSelectLoop(t *testing.T, project string) {
	t.Helper()
	namespace := backupLiveOrg + "-" + project
	dsn := fmt.Sprintf("host=%s-postgres-rw port=5432 dbname=%s user=%s password=%s connect_timeout=2 sslmode=require",
		project, lab.source.DatabaseName, lab.source.Username, lab.source.Password)
	image, err := config.PostgresImage("17")
	if err != nil {
		t.Fatalf("image: %v", err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: selectLoopPod(project), Namespace: namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name: "loop", Image: image, Command: []string{"/bin/bash", "-c"},
				Env:  []corev1.EnvVar{{Name: "DSN", Value: dsn}},
				Args: []string{`while true; do t=$(date +%s.%N); if psql "$DSN" -tAc 'select 1' > /dev/null 2>&1; then echo "$t ok"; else echo "$t fail"; fi; sleep 0.2; done`},
			}},
		},
	}
	if _, err := lab.cs.CoreV1().Pods(namespace).Create(lab.ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create loop: %v", err)
	}
	eventuallyLive(t, "select loop answering", 3*time.Minute, func() bool {
		return strings.Contains(lab.loopLog(t, project), " ok")
	})
}

func (lab *backupLab) loopLog(t *testing.T, project string) string {
	t.Helper()
	stream, err := lab.cs.CoreV1().Pods(backupLiveOrg+"-"+project).GetLogs(selectLoopPod(project), &corev1.PodLogOptions{}).Stream(lab.ctx)
	if err != nil {
		return ""
	}
	defer stream.Close()
	raw, _ := io.ReadAll(stream)
	return string(raw)
}

func (lab *backupLab) stopSelectLoop(t *testing.T, project string) outage {
	t.Helper()
	log := lab.loopLog(t, project)
	_ = lab.cs.CoreV1().Pods(backupLiveOrg+"-"+project).Delete(lab.ctx, selectLoopPod(project), metav1.DeleteOptions{})
	return parseLoop(log)
}

// parseLoop counts failed probes and the longest time from the last success
// before a failure to the next success.
func parseLoop(log string) outage {
	type probe struct {
		at time.Time
		ok bool
	}
	var probes []probe
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		seconds, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		probes = append(probes, probe{at: time.Unix(0, int64(seconds*1e9)), ok: fields[1] == "ok"})
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].at.Before(probes[j].at) })
	result := outage{probes: len(probes)}
	var lastOK time.Time
	inOutage := false
	for _, p := range probes {
		if !p.ok {
			result.failed++
			inOutage = true
			continue
		}
		if inOutage && !lastOK.IsZero() && p.at.Sub(lastOK) > result.longest {
			result.longest = p.at.Sub(lastOK)
		}
		inOutage = false
		lastOK = p.at
	}
	return result
}
