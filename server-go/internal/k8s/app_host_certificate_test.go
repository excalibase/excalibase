package k8s

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

const testIssuer = "letsencrypt"

func issuerRoute() AppRouteOptions {
	route := testRoute
	route.Issuer = testIssuer
	return route
}

var readyCondition = []any{map[string]any{"type": "Ready", "status": "True"}}

func TestRenderAppWorkload_TheIssuerGivesThePlatformHostItsOwnCertificate(t *testing.T) {
	workload := renderWithRoute(t, issuerRoute())
	cert := workload.HostCertificate
	if cert == nil {
		t.Fatal("an app with a public route and an issuer needs a certificate")
	}
	if cert.GetName() != AppObjectName("web") || cert.GetNamespace() != testNamespace || cert.GetKind() != "Certificate" {
		t.Fatalf("certificate is %s %s/%s", cert.GetKind(), cert.GetNamespace(), cert.GetName())
	}
	secret, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	hosts, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	issuer, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "name")
	kind, _, _ := unstructured.NestedString(cert.Object, "spec", "issuerRef", "kind")
	secretLabels, _, _ := unstructured.NestedStringMap(cert.Object, "spec", "secretTemplate", "labels")
	if secret != AppHostTLSSecretName("web") || len(hosts) != 1 || hosts[0] != wantHost || issuer != testIssuer || kind != "ClusterIssuer" {
		t.Fatalf("certificate spec = %v", cert.Object["spec"])
	}
	if !maps.Equal(secretLabels, appLabels(minimalApp())) {
		t.Errorf("the key must carry the app's labels, so teardown finds it: %v", secretLabels)
	}
	if len(workload.Ingress.Spec.TLS) != 0 || workload.Ingress.Annotations[haproxySSLRedirect] != "" {
		t.Errorf("TLS is attached only once the certificate is issued: %+v %v", workload.Ingress.Spec.TLS, workload.Ingress.Annotations)
	}
	if workload.AcmeSolverPolicy == nil || workload.AcmeSolverPolicy.GetName() != AcmeSolverPolicyName {
		t.Error("the edge must reach the HTTP-01 solver pods")
	}
	if !issuerRoute().Public().TLS {
		t.Error("an issuer makes the public URL https")
	}
}

func TestRenderAppWorkload_NoIssuerServesHTTPOnly(t *testing.T) {
	workload := renderWithRoute(t, testRoute)
	if workload.HostCertificate != nil || workload.AcmeSolverPolicy != nil || len(workload.Ingress.Spec.TLS) != 0 {
		t.Fatalf("no issuer, no certificate: %+v", workload)
	}
	if testRoute.Public().TLS {
		t.Error("without an issuer the URL is http")
	}
}

func TestRenderAppWorkload_AnInternalServiceGetsNoCertificate(t *testing.T) {
	workload := renderWithRouteFor(t, internalService(), issuerRoute())
	if workload.HostCertificate != nil || workload.AcmeSolverPolicy != nil {
		t.Fatal("an internal service has no public host to certify")
	}
	if !workload.dropHostCertificate {
		t.Error("an internal service must drop the certificate it had while public")
	}
}

func hostCertificate(t *testing.T, c *Client, name string) *unstructured.Unstructured {
	t.Helper()
	cert, err := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("certificate %s: %v", name, err)
	}
	return cert
}

func TestApplyAppWorkload_ServesHTTPUntilThePlatformCertificateIsIssued(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ingress, _ := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	if len(ingress.Spec.TLS) != 0 || ingress.Annotations[haproxySSLRedirect] != "" {
		t.Fatalf("before issuing, TLS or a redirect would send the HTTP-01 challenge to HTTPS: %+v %v", ingress.Spec.TLS, ingress.Annotations)
	}
	if got := hostCertificate(t, c, AppObjectName("web")).GetLabels()[hostTLSLabel]; got != hostTLSPending {
		t.Fatalf("an unissued certificate is followed by the sweeper: label %q", got)
	}
	if _, err := c.GetCRD(ctx, CiliumNetworkPolicyGVR, testNamespace, AcmeSolverPolicyName); err != nil {
		t.Fatalf("solver policy: %v", err)
	}

	createSecret(t, c, AppHostTLSSecretName("web"), appLabels(minimalApp()))
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatalf("apply: %v", err)
	}
	ingress, _ = clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	assertServesHTTPS(t, ingress.Spec.TLS, ingress.Annotations, wantHost, AppHostTLSSecretName("web"))
	if got := hostCertificate(t, c, AppObjectName("web")).GetLabels()[hostTLSLabel]; got != hostTLSServed {
		t.Errorf("label = %q, want %q", got, hostTLSServed)
	}
}

