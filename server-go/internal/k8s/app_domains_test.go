package k8s

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var testDomainOptions = AppDomainOptions{Issuer: "letsencrypt", Route: testRoute}

func TestSyncAppDomains_RoutesEachHostWithItsOwnCertificate(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	app := fullApp()
	if err := c.SyncAppDomains(ctx, testNamespace, app, []string{"shop.example.com"}, testDomainOptions); err != nil {
		t.Fatalf("sync: %v", err)
	}
	name := AppDomainObjectName(app.Name, "shop.example.com")
	ingress, err := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ingress: %v", err)
	}
	rule := ingress.Spec.Rules[0]
	if rule.Host != "shop.example.com" || rule.HTTP.Paths[0].Backend.Service.Name != AppObjectName(app.Name) {
		t.Fatalf("rule = %+v", rule)
	}
	if len(ingress.Spec.TLS) != 0 {
		t.Fatalf("no TLS before the certificate exists, or the challenge is redirected: %+v", ingress.Spec.TLS)
	}
	createSecret(t, c, name+"-tls", appDomainLabels(app, "shop.example.com"))
	if err := c.SyncAppDomains(ctx, testNamespace, app, []string{"shop.example.com"}, testDomainOptions); err != nil {
		t.Fatal(err)
	}
	ingress, _ = clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, name, metav1.GetOptions{})
	if len(ingress.Spec.TLS) != 1 || ingress.Spec.TLS[0].SecretName != name+"-tls" || ingress.Spec.TLS[0].Hosts[0] != "shop.example.com" {
		t.Fatalf("tls = %+v", ingress.Spec.TLS)
	}
	cert, err := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	issuer, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	hosts, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	secretLabels, _, _ := unstructured.NestedStringMap(cert.Object, "spec", "secretTemplate", "labels")
	if issuer != "letsencrypt" || len(hosts) != 1 || hosts[0] != "shop.example.com" || secretLabels["excalibase.io/app"] != app.ID {
		t.Fatalf("certificate spec = %v", cert.Object["spec"])
	}
	policy, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, AcmeSolverPolicyName)
	if err != nil {
		t.Fatalf("solver policy: %v", err)
	}
	selector, _, _ := unstructured.NestedStringMap(policy.Object, "spec", "endpointSelector", "matchLabels")
	if selector[acmeSolverLabel] != "true" {
		t.Fatalf("the solver policy must select only solver pods: %v", selector)
	}
}

func TestSyncAppDomains_RemovesWhatIsNoLongerWanted(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	app := fullApp()
	other := fullApp()
	other.ID, other.Name = "app-other", "other"
	if err := c.SyncAppDomains(ctx, testNamespace, app, []string{"a.example.com", "b.example.com"}, testDomainOptions); err != nil {
		t.Fatal(err)
	}
	if err := c.SyncAppDomains(ctx, testNamespace, other, []string{"c.example.com"}, testDomainOptions); err != nil {
		t.Fatal(err)
	}
	createSecret(t, c, AppDomainObjectName(app.Name, "a.example.com")+"-tls", appDomainLabels(app, "a.example.com"))

	if err := c.SyncAppDomains(ctx, testNamespace, app, []string{"b.example.com"}, testDomainOptions); err != nil {
		t.Fatal(err)
	}
	gone := AppDomainObjectName(app.Name, "a.example.com")
	if _, err := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, gone, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the removed domain's route is still there: %v", err)
	}
	if _, err := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace).Get(ctx, gone, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the removed domain's certificate is still there: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, gone+"-tls", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the removed domain's key is still there: %v", err)
	}
	for _, kept := range []string{AppDomainObjectName(app.Name, "b.example.com"), AppDomainObjectName(other.Name, "c.example.com")} {
		if _, err := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, kept, metav1.GetOptions{}); err != nil {
			t.Errorf("%s must stay: %v", kept, err)
		}
	}
	if err := c.SyncAppDomains(ctx, testNamespace, app, nil, AppDomainOptions{}); err != nil {
		t.Fatalf("removing every domain needs no issuer: %v", err)
	}
}

