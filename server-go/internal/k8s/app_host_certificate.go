package k8s

import (
	"context"
	"errors"
	"fmt"
	"maps"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const (
	// hostTLSLabel marks an app's own-hostname Certificate: pending until its
	// Ingress serves it, so the sweep lists only what it still has to attach.
	hostTLSLabel   = "excalibase.io/host-tls"
	hostTLSPending = "pending"
	hostTLSServed  = "served"

	haproxySSLRedirect = "haproxy.org/ssl-redirect"
)

// httpsRedirectAnnotations send plain HTTP to HTTPS with 308, so a POST keeps
// its method and body. The controller's own redirect is off: it answers 301 and
// names the port it listens on inside its pod (8443), which forced an explicit
// :443 into every Location. The snippet redirects to the host the client asked
// for, with no port, on the backend that serves it.
var httpsRedirectAnnotations = map[string]string{
	haproxySSLRedirect:                   "false",
	"haproxy.org/backend-config-snippet": `http-request redirect location https://%[req.hdr(host),field(1,:)]%[pathq] code 308 unless { ssl_fc }`,
}

// hstsAnnotations make browsers keep to HTTPS on an app's platform hostname for a
// year. No preload: that is a commitment for the whole registered domain.
var hstsAnnotations = map[string]string{
	"haproxy.org/response-set-header": `Strict-Transport-Security "max-age=31536000; includeSubDomains"`,
}

// ErrNoCertificate: the app's hostname has no certificate (not deployed, internal, or no issuer).
var ErrNoCertificate = errors.New("the app's hostname has no certificate")

// AppHostTLSSecretName holds the key and certificate of the app's own hostname.
func AppHostTLSSecretName(appName string) string { return AppObjectName(appName) + "-tls" }

// buildAppHostCertificate is named after the app's Ingress, so the sweep finds the route it belongs to.
func buildAppHostCertificate(namespace string, app *apphost.App, host, issuer string) *unstructured.Unstructured {
	labels := appLabels(app)
	labels[hostTLSLabel] = hostTLSPending
	return buildCertificate(namespace, AppObjectName(app.Name), host, issuer, labels, appLabels(app))
}

// serveTLS attaches an issued certificate and redirects HTTP to it.
func serveTLS(ingress *networkingv1.Ingress, host, secretName string) {
	ingress.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{host}, SecretName: secretName}}
	addAnnotations(ingress, httpsRedirectAnnotations)
}

// serveAppHostTLS is serveTLS for the app's platform hostname, which also gets HSTS.
func serveAppHostTLS(ingress *networkingv1.Ingress, host, secretName string) {
	serveTLS(ingress, host, secretName)
	addAnnotations(ingress, hstsAnnotations)
}

// serveWildcardTLS redirects and pins HTTPS for a host the edge's wildcard
// certificate already serves: there is no per-host secret to name.
func serveWildcardTLS(ingress *networkingv1.Ingress) {
	addAnnotations(ingress, httpsRedirectAnnotations)
	addAnnotations(ingress, hstsAnnotations)
}

func addAnnotations(ingress *networkingv1.Ingress, annotations map[string]string) {
	if ingress.Annotations == nil {
		ingress.Annotations = map[string]string{}
	}
	maps.Copy(ingress.Annotations, annotations)
}

// withRedirectAnnotationsOf keeps existing's other annotations and takes the redirect ones from desired.
func withRedirectAnnotationsOf(existing, desired map[string]string) map[string]string {
	merged := maps.Clone(existing)
	if merged == nil {
		merged = map[string]string{}
	}
	for _, managed := range []map[string]string{httpsRedirectAnnotations, hstsAnnotations} {
		for key := range managed {
			delete(merged, key)
			if value, ok := desired[key]; ok {
				merged[key] = value
			}
		}
	}
	return merged
}

