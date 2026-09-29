//go:build live

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const quotaLiveProject, quotaLiveNamespace = "proj-quota", "org1-proj-quota"

// EXC-524: a project with no app follows its org's plan; a downgrade keeps
// what already runs and refuses what is new.
// Run with: go test ./internal/service/ -tags=live -run TestLiveNamespaceQuotaFollowsTheOrgsPlan -v -count=1
func TestLiveNamespaceQuotaFollowsTheOrgsPlan(t *testing.T) {
	lab := &appLiveLab{ctx: context.Background(), instances: fakestore.NewInstances()}
	container, err := k3s.Run(lab.ctx, appLiveK3s)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	lab.container = container
	lab.connect(t)
	lab.namespace(t, quotaLiveNamespace)
	lab.instances.Items[quotaLiveProject] = &domain.DatabaseInstance{
		ProjectID: quotaLiveProject, OrgID: "org1", Namespace: quotaLiveNamespace, DeploymentMode: domain.ModeK8s,
	}
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	svc := NewAppDeployService(&fakeAppStoreForDeploy{apps: map[string]*apphost.App{}}, newFakeDeployStore(), lab.client, lab.instances, nil, lab.render())
	svc.SetPlanTiers(NewOrgPlanTiers(lab.instances, orgs))
	svc.SetNamespaceQuotaTiers(builtinTiers(t))

	if err := svc.SyncProjectQuota(lab.ctx, quotaLiveProject); err != nil {
		t.Fatalf("size on FREE: %v", err)
	}
	free := lab.observedQuota(t, 7)
	t.Logf("FREE quota: %v", free)

	t.Run("an org moved to ENTERPRISE with no app deployed gets the ENTERPRISE quota", func(t *testing.T) {
		orgs.AddOrg("org1", domain.Enterprise)
		svc.SyncAllProjectQuotas(lab.ctx)
		hard := lab.observedQuota(t, 45)
		if pods := hard[corev1.ResourcePods]; pods.Value() != 120 {
			t.Fatalf("pods = %d, want 120", pods.Value())
		}
		for i := range 30 {
			lab.claim(t, i)
		}
		for i := range 30 {
			lab.pausePod(t, i)
		}
		lab.waitRunning(t, 30)
	})

	t.Run("a downgrade to FREE keeps what runs and refuses what is new", func(t *testing.T) {
		orgs.AddOrg("org1", domain.Free)
		svc.SyncAllProjectQuotas(lab.ctx)
		hard := lab.observedQuota(t, 30)
		if pods := hard[corev1.ResourcePods]; pods.Value() != 30 {
			t.Fatalf("pods = %d, want the 30 already running", pods.Value())
		}
		lab.waitRunning(t, 30)
		if err := lab.tryClaim(30); err == nil {
			t.Fatal("a new claim above the running ones must be refused on FREE")
		}
		if err := lab.tryPausePod(30); err == nil {
			t.Fatal("a new pod above the running ones must be refused on FREE")
		}
	})
}

func builtinTiers(t *testing.T) fixedTierConfigs {
	t.Helper()
	out := fixedTierConfigs{}
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		tc, err := config.GetTierConfig(tier)
		if err != nil {
			t.Fatal(err)
		}
		out[tier] = tc
	}
	return out
}

// observedQuota waits until the quota controller reports the given claim count as the hard limit.
func (lab *appLiveLab) observedQuota(t *testing.T, pvcs int64) corev1.ResourceList {
	t.Helper()
	var hard corev1.ResourceList
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		q, err := lab.cs.CoreV1().ResourceQuotas(quotaLiveNamespace).Get(lab.ctx, "namespace-quota", metav1.GetOptions{})
		if err == nil {
			if got, ok := q.Status.Hard[corev1.ResourcePersistentVolumeClaims]; ok && got.Value() == pvcs {
				return q.Status.Hard
			}
			hard = q.Status.Hard
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("quota never reached %d claims: %v", pvcs, hard)
	return nil
}

func (lab *appLiveLab) tryClaim(i int) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("claim-%d", i), Namespace: quotaLiveNamespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}},
		},
	}
	_, err := lab.cs.CoreV1().PersistentVolumeClaims(quotaLiveNamespace).Create(lab.ctx, pvc, metav1.CreateOptions{})
	return err
}

func (lab *appLiveLab) claim(t *testing.T, i int) {
	t.Helper()
	if err := lab.tryClaim(i); err != nil {
		t.Fatalf("claim %d: %v", i, err)
	}
}

func (lab *appLiveLab) tryPausePod(i int) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("pause-%d", i), Namespace: quotaLiveNamespace},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "pause", Image: "registry.k8s.io/pause:3.10"}}},
	}
	_, err := lab.cs.CoreV1().Pods(quotaLiveNamespace).Create(lab.ctx, pod, metav1.CreateOptions{})
	return err
}

func (lab *appLiveLab) pausePod(t *testing.T, i int) {
	t.Helper()
	if err := lab.tryPausePod(i); err != nil {
		t.Fatalf("pod %d: %v", i, err)
	}
}

func (lab *appLiveLab) waitRunning(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	running := 0
	for time.Now().Before(deadline) {
		pods, err := lab.cs.CoreV1().Pods(quotaLiveNamespace).List(lab.ctx, metav1.ListOptions{})
		if err == nil {
			running = 0
			for _, pod := range pods.Items {
				if pod.Status.Phase == corev1.PodRunning {
					running++
				}
			}
			if running == want {
				return
			}
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("%d pods running, want %d", running, want)
}
