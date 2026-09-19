package service

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/excalibase/provisioning-poc/internal/natsauth"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/nats-io/nats.go"
)

// pgdogReloadSubject must stay in step with natsauth.SubjectPgDogReload,
// which is what the svc-pgdog principal is allowed to subscribe to.
const pgdogReloadSubject = natsauth.SubjectPgDogReload

// pgdogPublisherName labels this publisher's bus metrics.
const pgdogPublisherName = "pgdog-notifier"

// ErrPgDogRoleNotRoutable is returned when a caller tries to expose a role
// through the shared pooler that is not one of the engine-facing roles.
var ErrPgDogRoleNotRoutable = errors.New("pgdog: role is not routable through the pooler")

// pgdogRoutableRoles are the only roles provisioning ever registers with
// PgDog. The CNPG owner ("app"), the docker-mode superuser and cdc_watcher
// (REPLICATION cannot cross a transaction pooler) are deliberately absent:
// a pgdog_users row is exactly the set of credentials that can reach a
// tenant through the single shared PgDog address.
var pgdogRoutableRoles = map[string]bool{
	"excalibase_app": true,
	"auth_admin":     true,
}

// PgDogRole is one (username, password) pair to expose for a project.
type PgDogRole struct {
	Name     string
	Password string
}

// PgDogNotifier registers CNPG clusters with PgDog's config tables
// and publishes reload signals via NATS.
type PgDogNotifier struct {
	store storage.PgDogConfigStore
	nc    *nats.Conn
}

// NewPgDogNotifier dials NATS. opts carries the svc-provisioning credential
// and inbox prefix (see natsauth.ClientOptions).
func NewPgDogNotifier(store storage.PgDogConfigStore, natsURL string, opts ...nats.Option) (*PgDogNotifier, error) {
	if natsURL == "" {
		return &PgDogNotifier{store: store}, nil
	}
	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &PgDogNotifier{store: store, nc: nc}, nil
}

// RegisterCluster adds a CNPG cluster's primary + replica to PgDog config and
// scopes each engine role to the project's logical database. Every role is
// validated before anything is written so a rejected set leaves no route.
func (n *PgDogNotifier) RegisterCluster(ctx context.Context, projectID, namespace, dbName string, roles []PgDogRole) error {
	if n.store == nil {
		return nil
	}
	if err := validatePgDogRoles(roles); err != nil {
		return err
	}

	rwHost := fmt.Sprintf("%s-postgres-rw.%s.svc.cluster.local", projectID, namespace)
	roHost := fmt.Sprintf("%s-postgres-ro.%s.svc.cluster.local", projectID, namespace)

	if err := n.store.RegisterPgDogDatabase(ctx, &domain.PgDogDatabase{
		Name: projectID, Host: rwHost, Port: 5432,
		DatabaseName: dbName, Role: "primary", Shard: 0,
	}); err != nil {
		return fmt.Errorf("register primary: %w", err)
	}

	if err := n.store.RegisterPgDogDatabase(ctx, &domain.PgDogDatabase{
		Name: projectID, Host: roHost, Port: 5432,
		DatabaseName: dbName, Role: "replica", Shard: 0, ReadOnly: true,
	}); err != nil {
		return fmt.Errorf("register replica: %w", err)
	}

	for _, role := range roles {
		if err := n.store.RegisterPgDogUser(ctx, &domain.PgDogUser{
			Name: role.Name, Database: projectID, Password: role.Password,
		}); err != nil {
			return fmt.Errorf("register user %s: %w", role.Name, err)
		}
	}

	n.publishReload()
	return nil
}

func validatePgDogRoles(roles []PgDogRole) error {
	if len(roles) == 0 {
		return fmt.Errorf("%w: no roles given", ErrPgDogRoleNotRoutable)
	}
	for _, role := range roles {
		if !pgdogRoutableRoles[role.Name] {
			return fmt.Errorf("%w: %q", ErrPgDogRoleNotRoutable, role.Name)
		}
	}
	return nil
}

// DeregisterCluster removes a project's routes and every user bound to them.
func (n *PgDogNotifier) DeregisterCluster(ctx context.Context, projectID string) error {
	if n.store == nil {
		return nil
	}

	if err := n.store.RemovePgDogUsers(ctx, projectID); err != nil {
		log.Printf("WARN: pgdog remove users: %v", err)
	}
	if err := n.store.RemovePgDogDatabase(ctx, projectID); err != nil {
		log.Printf("WARN: pgdog remove database: %v", err)
	}

	n.publishReload()
	return nil
}

// Connected reports whether a reload signal published right now would reach
// PgDog.
func (n *PgDogNotifier) Connected() bool {
	return n != nil && n.nc != nil && n.nc.IsConnected()
}

// publishReload signals PgDog to re-read its config. A signal that cannot be
// sent is counted, not swallowed: PgDog would otherwise keep routing on a
// stale config with nothing to show for it.
func (n *PgDogNotifier) publishReload() {
	if n.nc == nil {
		return
	}
	if !n.Connected() {
		metrics.CountNatsPublishDropped(pgdogPublisherName)
		log.Print("WARN: pgdog reload dropped: nats bus not connected")
		return
	}
	if err := n.nc.Publish(pgdogReloadSubject, []byte("reload")); err != nil {
		metrics.CountNatsPublishDropped(pgdogPublisherName)
		log.Printf("WARN: pgdog nats publish: %v", err)
	}
}

func (n *PgDogNotifier) Close() {
	if n.nc != nil {
		n.nc.Close()
	}
}
