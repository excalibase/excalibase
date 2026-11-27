package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

// trustBackfillCLI handles `trust-backfill`: the one-off before the limited
// trial flag is switched on (EXC-899). It reads the cutoff from TRUST_GRANDFATHER_BEFORE.
func trustBackfillCLI() {
	fs := flag.NewFlagSet("trust-backfill", flag.ExitOnError)
	dbURL := fs.String("db", "", "PostgreSQL connection string (default: from PLATFORM_DB_URL env)")
	_ = fs.Parse(os.Args[2:])
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	if *dbURL == "" {
		*dbURL = cfg.PlatformDBURL
	}
	sqlStore := openPlatformStoreCLI(*dbURL)
	defer sqlStore.Close()
	if err := runTrustBackfill(context.Background(), sqlStore, cfg.Trust.GrandfatherBefore, time.Now().UTC(), os.Stdout); err != nil {
		log.Fatalf("trust backfill: %v", err)
	}
}

// runTrustBackfill grandfathers the accounts before cutoff and reports what it did.
func runTrustBackfill(ctx context.Context, store trust.Grandfatherer, cutoff, now time.Time, out io.Writer) error {
	granted, err := trust.RunBackfill(ctx, store, cutoff, now)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Grandfathered %d accounts created before %s as full.\nAccounts created on or after that date must verify before they run containers.\n",
		granted, cutoff.UTC().Format("2006-01-02"))
	return err
}
