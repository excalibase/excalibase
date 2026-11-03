package k8s

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EXC-666: the public read port copies the selector of the operator's replica
// Service, so it reaches replicas only and follows a switchover.

func operatorService(name string, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: dbEndpointNS},
		Spec:       corev1.ServiceSpec{Selector: selector},
	}
}

func TestTheReadPortSelectsWhatTheReplicaServiceSelects(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		selector map[string]string
		target   int
	}{
		{"cnpg -ro", "proj-abc-postgres-ro", map[string]string{"cnpg.io/cluster": "proj-abc-postgres", "cnpg.io/instanceRole": "replica"}, 5432},
		{"moco replica", "moco-proj-abc-mysql-replica", map[string]string{
			"app.kubernetes.io/name": "mysql", "app.kubernetes.io/instance": "proj-abc-mysql",
			"app.kubernetes.io/created-by": "moco", "moco.cybozu.com/role": "replica"}, 3306},
	}
	for _, tc := range cases {
		c := newFakeClient(operatorService(tc.source, tc.selector))
		spec := PublicDBServiceSpec{Name: "proj-abc-read-public", Port: 30222, ReadWriteService: tc.source,
			SharedIPKey: "excalibase-db-edge", ProjectID: "proj-abc", TargetPort: tc.target}
		if err := c.EnsurePublicDBService(context.Background(), dbEndpointNS, spec); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		svc, err := c.clientset.CoreV1().Services(dbEndpointNS).Get(context.Background(), spec.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(svc.Spec.Selector, tc.selector) {
			t.Errorf("%s: selector %v", tc.name, svc.Spec.Selector)
		}
		if svc.Spec.Ports[0].TargetPort.IntValue() != tc.target || svc.Annotations[metalLBSharedIPAnnotation] != "excalibase-db-edge" {
			t.Errorf("%s: service %+v", tc.name, svc.Spec)
		}
	}
}

func TestTheReadPortIsRefusedWithoutTheReplicaService(t *testing.T) {
	c := newFakeClient()
	spec := PublicDBServiceSpec{Name: "proj-abc-read-public", Port: 30222, ReadWriteService: "proj-abc-postgres-ro"}
	if err := c.EnsurePublicDBService(context.Background(), dbEndpointNS, spec); err == nil {
		t.Fatal("a read port was published with no replica Service to copy from")
	}
}
