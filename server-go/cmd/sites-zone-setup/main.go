// Command sites-zone-setup prepares the user-content zone for site custom
// domains (EXC-614, ADR 0043). It is a dry run unless --apply is given, and
// running it again changes nothing that is already in place.
//
//	CLOUDFLARE_API_TOKEN=... go run ./cmd/sites-zone-setup \
//	  --zone-id <32-hex zone id> --base-domain excalibase.app [--origin cname.excalibase.app] \
//	  [--router-script excalibase-sites-router] [--apply]
//
// The token needs Zone → SSL and Certificates: Edit, DNS: Edit and Workers
// Routes: Edit on the zone. It is read from the environment and never printed.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/sites/zonesetup"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sites-zone-setup:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	flags := flag.NewFlagSet("sites-zone-setup", flag.ContinueOnError)
	zoneID := flags.String("zone-id", getenv("SITES_ZONE_ID"), "the user-content zone's 32-hex id")
	baseDomain := flags.String("base-domain", getenv("SITES_BASE_DOMAIN"), "the user-content domain, e.g. excalibase.app")
	origin := flags.String("origin", getenv("SITES_CUSTOM_DOMAIN_TARGET"), "fallback origin and CNAME target; default cname.<base domain>")
	script := flags.String("router-script", "excalibase-sites-router", "the sites router Worker")
	apply := flags.Bool("apply", false, "make the changes; without it nothing is written")
	if err := flags.Parse(args); err != nil {
		return err
	}
	token := getenv("CLOUDFLARE_API_TOKEN")
	if token == "" {
		token = getenv("SITES_CLOUDFLARE_API_TOKEN")
	}
	if token == "" {
		return fmt.Errorf("set CLOUDFLARE_API_TOKEN")
	}
	settings := config.SitesConfig{BaseDomain: *baseDomain, CustomTarget: *origin}
	zone, err := cfworkers.NewZoneClient(cfworkers.ZoneConfig{APIToken: token, ZoneID: *zoneID})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	plan := zonesetup.Plan{BaseDomain: *baseDomain, Origin: settings.CustomDomainTarget(), RouterScript: *script, Apply: *apply}
	if err := zonesetup.Run(ctx, zone, plan, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "next: customers point a CNAME at %s; set SITES_ZONE_ID on the server to offer custom domains\n", plan.Origin)
	return nil
}
