package provisioner

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// The Mongo identity a DocumentDB project's gateway serves (EXC-409).
//
// The sidecar injector puts the gateway container in the project's Postgres
// pod and wires USERNAME and PASSWORD into it from a Secret in that project's
// namespace. The reference carries no "optional", so a pod whose Secret is
// missing does not start at all — which would take the tenant's Postgres down,
// not just their Mongo endpoint. The Secret is therefore written in the
// namespace stage, before the cluster is applied, and never afterwards.
//
// The password is generated here and read back by registration, which files it
// in the project's vault and creates the matching Mongo user. The Secret is
// the copy the gateway actually reads, so it is the one the other two are
// derived from rather than a third place the same secret is written.

// documentDBPasswordBytes is the entropy behind the generated password. It is
// a credential that reaches the public internet the moment a customer publishes
// their endpoint, and it is never typed by a human, so there is no reason for
// it to be short.
const documentDBPasswordBytes = 32

// ensureDocumentDBCredential writes the project's Mongo credential Secret.
// Projects without DocumentDB get nothing: there is no gateway to read it.
func ensureDocumentDBCredential(ctx context.Context, client k8s.KubeClient, namespace, projectID string, documentDB bool) error {
	if !documentDB {
		return nil
	}
	password, err := generateDocumentDBPassword()
	if err != nil {
		return fmt.Errorf("generate documentdb password: %w", err)
	}
	secret := map[string][]byte{
		"username": []byte(k8s.DocumentDBGatewayUsername),
		"password": []byte(password),
	}
	if err := client.CreateSecret(ctx, namespace, k8s.DocumentDBCredentialSecretName(projectID), secret); err != nil {
		return fmt.Errorf("create documentdb credential secret: %w", err)
	}
	return nil
}

// generateDocumentDBPassword returns a fresh password from the system CSPRNG.
// A failure is returned rather than swallowed: a predictable password on a port
// the customer may publish is worse than a provision that did not happen.
func generateDocumentDBPassword() (string, error) {
	raw := make([]byte, documentDBPasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	// URL-safe and unpadded, so the value survives a MongoDB connection URI
	// without percent-encoding and without a trailing "=" a reader would
	// mistake for part of a query string.
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
