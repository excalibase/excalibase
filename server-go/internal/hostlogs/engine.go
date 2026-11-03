package hostlogs

import (
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/enginestats"
)

type engineClient struct{ client *enginestats.Client }

// EngineFrom is the engine proxy client as the shipper uses it.
func EngineFrom(client *enginestats.Client) Engine { return engineClient{client: client} }

func (e engineClient) Managed(ctx context.Context) ([]enginestats.Container, error) {
	return e.client.Managed(ctx)
}

func (e engineClient) FollowLogs(ctx context.Context, ref string, since time.Time) (Lines, error) {
	stream, err := e.client.FollowLogs(ctx, ref, since)
	if err != nil {
		return nil, err
	}
	return stream, nil
}
