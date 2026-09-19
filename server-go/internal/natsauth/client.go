package natsauth

import (
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/nats-io/nats.go"
)

// reconnectWait is the pause between reconnect attempts. It is long enough
// not to add to a reconnect storm the auth callout is already working
// through, and short enough that a service is back on the bus within a few
// seconds of the server returning.
const reconnectWait = 2 * time.Second

// BaseOptions are the dial options every platform client shares, whatever
// principal it connects as.
//
// The defaults are wrong for this platform in three ways, and each one has
// cost us delivery: without RetryOnFailedConnect a client that starts before
// the bus fails construction outright; with a finite MaxReconnects it gives
// up on a bus that is merely restarting; and nats.go stops reconnecting
// after two consecutive authorization errors, so a single late auth callout
// takes a service off the bus for the life of the process unless
// IgnoreAuthErrorAbort is set.
//
// The handlers are not decoration: a connection that is silently down looks
// exactly like an idle one, so every state change is logged and reflected in
// the excalibase_nats_connected gauge.
func BaseOptions(client string) []nats.Option {
	return []nats.Option{
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(reconnectWait),
		nats.IgnoreAuthErrorAbort(),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			metrics.SetNatsConnected(client, false)
			log.Printf("WARN: nats %s disconnected: %v", client, err)
		}),
		nats.ReconnectHandler(func(conn *nats.Conn) {
			metrics.SetNatsConnected(client, true)
			log.Printf("nats %s reconnected to %s", client, conn.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			metrics.SetNatsConnected(client, false)
			log.Printf("nats %s connection closed", client)
		}),
		nats.ConnectHandler(func(conn *nats.Conn) {
			metrics.SetNatsConnected(client, true)
			log.Printf("nats %s connected to %s", client, conn.ConnectedUrl())
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}
			log.Printf("WARN: nats %s error on %q: %v", client, subject, err)
		}),
	}
}

// ClientOptions builds the dial options a platform service uses to reach the
// bus as a named principal: the shared resilience options, its credential,
// and the inbox prefix the permission matrix grants it. Without the matching
// prefix the connection authenticates but every request/reply times out,
// because replies land on a subject the principal may not subscribe to.
//
// A blank password yields the shared options alone, which keeps an
// unauthenticated local/dev NATS working — resilience is not something only
// authenticated deployments need.
func ClientOptions(principal, password, cdcStream string) ([]nats.Option, error) {
	perms, err := PermissionsFor(principal, cdcStream)
	if err != nil {
		return nil, fmt.Errorf("nats client options: %w", err)
	}
	opts := BaseOptions(principal)
	if password == "" {
		return opts, nil
	}
	opts = append(opts, nats.UserInfo(principal, password))
	if perms.InboxPrefix != "" {
		opts = append(opts, nats.CustomInboxPrefix(perms.InboxPrefix))
	}
	return opts, nil
}
