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
	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/nats-io/nats.go"
)

// policyPublisherName labels this publisher's bus metrics.
const policyPublisherName = "policy-change-publisher"

// PolicyChangePublisher publishes RLS / CLS policy mutation events to NATS.
type PolicyChangePublisher struct {
	nc *nats.Conn
}

// NewPolicyChangePublisher dials NATS. A blank natsURL yields a no-op
// publisher — useful for local dev / unit tests. opts carries the
// svc-provisioning credential, inbox prefix and the shared resilience
// options (see natsauth.ClientOptions).
//
// With those options the dial does not block on a bus that is down: the
// publisher is constructed, reports NotConnected, and starts delivering once
// the bus is back.
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

// Connected reports whether events published right now would reach the bus.
func (p *PolicyChangePublisher) Connected() bool {
	return p != nil && p.nc != nil && p.nc.IsConnected()
}

// PublishPolicyChange satisfies handler.PolicyChangePublisher. The interface
// returns nothing — the policy write has already committed — so a dropped
// event is surfaced on excalibase_nats_publish_dropped_total rather than
// disappearing into a log line nobody alerts on.
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
	if !p.Connected() {
		metrics.CountNatsPublishDropped(policyPublisherName)
		log.Printf("WARN: policy change dropped (%s): nats bus not connected", subject)
		return
	}
	if err := p.nc.Publish(subject, payload); err != nil {
		metrics.CountNatsPublishDropped(policyPublisherName)
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
