package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

// EXC-675: a rotated password ends the sessions opened with the old one, on
// the primary and on each replica once it has replayed the change.

const (
	rotPrimaryLSN = "0/3000060"
	rotPrimary    = rotProject + "-postgres-1"
)

// clusterWithReplicas seeds the CNPG cluster: -1 is the primary, -2 and -3 replicas.
func (h *rotationHarness) clusterWithReplicas() {
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": rotProject + postgresClusterSuffix, "namespace": rotNamespace},
		"status": map[string]any{
			"currentPrimary": rotPrimary,
			"instanceNames":  []any{rotPrimary, rotProject + "-postgres-2", rotProject + "-postgres-3"},
		},
	}}
	h.kube.CRDs[rotNamespace+"/"+rotProject+postgresClusterSuffix] = cluster
	h.kube.ExecAnswerInPod = func(pod string, _ []string) (string, error) {
		if pod == rotPrimary {
			return "2\n" + rotPrimaryLSN + "\n", nil
		}
		return "1\n", nil
	}
}

func (h *rotationHarness) readRoleOn() {
	h.svc.SetFeatures(features.NewStatic(features.DBReadRole))
}

// sessionScripts are the session-ending scripts each pod was given, in order.
func sessionScripts(kube *k8s.MockClient) []string {
	var scripts []string
	for _, stdin := range kube.ExecStdin {
		if strings.Contains(stdin, "pg_terminate_backend") {
			scripts = append(scripts, stdin)
		}
	}
	return scripts
}

func TestRotationEndsEachRolesSessionsOnEveryCopy(t *testing.T) {
	h := newRotationHarness(t)
	h.clusterWithReplicas()
	h.readRoleOn()

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	scripts := sessionScripts(h.kube)
	// Three roles, each on the primary and two replicas.
	if len(scripts) != 9 {
		t.Fatalf("session scripts = %d, want 9", len(scripts))
	}
	for i, role := range []string{roleAuthAdmin, roleApp, rotOwner} {
		primary, replicas := scripts[3*i], scripts[3*i+1:3*i+3]
		if !strings.Contains(primary, sqlTextLiteral(role)) || !strings.Contains(primary, "pg_current_wal_lsn") {
			t.Errorf("%s: the primary script does not end its sessions and read the position: %s", role, primary)
		}
		for _, replica := range replicas {
			if !strings.Contains(replica, sqlTextLiteral(role)) || !strings.Contains(replica, "'"+rotPrimaryLSN+"'::pg_lsn") {
				t.Errorf("%s: a replica ends sessions without waiting for the change: %s", role, replica)
			}
		}
	}
	podsAsked := strings.Join(h.kube.Calls, " ")
	for _, pod := range []string{"-postgres-2", "-postgres-3"} {
		if !strings.Contains(podsAsked, rotNamespace+"/"+rotProject+pod) {
			t.Errorf("replica %s was never reached", pod)
		}
	}
}

func TestRotationLeavesSessionsAloneWhileTheFlagIsOff(t *testing.T) {
	h := newRotationHarness(t)
	h.clusterWithReplicas()

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if scripts := sessionScripts(h.kube); len(scripts) != 0 {
		t.Fatalf("flag off ended sessions: %v", scripts)
	}
}

// The password is already promoted when its sessions cannot all be ended;
// the caller learns that, and a retry rotates again.
func TestRotationReportsSessionsAReplicaCouldNotEnd(t *testing.T) {
	h := newRotationHarness(t)
	h.clusterWithReplicas()
	h.readRoleOn()
	h.kube.ExecAnswerInPod = func(pod string, _ []string) (string, error) {
		switch pod {
		case rotPrimary:
			return rotPrimaryLSN + "\n", nil
		case rotProject + "-postgres-3":
			return "", errors.New("the replica has not replayed the password change")
		}
		return "", nil
	}

	_, err := h.svc.RotateCredentials(context.Background(), rotProject)
	if err == nil || !strings.Contains(err.Error(), "sessions") {
		t.Fatalf("err = %v, want one about the old sessions", err)
	}
	if h.vault.data[rotAuthCurrent]["password"] == testutil.FixturePassword(roleAuthAdmin) {
		t.Error("the proved password was not kept as current")
	}
}

func TestRotationRefusesAPositionThatIsNotAnLSN(t *testing.T) {
	h := newRotationHarness(t)
	h.clusterWithReplicas()
	h.readRoleOn()
	h.kube.ExecAnswerInPod = func(pod string, _ []string) (string, error) {
		return "0/1'; DROP TABLE x; --\n", nil
	}

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err == nil {
		t.Fatal("a malformed position reached a replica script")
	}
	for _, stdin := range h.kube.ExecStdin {
		if strings.Contains(stdin, "DROP TABLE") {
			t.Fatal("the primary's answer was copied into SQL")
		}
	}
}

func TestRotationNeedsTheClusterToEndSessions(t *testing.T) {
	h := newRotationHarness(t)
	h.readRoleOn()

	_, err := h.svc.RotateCredentials(context.Background(), rotProject)
	if err == nil || !strings.Contains(err.Error(), "sessions") {
		t.Fatalf("err = %v, want the sessions left open reported", err)
	}
}

// A single host runs one container per database: the password changes there,
// and the old sessions end there, through the container engine.
func TestSingleHostPostgresRotationRunsInTheContainer(t *testing.T) {
	h := newRotationHarness(t)
	inst, _ := h.store.FindByProjectID(rotProject)
	inst.DeploymentMode = domain.ModeDocker
	if err := h.store.Update(inst); err != nil {
		t.Fatalf("update: %v", err)
	}
	docker := &fakeDockerClientForAdapter{}
	h.svc.SetDockerClient(docker)
	h.readRoleOn()

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if order := rotatedRoleOrder(t, docker.stdins); len(order) != 3 {
		t.Fatalf("container ALTERs = %v, want three", order)
	}
	ended := 0
	for _, stdin := range docker.stdins {
		if strings.Contains(stdin, "pg_terminate_backend") {
			ended++
		}
	}
	if ended != 3 {
		t.Fatalf("container session scripts = %d, want 3", ended)
	}
	if len(h.kube.ExecStdin) != 0 {
		t.Fatal("a single-host rotation reached the cluster client")
	}
}

func TestRotationReplacesTheReaderBeforeTheOwner(t *testing.T) {
	h := newRotationHarness(t)
	h.vault.data["projects/rot-db/credentials/"+roleReader] = map[string]string{
		"host": "h.local", "port": "5432", "database": "app",
		"username": roleReader, "password": testutil.FixturePassword(roleReader),
	}

	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	order := rotatedRoleOrder(t, h.kube.ExecStdin)
	want := []string{roleAuthAdmin, roleApp, roleReader, rotOwner}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

type rotationObserver struct{ projects []string }

func (r *rotationObserver) CredentialsRotated(projectID string) {
	r.projects = append(r.projects, projectID)
}

// The platform's own pools hold the old password; each promoted role tells them.
func TestRotationTellsCredentialObservers(t *testing.T) {
	h := newRotationHarness(t)
	observer := &rotationObserver{}
	h.svc.AddCredentialObserver(observer)
	if _, err := h.svc.RotateCredentials(context.Background(), rotProject); err != nil {
		t.Fatalf("RotateCredentials: %v", err)
	}
	if len(observer.projects) != 3 || observer.projects[0] != rotProject {
		t.Fatalf("observer told %v, want the project once per role", observer.projects)
	}
}
