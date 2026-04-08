package service

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/nats-io/nats.go"
)

const pgdogReloadSubject = "pgdog.config.reload"

// PgDogNotifier registers CNPG clusters with PgDog's config tables
// and publishes reload signals via NATS.
type PgDogNotifier struct {
	store storage.PgDogConfigStore
	nc    *nats.Conn
}

func NewPgDogNotifier(store storage.PgDogConfigStore, natsURL string) (*PgDogNotifier, error) {
	if natsURL == "" {
		return &PgDogNotifier{store: store}, nil
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &PgDogNotifier{store: store, nc: nc}, nil
}

// RegisterCluster adds a CNPG cluster's primary + replica to PgDog config.
func (n *PgDogNotifier) RegisterCluster(ctx context.Context, projectID, namespace, dbName, username, password string) error {
	if n.store == nil {
		return nil
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

	if err := n.store.RegisterPgDogUser(ctx, &domain.PgDogUser{
		Name: username, Database: projectID, Password: password,
	}); err != nil {
		return fmt.Errorf("register user: %w", err)
	}

	n.publishReload()
	return nil
}

// DeregisterCluster removes a CNPG cluster from PgDog config.
func (n *PgDogNotifier) DeregisterCluster(ctx context.Context, projectID, username string) error {
	if n.store == nil {
		return nil
	}

	if err := n.store.RemovePgDogUser(ctx, username, projectID); err != nil {
		log.Printf("WARN: pgdog remove user: %v", err)
	}
	if err := n.store.RemovePgDogDatabase(ctx, projectID); err != nil {
		log.Printf("WARN: pgdog remove database: %v", err)
	}

	n.publishReload()
	return nil
}

func (n *PgDogNotifier) publishReload() {
	if n.nc == nil {
		return
	}
	if err := n.nc.Publish(pgdogReloadSubject, []byte("reload")); err != nil {
		log.Printf("WARN: pgdog nats publish: %v", err)
	}
}

func (n *PgDogNotifier) Close() {
	if n.nc != nil {
		n.nc.Close()
	}
}
