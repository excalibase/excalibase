package main

import (
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

// trustBackfillTimeout bounds the boot guard's read, so a database that never
// answers stops the boot instead of hanging it. A variable so a test can shorten it.
var trustBackfillTimeout = 30 * time.Second

// trustBackfillCheck is the boot guard of the limited trial (EXC-899): the flag
// may be on only after the grandfather backfill has recorded its marker, with
// the cutoff that is configured now.
func trustBackfillCheck(cfg config.AppConfig, platform any) error {
	ctx, cancel := context.WithTimeout(context.Background(), trustBackfillTimeout)
	defer cancel()
	markers, _ := platform.(trust.BackfillMarkers)
	return trust.RequireBackfill(ctx, cfg.Features(), markers, cfg.Trust.GrandfatherBefore)
}
