package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

var (
	CertificateGVR   = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	ClusterIssuerGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers"}
)

const (
	// appDomainLabel names the custom domain an object serves, hashed to fit a label.
	appDomainLabel = "excalibase.io/domain"
	// AcmeSolverPolicyName admits the edge to cert-manager's HTTP-01 solver pods.
	AcmeSolverPolicyName = "acme-http01-solver"
	acmeSolverLabel      = "acme.cert-manager.io/http01-solver"
	acmeSolverPort       = 8089
)

// ErrClusterIssuerNotReady refuses custom domains when their issuer cannot issue.
var ErrClusterIssuerNotReady = errors.New("the ACME ClusterIssuer is not ready")

// AppDomainOptions are the issuer and the edge a custom domain is served through.
type AppDomainOptions struct {
	Issuer string
	Route  AppRouteOptions
}

// CertificateState is what cert-manager reports for one domain's certificate.
type CertificateState struct {
	Ready   bool
	Failure string
}

func domainKey(host string) string {
	sum := sha256.Sum256([]byte(host))
	return hex.EncodeToString(sum[:8])
}

// AppDomainObjectName names a domain's Ingress and Certificate; its Secret adds "-tls".
func AppDomainObjectName(appName, host string) string {
	return AppObjectName(appName) + "-d-" + domainKey(host)
}

func appDomainLabels(app *apphost.App, host string) map[string]string {
	labels := appLabels(app)
	labels[appDomainLabel] = domainKey(host)
	return labels
}

// buildAppDomainIngress serves the domain over TLS only once its certificate
// exists: before that the edge would redirect the HTTP-01 challenge to HTTPS.
func buildAppDomainIngress(namespace string, app *apphost.App, host string, opts AppDomainOptions, issued bool) *networkingv1.Ingress {
	route := opts.Route
	route.TLSSecret = ""
	ingress := buildAppIngress(namespace, app, host, route)
	name := AppDomainObjectName(app.Name, host)
	ingress.Name = name
	ingress.Labels = appDomainLabels(app, host)
	if issued {
		ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: name + "-tls"}}
	}
	return ingress
}

func buildAppDomainCertificate(namespace string, app *apphost.App, host string, opts AppDomainOptions) *unstructured.Unstructured {
	name := AppDomainObjectName(app.Name, host)
	labels := appDomainLabels(app, host)
	spec := map[string]any{
		"secretName":     name + "-tls",
		"dnsNames":       []any{host},
		"issuerRef":      map[string]any{"name": opts.Issuer, "kind": "ClusterIssuer", "group": CertificateGVR.Group},
		"secretTemplate": map[string]any{"labels": stringMapAny(labels)},
	}
	cert := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	cert.SetAPIVersion(CertificateGVR.GroupVersion().String())
	cert.SetKind("Certificate")
	cert.SetName(name)
	cert.SetNamespace(namespace)
	cert.SetLabels(labels)
	return cert
}

func stringMapAny(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// buildAcmeSolverPolicy admits only the edge, on the solver's port, to the
// pods cert-manager starts to answer an HTTP-01 challenge.
func buildAcmeSolverPolicy(namespace string, route AppRouteOptions) (*unstructured.Unstructured, error) {
	from := map[string]string{podNamespaceKey: route.IngressFromNamespace}
	for key, value := range route.IngressFromLabels {
		from["k8s:"+key] = value
	}
	spec := ciliumPolicySpec{
		EndpointSelector: metav1.LabelSelector{MatchLabels: map[string]string{acmeSolverLabel: "true"}},
		Ingress: []ciliumIngressRule{{
			FromEndpoints: []metav1.LabelSelector{{MatchLabels: from}},
			ToPorts:       []ciliumPortRule{{Ports: []ciliumPort{{Port: strconv.Itoa(acmeSolverPort), Protocol: protocolTCP}}}},
		}},
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&spec)
	if err != nil {
		return nil, fmt.Errorf("%w: solver policy: %w", ErrRenderApp, err)
	}
	policy := &unstructured.Unstructured{Object: map[string]any{"spec": content}}
	policy.SetAPIVersion(CiliumNetworkPolicyGVR.GroupVersion().String())
	policy.SetKind(ciliumPolicyKind)
	policy.SetName(AcmeSolverPolicyName)
	policy.SetNamespace(namespace)
	policy.SetLabels(map[string]string{"app.kubernetes.io/managed-by": appManagedByValue})
	return policy, nil
}

// SyncAppDomains makes the app's custom-domain routes exactly hosts, under the
// app's current name: each gets an Ingress and a Certificate, and every other
// domain route of that name is removed with its certificate and key.
func (c *Client) SyncAppDomains(ctx context.Context, namespace string, app *apphost.App, hosts []string, opts AppDomainOptions) error {
	wanted := map[string]bool{}
	if len(hosts) > 0 {
		if opts.Issuer == "" {
			return fmt.Errorf("%w: custom domains need an ACME issuer", ErrRenderApp)
		}
		if err := opts.Route.validate(); err != nil {
			return err
		}
		policy, err := buildAcmeSolverPolicy(namespace, opts.Route)
		if err != nil {
			return err
		}
		if err := c.applyCiliumPolicy(ctx, namespace, policy, "ACME solver policy"); err != nil {
			return err
		}
	}
	for _, host := range hosts {
		wanted[domainKey(host)] = true
		if err := c.applyAppDomain(ctx, namespace, app, host, opts); err != nil {
			return err
		}
	}
	return c.removeUnwantedDomains(ctx, namespace, app, wanted)
}

func (c *Client) applyAppDomain(ctx context.Context, namespace string, app *apphost.App, host string, opts AppDomainOptions) error {
	cert := buildAppDomainCertificate(namespace, app, host, opts)
	if err := c.applyUnstructured(ctx, CertificateGVR, namespace, cert, "certificate"); err != nil {
		return err
	}
	_, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, AppDomainObjectName(app.Name, host)+"-tls", metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("read domain certificate secret: %w", err)
	}
	return c.applyAppIngress(ctx, namespace, buildAppDomainIngress(namespace, app, host, opts, err == nil))
}