// applyAppHostRoute serves the hostname over HTTPS only once its key exists:
// before that a redirect would send the HTTP-01 challenge to HTTPS. The
// Certificate is written after the Ingress, so a pending label always follows
// an Ingress still without TLS and the sweep attaches it later.
func (c *Client) applyAppHostRoute(ctx context.Context, namespace string, workload *AppWorkload) error {
	ingress := workload.Ingress
	if workload.HostCertificate == nil {
		if err := c.applyAppIngress(ctx, namespace, ingress); err != nil {
			return err
		}
		if workload.dropHostCertificate {
			return c.dropAppHostCertificate(ctx, namespace, ingress.Name)
		}
		return nil
	}
	if err := c.applyCiliumPolicy(ctx, namespace, workload.AcmeSolverPolicy, "ACME solver policy"); err != nil {
		return err
	}
	secretName, _, _ := unstructured.NestedString(workload.HostCertificate.Object, "spec", "secretName")
	_, err := c.clientset.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("read app certificate secret: %w", err)
	}
	issued := err == nil
	cert := workload.HostCertificate.DeepCopy()
	if issued {
		ingress = ingress.DeepCopy()
		serveAppHostTLS(ingress, ingress.Spec.Rules[0].Host, secretName)
		labels := cert.GetLabels()
		labels[hostTLSLabel] = hostTLSServed
		cert.SetLabels(labels)
	}
	if err := c.applyAppIngress(ctx, namespace, ingress); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		return c.applyUnstructured(ctx, CertificateGVR, namespace, cert, "app certificate")
	})
}

// dropAppHostCertificate removes the certificate named after the app's
// objects before its key, so cert-manager does not issue it again.
func (c *Client) dropAppHostCertificate(ctx context.Context, namespace, objectName string) error {
	err := c.dynamicClient.Resource(CertificateGVR).Namespace(namespace).Delete(ctx, objectName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete app certificate: %w", err)
	}
	err = c.clientset.CoreV1().Secrets(namespace).Delete(ctx, objectName+"-tls", metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete app certificate secret: %w", err)
	}
	return nil
}

// AttachIssuedAppHostCertificates serves every app hostname whose certificate
// has been issued since its Ingress was written. One cluster-wide list per
// sweep, of pending certificates only.
func (c *Client) AttachIssuedAppHostCertificates(ctx context.Context) error {
	certs := c.dynamicClient.Resource(CertificateGVR).Namespace(metav1.NamespaceAll)
	selector := hostTLSLabel + "=" + hostTLSPending + ",app.kubernetes.io/managed-by=" + appManagedByValue
	list, err := certs.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list app certificates: %w", err)
	}
	var errs []error
	for i := range list.Items {
		cert := &list.Items[i]
		if !certificateState(cert).Ready {
			continue
		}
		if err := c.serveIssuedHostCertificate(ctx, cert); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", cert.GetNamespace(), cert.GetName(), err))
		}
	}
	return errors.Join(errs...)
}

// serveIssuedHostCertificate changes only the Ingress the same app rendered for
// the certificate's host; a conflicting write by a deploy is retried next sweep.
func (c *Client) serveIssuedHostCertificate(ctx context.Context, cert *unstructured.Unstructured) error {
	namespace := cert.GetNamespace()
	ingresses := c.clientset.NetworkingV1().Ingresses(namespace)
	ingress, err := ingresses.Get(ctx, cert.GetName(), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read app ingress: %w", err)
	}
	hosts, _, _ := unstructured.NestedStringSlice(cert.Object, "spec", "dnsNames")
	secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
	if !sameAppOwner(ingress.Labels, cert.GetLabels()) || len(hosts) != 1 ||
		len(ingress.Spec.Rules) != 1 || ingress.Spec.Rules[0].Host != hosts[0] {
		return nil
	}
	serveAppHostTLS(ingress, hosts[0], secretName)
	if _, err := ingresses.Update(ctx, ingress, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("serve app certificate: %w", err)
	}
	served := cert.DeepCopy()
	labels := served.GetLabels()
	labels[hostTLSLabel] = hostTLSServed
	served.SetLabels(labels)
	if _, err := c.dynamicClient.Resource(CertificateGVR).Namespace(namespace).Update(ctx, served, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("mark app certificate served: %w", err)
	}
	return nil
}

// AppHostCertificate reports whether the app's own hostname has its certificate, or why issuing failed.
func (c *Client) AppHostCertificate(ctx context.Context, namespace, appName string) (CertificateState, error) {
	cert, err := c.dynamicClient.Resource(CertificateGVR).Namespace(namespace).Get(ctx, AppObjectName(appName), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return CertificateState{}, ErrNoCertificate
	}
	if err != nil {
		return CertificateState{}, fmt.Errorf("read app certificate: %w", err)
	}
	return certificateState(cert), nil
}
