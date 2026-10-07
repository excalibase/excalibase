package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func ownedSecret(name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "ns",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "postgresql.cnpg.io/v1", Kind: "Cluster", Name: "p-postgres", UID: "u1"}},
	}, Data: map[string][]byte{"ca.crt": []byte("ca")}}
}

func TestKeepSecretsPastOwnerDropsOnlyTheNamedOwnerReferences(t *testing.T) {
	clientset := fake.NewSimpleClientset(ownedSecret("p-postgres-ca"), ownedSecret("p-postgres-server"))
	client := &Client{clientset: clientset}

	if err := client.KeepSecretsPastOwner(context.Background(), "ns", []string{"p-postgres-ca", "p-postgres-missing"}); err != nil {
		t.Fatalf("KeepSecretsPastOwner: %v", err)
	}
	kept, _ := clientset.CoreV1().Secrets("ns").Get(context.Background(), "p-postgres-ca", metav1.GetOptions{})
	if len(kept.OwnerReferences) != 0 || string(kept.Data["ca.crt"]) != "ca" {
		t.Errorf("kept secret: %+v", kept)
	}
	other, _ := clientset.CoreV1().Secrets("ns").Get(context.Background(), "p-postgres-server", metav1.GetOptions{})
	if len(other.OwnerReferences) != 1 {
		t.Error("a secret that was not named keeps its owner")
	}
	// Already released: nothing to do, no error.
	if err := client.KeepSecretsPastOwner(context.Background(), "ns", []string{"p-postgres-ca"}); err != nil {
		t.Fatal(err)
	}
}
