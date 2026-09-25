package provisioner

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// The Secret the DocumentDB gateway's environment requires (EXC-409).
//
// The sidecar injector wires USERNAME and PASSWORD into the gateway container
// from a Secret in the project's namespace, with no "optional" on the
// reference — so a pod whose Secret is missing does not start at all, and that
// would take the tenant's Postgres down rather than merely their Mongo
// endpoint. The Secret therefore has to exist, and has to exist before the
// cluster is applied.
//
// It is deliberately empty. The gateway's entrypoint creates an admin user on
// start-up only when both variables are non-empty:
//
//	if [ "$CREATE_USER" = "true" ] && [ -n "$USERNAME" ] && [ -n "$PASSWORD" ]
//
// Empty values skip that, which is what this platform wants: a DocumentDB
// project has one credential, its own application role, created and rotated
// through the ordinary path and filed once in the project's vault. Letting the
// gateway mint a second identity of its own would give a customer two
// credentials to keep in step, and would put a password in a Kubernetes Secret
// that nothing else could rotate.

// ensureDocumentDBCredential writes the empty Secret the gateway's environment
// references. Projects without DocumentDB get nothing: they have no gateway.
func ensureDocumentDBCredential(ctx context.Context, client k8s.KubeClient, namespace, projectID string, documentDB bool) error {
	if !documentDB {
		return nil
	}
	// Present and empty, not absent: the container's env reference must
	// resolve, and the values must not name a user the gateway would create.
	secret := map[string][]byte{
		"username": []byte(""),
		"password": []byte(""),
	}
	if err := client.CreateSecret(ctx, namespace, k8s.DocumentDBCredentialSecretName(projectID), secret); err != nil {
		return fmt.Errorf("create documentdb credential secret: %w", err)
	}
	return nil
}

// ensureDocumentDBService runs after the pods are ready, because the selector
// is copied from the operator's read-write Service.
func ensureDocumentDBService(ctx context.Context, client k8s.KubeClient, namespace, projectID string, documentDB bool) error {
	if !documentDB {
		return nil
	}
	if err := client.EnsureDocumentDBService(ctx, namespace, projectID); err != nil {
		return fmt.Errorf("create documentdb gateway service: %w", err)
	}
	return nil
}
