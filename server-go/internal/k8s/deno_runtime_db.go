package k8s

import (
	"bytes"
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// denoDBSecretName holds the project database URL ctx.db opens (EXC-625).
	denoDBSecretName = "deno-runtime-db"
	dbURLEnvName     = "EXCALIBASE_DB_URL"
	// dbURLHashAnnotation fingerprints the URL on the pod template: the runtime
	// reads its environment once, so a rotated login must roll the pod.
	dbURLHashAnnotation = "excalibase.io/db-url-hash"
)

// applyDenoDBSecret files the URL in the namespace's runtime Secret, creating
// it or rewriting a differing one. An empty URL leaves whatever is there.
func (c *Client) applyDenoDBSecret(ctx context.Context, namespace, url string) error {
	if url == "" {
		return nil
	}
	secrets := c.clientset.CoreV1().Secrets(namespace)
	existing, err := secrets.Get(ctx, denoDBSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := secrets.Create(ctx, buildDenoDBSecret(namespace, url), metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create runtime database secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get runtime database secret: %w", err)
	}
	if bytes.Equal(existing.Data[dbURLEnvName], []byte(url)) {
		return nil
	}
	updated := existing.DeepCopy()
	updated.Data = map[string][]byte{dbURLEnvName: []byte(url)}
	if _, err := secrets.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update runtime database secret: %w", err)
	}
	return nil
}

func buildDenoDBSecret(namespace, url string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      denoDBSecretName,
			Namespace: namespace,
			Labels:    map[string]string{"excalibase.io/component": "edgefn"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{dbURLEnvName: []byte(url)},
	}
}

func denoDBEnv() corev1.EnvVar {
	return corev1.EnvVar{Name: dbURLEnvName, ValueFrom: &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: denoDBSecretName},
			Key:                  dbURLEnvName,
		},
	}}
}

// hasDBEnv reports whether the runtime already reads this URL: the reference
// is in place and the pod was started with the URL's fingerprint.
func hasDBEnv(dep *appsv1.Deployment, url string) bool {
	if url == "" {
		return true
	}
	if dep.Spec.Template.Annotations[dbURLHashAnnotation] != egressHash(url) {
		return false
	}
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == dbURLEnvName {
			return env.Value == "" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil &&
				env.ValueFrom.SecretKeyRef.Name == denoDBSecretName
		}
	}
	return false
}

// setDBEnv points the runtime at the Secret and stamps the URL's fingerprint.
func setDBEnv(dep *appsv1.Deployment, url string) {
	if url == "" {
		return
	}
	setEnv(dep, []corev1.EnvVar{denoDBEnv()})
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations[dbURLHashAnnotation] = egressHash(url)
}
