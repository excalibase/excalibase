package main

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

// trustBackfillCheck is the boot guard of the limited trial (EXC-899): the flag
// may be on only after the grandfather backfill has recorded its marker.
func trustBackfillCheck(cfg config.AppConfig, platform any) error {
	markers, _ := platform.(trust.BackfillMarkers)
	return trust.RequireBackfill(context.Background(), cfg.Features(), markers)
}