func (c *Client) applyUnstructured(ctx context.Context, gvr schema.GroupVersionResource, namespace string, desired *unstructured.Unstructured, what string) error {
	resources := c.dynamicClient.Resource(gvr).Namespace(namespace)
	existing, err := resources.Get(ctx, desired.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := resources.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create %s: %w", what, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", what, err)
	}
	updated := existing.DeepCopy()
	updated.SetLabels(desired.GetLabels())
	updated.Object["spec"] = runtime.DeepCopyJSONValue(desired.Object["spec"])
	if _, err := resources.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update %s: %w", what, err)
	}
	return nil
}

// removeUnwantedDomains deletes the route before the certificate and its key.
func (c *Client) removeUnwantedDomains(ctx context.Context, namespace string, app *apphost.App, wanted map[string]bool) error {
	selector := "excalibase.io/app=" + app.ID + ",app.kubernetes.io/managed-by=" + appManagedByValue +
		",app.kubernetes.io/name=" + app.Name + "," + appDomainLabel
	opts := metav1.ListOptions{LabelSelector: selector}
	unwanted := func(labels map[string]string) bool { return !wanted[labels[appDomainLabel]] }
	ingresses := c.clientset.NetworkingV1().Ingresses(namespace)
	certs := c.dynamicClient.Resource(CertificateGVR).Namespace(namespace)
	secrets := c.clientset.CoreV1().Secrets(namespace)
	return deleteOwned(ctx, []ownedKind{
		{"domain ingress", func(ctx context.Context) ([]string, error) {
			list, err := ingresses.List(ctx, opts)
			if err != nil {
				return nil, err
			}
			return namesMatching(list.Items, unwanted), nil
		}, func(ctx context.Context, name string) error {
			return ingresses.Delete(ctx, name, metav1.DeleteOptions{})
		}},
		{"domain certificate", func(ctx context.Context) ([]string, error) {
			list, err := certs.List(ctx, opts)
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			return namesMatching(list.Items, unwanted), nil
		}, func(ctx context.Context, name string) error { return certs.Delete(ctx, name, metav1.DeleteOptions{}) }},
		{"domain key", func(ctx context.Context) ([]string, error) {
			list, err := secrets.List(ctx, opts)
			if err != nil {
				return nil, err
			}
			return namesMatching(list.Items, unwanted), nil
		}, func(ctx context.Context, name string) error { return secrets.Delete(ctx, name, metav1.DeleteOptions{}) }},
	})
}

func namesMatching[T any, P interface {
	*T
	metav1.Object
}](items []T, keep func(map[string]string) bool) []string {
	names := make([]string, 0, len(items))
	for i := range items {
		object := P(&items[i])
		if keep(object.GetLabels()) {
			names = append(names, object.GetName())
		}
	}
	return names
}

// AppDomainCertificate reports whether the domain's certificate is issued, or why issuing failed.
func (c *Client) AppDomainCertificate(ctx context.Context, namespace, appName, host string) (CertificateState, error) {
	cert, err := c.dynamicClient.Resource(CertificateGVR).Namespace(namespace).Get(ctx, AppDomainObjectName(appName, host), metav1.GetOptions{})
	if err != nil {
		return CertificateState{}, fmt.Errorf("read certificate: %w", err)
	}
	conditions, _, _ := unstructured.NestedSlice(cert.Object, "status", "conditions")
	state := CertificateState{}
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		kind, status, reason, message := condition["type"], condition["status"], condition["reason"], condition["message"]
		if kind == "Ready" && status == "True" {
			return CertificateState{Ready: true}, nil
		}
		if kind == "Issuing" && status == "False" && reason == "Failed" {
			state.Failure = fmt.Sprint(message)
		}
	}
	return state, nil
}

// ClusterIssuerReady refuses an issuer that is missing or not ready to issue.
func (c *Client) ClusterIssuerReady(ctx context.Context, name string) error {
	issuer, err := c.dynamicClient.Resource(ClusterIssuerGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrClusterIssuerNotReady, name, err)
	}
	conditions, _, _ := unstructured.NestedSlice(issuer.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, _ := raw.(map[string]any)
		if condition["type"] == "Ready" && condition["status"] == "True" {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrClusterIssuerNotReady, name)
}