func TestSyncAppDomains_RefusesWithoutAnIssuer(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	err := c.SyncAppDomains(context.Background(), testNamespace, fullApp(), []string{"a.example.com"}, AppDomainOptions{Route: testRoute})
	if !errors.Is(err, ErrRenderApp) {
		t.Fatalf("err = %v", err)
	}
}

func setCertConditions(t *testing.T, c *Client, name string, conditions []any) {
	t.Helper()
	certs := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace)
	cert, err := certs.Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedSlice(cert.Object, conditions, "status", "conditions")
	if _, err := certs.Update(context.Background(), cert, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestAppDomainCertificate(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	app := fullApp()
	if err := c.SyncAppDomains(ctx, testNamespace, app, []string{"a.example.com"}, testDomainOptions); err != nil {
		t.Fatal(err)
	}
	name := AppDomainObjectName(app.Name, "a.example.com")
	if state, err := c.AppDomainCertificate(ctx, testNamespace, app.Name, "a.example.com"); err != nil || state.Ready || state.Failure != "" {
		t.Fatalf("new = %+v %v", state, err)
	}
	setCertConditions(t, c, name, []any{map[string]any{"type": "Issuing", "status": "False", "reason": "Failed", "message": "acme: 403 unauthorized"}})
	if state, _ := c.AppDomainCertificate(ctx, testNamespace, app.Name, "a.example.com"); state.Failure != "acme: 403 unauthorized" {
		t.Fatalf("failed = %+v", state)
	}
	setCertConditions(t, c, name, []any{map[string]any{"type": "Ready", "status": "True"}})
	if state, _ := c.AppDomainCertificate(ctx, testNamespace, app.Name, "a.example.com"); !state.Ready {
		t.Fatalf("ready = %+v", state)
	}
	if _, err := c.AppDomainCertificate(ctx, testNamespace, app.Name, "missing.example.com"); err == nil {
		t.Fatal("a missing certificate must be an error")
	}
}

func TestClusterIssuerReady(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	if err := c.ClusterIssuerReady(ctx, "letsencrypt"); !errors.Is(err, ErrClusterIssuerNotReady) {
		t.Fatalf("missing: %v", err)
	}
	issuer := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"conditions": []any{
		map[string]any{"type": "Ready", "status": "False"},
	}}}}
	issuer.SetAPIVersion("cert-manager.io/v1")
	issuer.SetKind("ClusterIssuer")
	issuer.SetName("letsencrypt")
	issuers := c.dynamicClient.Resource(ClusterIssuerGVR)
	if _, err := issuers.Create(ctx, issuer, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ClusterIssuerReady(ctx, "letsencrypt"); !errors.Is(err, ErrClusterIssuerNotReady) {
		t.Fatalf("not ready: %v", err)
	}
	_ = unstructured.SetNestedSlice(issuer.Object, []any{map[string]any{"type": "Ready", "status": "True"}}, "status", "conditions")
	if _, err := issuers.Update(ctx, issuer, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ClusterIssuerReady(ctx, "letsencrypt"); err != nil {
		t.Fatalf("ready: %v", err)
	}
}

func TestDeleteAppWorkload_TakesTheDomainsCertificatesToo(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.SyncAppDomains(ctx, testNamespace, app, []string{"a.example.com"}, testDomainOptions); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteAppWorkload(ctx, testNamespace, app.ID, shortWait); err != nil {
		t.Fatal(err)
	}
	list, err := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("certificates left: %v %v", list, err)
	}
	assertNothingLeft(t, c, app)
}

func createSecret(t *testing.T, c *Client, name string, labels map[string]string) {
	t.Helper()
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Labels: labels}}
	if _, err := c.clientset.CoreV1().Secrets(testNamespace).Create(context.Background(), secret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}