func TestApplyAppWorkload_TakesTheRedirectAwayWithTheCertificate(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	createSecret(t, c, AppHostTLSSecretName("web"), appLabels(minimalApp()))
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	if err := clientset.CoreV1().Secrets(testNamespace).Delete(ctx, AppHostTLSSecretName("web"), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	ingress, _ := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	if len(ingress.Spec.TLS) != 0 {
		t.Errorf("tls = %+v", ingress.Spec.TLS)
	}
	for key := range httpsRedirectAnnotations {
		if _, kept := ingress.Annotations[key]; kept {
			t.Errorf("annotation %s must go when there is no certificate to redirect to", key)
		}
	}
}

func assertServesHTTPS(t *testing.T, tls []networkingv1.IngressTLS, annotations map[string]string, host, secret string) {
	t.Helper()
	assertRedirectsWith308(t, annotations)
	if len(tls) != 1 || tls[0].SecretName != secret || len(tls[0].Hosts) != 1 || tls[0].Hosts[0] != host {
		t.Errorf("tls = %+v, want %s from %s", tls, host, secret)
	}
}

func TestAttachIssuedAppHostCertificates_ServesHTTPSOnceReady(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	other := minimalApp()
	other.ID, other.Name = "app-other", "other"
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRouteFor(t, other, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	setCertConditions(t, c, AppObjectName("web"), readyCondition)

	if err := c.AttachIssuedAppHostCertificates(ctx); err != nil {
		t.Fatalf("attach: %v", err)
	}
	ingress, _ := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	assertServesHTTPS(t, ingress.Spec.TLS, ingress.Annotations, wantHost, AppHostTLSSecretName("web"))
	if got := hostCertificate(t, c, AppObjectName("web")).GetLabels()[hostTLSLabel]; got != hostTLSServed {
		t.Errorf("a served certificate must leave the sweep: label %q", got)
	}
	waiting, _ := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("other"), metav1.GetOptions{})
	if len(waiting.Spec.TLS) != 0 {
		t.Errorf("an unissued certificate must not be served: %+v", waiting.Spec.TLS)
	}
}

func TestAttachIssuedAppHostCertificates_LeavesAnIngressItDoesNotOwn(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	setCertConditions(t, c, AppObjectName("web"), readyCondition)
	ingresses := clientset.NetworkingV1().Ingresses(testNamespace)
	ingress, _ := ingresses.Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	ingress.Labels = map[string]string{"excalibase.io/app": "someone-else"}
	if _, err := ingresses.Update(ctx, ingress, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.AttachIssuedAppHostCertificates(ctx); err != nil {
		t.Fatal(err)
	}
	ingress, _ = ingresses.Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	if len(ingress.Spec.TLS) != 0 {
		t.Fatal("an ingress of another owner must not be changed")
	}
}

func TestAttachIssuedAppHostCertificates_AGoneIngressIsNotAnError(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	setCertConditions(t, c, AppObjectName("web"), readyCondition)
	if err := clientset.NetworkingV1().Ingresses(testNamespace).Delete(ctx, AppObjectName("web"), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.AttachIssuedAppHostCertificates(ctx); err != nil {
		t.Fatalf("an app deleted mid-sweep is not a failure: %v", err)
	}
}

func TestApplyAppWorkload_AnInternalServiceDropsItsPlatformCertificate(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	createSecret(t, c, AppHostTLSSecretName("web"), appLabels(minimalApp()))
	internal := internalService()
	internal.Name = "web"
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRouteFor(t, internal, issuerRoute())); err != nil {
		t.Fatalf("apply internal: %v", err)
	}
	if _, err := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("certificate still there: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, AppHostTLSSecretName("web"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("key still there: %v", err)
	}
}

func TestPruneAppWorkload_ARenamedAppLosesTheOldHostsCertificate(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	app := minimalApp()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRouteFor(t, app, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	createSecret(t, c, AppHostTLSSecretName("web"), appLabels(app))
	renamed := minimalApp()
	renamed.Name = "shop"
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRouteFor(t, renamed, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	if err := c.PruneAppWorkload(ctx, testNamespace, app.ID, renamed.Name, shortWait); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("old certificate still there: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, AppHostTLSSecretName("web"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("old key still there: %v", err)
	}
	hostCertificate(t, c, AppObjectName("shop"))
}

func TestDeleteAppWorkload_RemovesTheCertificateBeforeItsKey(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	app := minimalApp()
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRouteFor(t, app, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	createSecret(t, c, AppHostTLSSecretName("web"), appLabels(app))
	var order []string
	record := func(kind string) ktesting.ReactionFunc {
		return func(ktesting.Action) (bool, runtime.Object, error) {
			order = append(order, kind)
			return false, nil, nil
		}
	}
	clientset.PrependReactor("delete", "secrets", record("secret"))
	c.dynamicClient.(*dynamicfake.FakeDynamicClient).PrependReactor("delete", "certificates", record("certificate"))
	if err := c.DeleteAppWorkload(ctx, testNamespace, app.ID, shortWait); err != nil {
		t.Fatalf("delete: %v", err)
	}
	certAt, secretAt := slices.Index(order, "certificate"), slices.Index(order, "secret")
	if certAt < 0 || secretAt < 0 || certAt > secretAt {
		t.Fatalf("delete order = %v: a key deleted before its certificate is issued again", order)
	}
}

func TestAppHostCertificate_ReportsIssuedFailedAndMissing(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	if _, err := c.AppHostCertificate(ctx, testNamespace, "web"); !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("missing: %v", err)
	}
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	if state, err := c.AppHostCertificate(ctx, testNamespace, "web"); err != nil || state.Ready || state.Failure != "" {
		t.Fatalf("issuing = %+v, %v", state, err)
	}
	setCertConditions(t, c, AppObjectName("web"), []any{map[string]any{"type": "Issuing", "status": "False", "reason": "Failed", "message": "rate limited"}})
	if state, _ := c.AppHostCertificate(ctx, testNamespace, "web"); state.Failure != "rate limited" {
		t.Fatalf("failed = %+v", state)
	}
	setCertConditions(t, c, AppObjectName("web"), readyCondition)
	if state, _ := c.AppHostCertificate(ctx, testNamespace, "web"); !state.Ready {
		t.Fatalf("ready = %+v", state)
	}
}

func assertRedirectsWith308(t *testing.T, annotations map[string]string) {
	t.Helper()
	snippet := annotations["haproxy.org/backend-config-snippet"]
	if annotations[haproxySSLRedirect] != "false" {
		t.Errorf("the controller's own redirect keeps :8443 and 301, so it must be off: %v", annotations)
	}
	if !strings.Contains(snippet, "code 308") || !strings.Contains(snippet, "unless { ssl_fc }") {
		t.Errorf("plain HTTP must be redirected with 308: %q", snippet)
	}
	if strings.Contains(snippet, ":443") || strings.Contains(snippet, "code 301") {
		t.Errorf("redirect must not name a port or use 301: %q", snippet)
	}
}

func wildcardRoute() AppRouteOptions {
	route := testRoute
	route.WildcardTLS = true
	return route
}

func TestRenderAppWorkload_WildcardServesHTTPSAtOnceWithNoCertificateOfItsOwn(t *testing.T) {
	workload := renderWithRoute(t, wildcardRoute())
	if workload.HostCertificate != nil || workload.AcmeSolverPolicy != nil || len(workload.Ingress.Spec.TLS) != 0 {
		t.Fatalf("the edge's wildcard certificate serves the host: %+v", workload)
	}
	assertRedirectsWith308(t, workload.Ingress.Annotations)
	hsts := workload.Ingress.Annotations["haproxy.org/response-set-header"]
	if !strings.Contains(hsts, "Strict-Transport-Security") || !strings.Contains(hsts, "max-age=31536000") || strings.Contains(hsts, "preload") {
		t.Errorf("HSTS for one year, without preload: %q", hsts)
	}
	if !wildcardRoute().Public().TLS {
		t.Error("a wildcard makes the public URL https")
	}
	if !workload.dropHostCertificate {
		t.Error("an app that had its own certificate must lose it")
	}
}

func TestRenderAppWorkload_WildcardWinsOverTheIssuerForAppHosts(t *testing.T) {
	route := issuerRoute()
	route.WildcardTLS = true
	if workload := renderWithRoute(t, route); workload.HostCertificate != nil || workload.AcmeSolverPolicy != nil {
		t.Fatal("with a wildcard no app host asks the issuer for a certificate")
	}
}

func TestApplyAppWorkload_WildcardDropsTheCertificateAnAppHadBefore(t *testing.T) {
	c, clientset := newLifecycleFakeClient()
	ctx := context.Background()
	createSecret(t, c, AppHostTLSSecretName("web"), appLabels(minimalApp()))
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, issuerRoute())); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyAppWorkload(ctx, testNamespace, renderWithRoute(t, wildcardRoute())); err != nil {
		t.Fatal(err)
	}
	if _, err := c.dynamicClient.Resource(CertificateGVR).Namespace(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("the app's own certificate must be deleted: %v", err)
	}
	if _, err := clientset.CoreV1().Secrets(testNamespace).Get(ctx, AppHostTLSSecretName("web"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("its key must be deleted: %v", err)
	}
	ingress, _ := clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName("web"), metav1.GetOptions{})
	assertRedirectsWith308(t, ingress.Annotations)
	if len(ingress.Spec.TLS) != 0 {
		t.Errorf("no per-host tls entry: %+v", ingress.Spec.TLS)
	}
}
