// Package service — NATS publisher for RLS / CLS policy changes.
//
// Excalibase-graphql subscribes to "policies.{projectId}.changed" to
// invalidate its in-process PolicyCache when an operator modifies
// policies via the provisioning REST endpoints. The publisher matches
// the shape of [PgDogNotifier] — nil-tolerant, lazy, never blocks the
// caller on NATS errors.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/nats-io/nats.go"
)

// PolicyChangePublisher publishes RLS / CLS policy mutation events to NATS.
type PolicyChangePublisher struct {
	nc *nats.Conn
}

// NewPolicyChangePublisher dials NATS. A blank natsURL yields a no-op
// publisher — useful for local dev / unit tests. opts carries the
// svc-provisioning credential and inbox prefix (see natsauth.ClientOptions).
func NewPolicyChangePublisher(natsURL string, opts ...nats.Option) (*PolicyChangePublisher, error) {
	if natsURL == "" {
		return &PolicyChangePublisher{}, nil
	}
	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &PolicyChangePublisher{nc: nc}, nil
}

// PublishPolicyChange satisfies handler.PolicyChangePublisher. Failures
// are logged but never returned — the policy write already committed
// and the consumer's TTL cache will eventually catch up.
func (p *PolicyChangePublisher) PublishPolicyChange(_ context.Context, evt domain.PolicyChangeEvent) {
	if p == nil || p.nc == nil {
		return
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		log.Printf("WARN: policy change marshal: %v", err)
		return
	}
	subject := fmt.Sprintf("policies.%s.changed", evt.ProjectID)
	if err := p.nc.Publish(subject, payload); err != nil {
		log.Printf("WARN: policy change publish (%s): %v", subject, err)
	}
}

// Close releases the NATS connection. Safe to call when the connection
// was never established (blank NATS URL path).
func (p *PolicyChangePublisher) Close() {
	if p != nil && p.nc != nil {
		p.nc.Close()
	}
}
