package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// denoRuntimeUID is the deno user the runtime image runs as. Named here so
	// runAsNonRoot can be checked: the image names its user, not its number.
	denoRuntimeUID int64 = 1000
	// denoRuntimeSecretName holds the per-project runtime secret, so the pod
	// spec, which anything allowed to list pods can read, carries a reference.
	denoRuntimeSecretName = "deno-runtime"
	denoRuntimeSecretKey  = "runtime-secret"
	// denoRuntimeSecretHashAnnotation rolls the runtime when its secret changes.
	denoRuntimeSecretHashAnnotation = "excalibase.io/runtime-secret-hash"
	runtimeSecretEnvName            = "RUNTIME_SECRET"
)

// denoSecurityContext runs a runtime container as the image's unprivileged
// user with no capabilities, no escalation and the default seccomp profile.
// Customer code runs in this container (EXC-642).
func denoSecurityContext(readOnlyRoot bool) *corev1.SecurityContext {
	noEscalation, nonRoot, uid := false, true, denoRuntimeUID
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noEscalation,
		ReadOnlyRootFilesystem:   &readOnlyRoot,
		RunAsNonRoot:             &nonRoot,
		RunAsUser:                &uid,
		RunAsGroup:               &uid,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}

func runtimeSecretEnv() corev1.EnvVar {
	return corev1.EnvVar{
		Name: runtimeSecretEnvName,
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: denoRuntimeSecretName},
			Key:                  denoRuntimeSecretKey,
		}},
	}
}

func runtimeSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:8])
}

// applyDenoRuntimeSecret creates the runtime's Secret, or rewrites it when
// the value differs; an identical one is left untouched.
func (c *Client) applyDenoRuntimeSecret(ctx context.Context, namespace, value string) error {
	secrets := c.clientset.CoreV1().Secrets(namespace)
	data := map[string][]byte{denoRuntimeSecretKey: []byte(value)}
	existing, err := secrets.Get(ctx, denoRuntimeSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: denoRuntimeSecretName, Namespace: namespace,
				Labels: map[string]string{"app": denoRuntimeName, "excalibase.io/component": "edgefn"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
		if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create deno runtime secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get deno runtime secret: %w", err)
	}
	if apiequality.Semantic.DeepEqual(existing.Data, data) {
		return nil
	}
	updated := existing.DeepCopy()
	updated.Data = data
	if _, err := secrets.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update deno runtime secret: %w", err)
	}
	return nil
}

// denoHardeningCurrent reports whether a live runtime already reads its secret
// by reference, runs unprivileged, and carries the current secret's hash.
func denoHardeningCurrent(dep *appsv1.Deployment, secret string) bool {
	container := dep.Spec.Template.Spec.Containers[0]
	if !apiequality.Semantic.DeepEqual(container.SecurityContext, denoSecurityContext(false)) {
		return false
	}
	if dep.Spec.Template.Annotations[denoRuntimeSecretHashAnnotation] != runtimeSecretHash(secret) {
		return false
	}
	for _, env := range container.Env {
		if env.Name == runtimeSecretEnvName {
			return apiequality.Semantic.DeepEqual(env, runtimeSecretEnv())
		}
	}
	return false
}

// hardenDeno brings a runtime's container, secret reference and hash up to date.
func hardenDeno(dep *appsv1.Deployment, secret string) {
	container := &dep.Spec.Template.Spec.Containers[0]
	container.SecurityContext = denoSecurityContext(false)
	replaced := false
	for i := range container.Env {
		if container.Env[i].Name == runtimeSecretEnvName {
			container.Env[i] = runtimeSecretEnv()
			replaced = true
		}
	}
	if !replaced {
		container.Env = append([]corev1.EnvVar{runtimeSecretEnv()}, container.Env...)
	}
	if dep.Spec.Template.Annotations == nil {
		dep.Spec.Template.Annotations = map[string]string{}
	}
	dep.Spec.Template.Annotations[denoRuntimeSecretHashAnnotation] = runtimeSecretHash(secret)
}
