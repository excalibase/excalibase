package natsauth

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// ClientOptions builds the dial options a platform service uses to reach the
// bus as a named principal: its credential plus the inbox prefix the
// permission matrix grants it. Without the matching prefix the connection
// authenticates but every request/reply times out, because replies land on
// a subject the principal may not subscribe to.
//
// A blank password yields no options at all, which keeps unauthenticated
// local/dev NATS working unchanged.
func ClientOptions(principal, password, cdcStream string) ([]nats.Option, error) {
	if password == "" {
		return nil, nil
	}
	perms, err := PermissionsFor(principal, cdcStream)
	if err != nil {
		return nil, fmt.Errorf("nats client options: %w", err)
	}
	opts := []nats.Option{nats.UserInfo(principal, password)}
	if perms.InboxPrefix != "" {
		opts = append(opts, nats.CustomInboxPrefix(perms.InboxPrefix))
	}
	return opts, nil
}
