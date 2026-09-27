package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// appRegistryLabel names the registry a pull secret authenticates to, hashed
// since a host with a port is not a valid label value.
const appRegistryLabel = "excalibase.io/registry"

const appPullSecretSuffix = "-pull"

// dockerHubLegacyKey is the key Docker Hub credentials are traditionally filed under.
const dockerHubLegacyKey = "https://index.docker.io/v1/"

// RegistryAuth is the credential the app's image is pulled with, read from the
// project's vault at deploy time and handed to nothing but the pull secret.
type RegistryAuth struct {
	Registry string
	Username string
	Password string
}

func AppPullSecretName(appName string) string { return AppObjectName(appName) + appPullSecretSuffix }

func registryLabelValue(registry string) string {
	sum := sha256.Sum256([]byte(registry))
	return hex.EncodeToString(sum[:16])
}

type dockerAuthEntry struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Auth     string `json:"auth"`
}

// buildAppPullSecret refuses a credential for any registry but the image's own,
// so a credential is never offered to a host the customer did not pull from.
func buildAppPullSecret(namespace string, app *apphost.App, auth *RegistryAuth) (*corev1.Secret, error) {
	if auth == nil {
		return nil, nil
	}
	if registry := apphost.ImageRegistry(app.Image); registry != auth.Registry {
		return nil, fmt.Errorf("%w: a credential for %s cannot pull an image from %s", ErrRenderApp, auth.Registry, registry)
	}
	entry := dockerAuthEntry{
		Username: auth.Username, Password: auth.Password,
		Auth: base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password)),
	}
	auths := map[string]dockerAuthEntry{auth.Registry: entry}
	if auth.Registry == "docker.io" {
		auths[dockerHubLegacyKey] = entry
	}
	config, err := json.Marshal(map[string]any{"auths": auths})
	if err != nil {
		return nil, fmt.Errorf("%w: encode the pull credential: %w", ErrRenderApp, err)
	}
	labels := appLabels(app)
	labels[appRegistryLabel] = registryLabelValue(auth.Registry)
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: AppPullSecretName(app.Name), Namespace: namespace, Labels: labels},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: config},
	}, nil
}

// DeleteRegistryPullSecrets removes every pull secret rendered from one
// registry's credential, so a credential the project removed stops being used.
func (c *Client) DeleteRegistryPullSecrets(ctx context.Context, namespace, registry string) error {
	secrets := c.clientset.CoreV1().Secrets(namespace)
	list, err := secrets.List(ctx, metav1.ListOptions{
		LabelSelector: appRegistryLabel + "=" + registryLabelValue(registry) + ",app.kubernetes.io/managed-by=" + appManagedByValue,
	})
	if err != nil {
		return fmt.Errorf("list pull secrets: %w", err)
	}
	for _, secret := range list.Items {
		if err := secrets.Delete(ctx, secret.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete pull secret %s: %w", secret.Name, err)
		}
	}
	return nil
}
