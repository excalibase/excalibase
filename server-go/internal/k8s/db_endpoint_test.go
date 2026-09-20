package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	dbEndpointNS      = "org-proj-abc"
	dbEndpointSvcName = "proj-abc-postgres-public"
	dbEndpointRWName  = "proj-abc-postgres-rw"
)

// cnpgReadWriteService models the Service the CNPG operator creates for a
// cluster. Our LoadBalancer copies its selector rather than guessing at the
// operator's label scheme.
func cnpgReadWriteService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: dbEndpointRWName, Namespace: dbEndpointNS},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"cnpg.io/cluster":      "proj-abc-postgres",
				"cnpg.io/instanceRole": "primary",
			},
			Ports: []corev1.ServicePort{{Port: 5432}},
		},
	}
}

func dbEndpointSpec() PublicDBServiceSpec {
	return PublicDBServiceSpec{
		Name:             dbEndpointSvcName,
		Port:             30111,
		ReadWriteService: dbEndpointRWName,
		SharedIPKey:      "excalibase-db-edge",
		ProjectID:        "proj-abc",
	}
}

func TestEnsurePublicDBServiceCarriesThePortAndTheSharingKey(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()

	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, dbEndpointSpec()); err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}
	svc, err := c.clientset.CoreV1().Services(dbEndpointNS).Get(ctx, dbEndpointSvcName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("service should exist: %v", err)
	}
	if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("type = %q, want LoadBalancer", svc.Spec.Type)
	}
	if got := svc.Annotations[metalLBSharedIPAnnotation]; got != "excalibase-db-edge" {
		t.Fatalf("sharing annotation = %q", got)
	}
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != 30111 {
		t.Fatalf("ports = %+v", svc.Spec.Ports)
	}
	if svc.Spec.Ports[0].TargetPort.IntValue() != postgresPort {
		t.Fatalf("target port = %v, want the postgres port", svc.Spec.Ports[0].TargetPort)
	}
}

func TestEnsurePublicDBServiceCopiesTheOperatorsSelector(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()

	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, dbEndpointSpec()); err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}
	svc, _ := c.clientset.CoreV1().Services(dbEndpointNS).Get(ctx, dbEndpointSvcName, metav1.GetOptions{})
	want := cnpgReadWriteService().Spec.Selector
	if len(svc.Spec.Selector) != len(want) {
		t.Fatalf("selector = %v, want %v", svc.Spec.Selector, want)
	}
	for k, v := range want {
		if svc.Spec.Selector[k] != v {
			t.Fatalf("selector[%s] = %q, want %q", k, svc.Spec.Selector[k], v)
		}
	}
}

func TestEnsurePublicDBServiceRefusesWithoutTheReadWriteService(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsurePublicDBService(context.Background(), dbEndpointNS, dbEndpointSpec()); err == nil {
		t.Fatal("a public endpoint must not be built on a guessed selector")
	}
}

func TestEnsurePublicDBServiceRepointsAnExistingServiceToTheHeldPort(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()

	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, dbEndpointSpec()); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	spec := dbEndpointSpec()
	spec.Port = 30222
	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, spec); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	svc, _ := c.clientset.CoreV1().Services(dbEndpointNS).Get(ctx, dbEndpointSvcName, metav1.GetOptions{})
	if svc.Spec.Ports[0].Port != 30222 {
		t.Fatalf("port = %d, want the held port", svc.Spec.Ports[0].Port)
	}
}

func TestPublicDBServiceExistsAnswersTruthfully(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()

	exists, err := c.PublicDBServiceExists(ctx, dbEndpointNS, dbEndpointSvcName)
	if err != nil {
		t.Fatalf("PublicDBServiceExists: %v", err)
	}
	if exists {
		t.Fatal("no service has been created yet")
	}
	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, dbEndpointSpec()); err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}
	exists, err = c.PublicDBServiceExists(ctx, dbEndpointNS, dbEndpointSvcName)
	if err != nil {
		t.Fatalf("PublicDBServiceExists: %v", err)
	}
	if !exists {
		t.Fatal("the service was created and must be seen")
	}
}

func TestDeletePublicDBServiceIsIdempotent(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()

	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, dbEndpointSpec()); err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}
	if err := c.DeletePublicDBService(ctx, dbEndpointNS, dbEndpointSvcName); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	if err := c.DeletePublicDBService(ctx, dbEndpointNS, dbEndpointSvcName); err != nil {
		t.Fatalf("delete of an absent service must succeed: %v", err)
	}
	exists, err := c.PublicDBServiceExists(ctx, dbEndpointNS, dbEndpointSvcName)
	if err != nil {
		t.Fatalf("PublicDBServiceExists: %v", err)
	}
	if exists {
		t.Fatal("the port must stop answering once the service is deleted")
	}
}
