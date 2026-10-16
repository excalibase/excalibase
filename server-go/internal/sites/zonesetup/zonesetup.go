// Package zonesetup prepares the user-content zone for site custom domains
// (EXC-614): the fallback origin record, the zone's fallback origin, and the
// catch-all Worker route that hands custom hostname traffic to the router.
// Each step checks first and writes only what is missing, so a second run
// changes nothing; it never replaces what another setup put there.
package zonesetup

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

const (
	// originlessAddress is the placeholder AAAA for a proxied name no origin answers: the Worker does.
	originlessAddress = "100::"
	// catchAllRoute matches every request entering the zone, custom hostnames included.
	catchAllRoute = "*/*"
)

var (
	hostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	scriptPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// ZoneAPI is the part of the zone API the setup reads and writes.
type ZoneAPI interface {
	DNSRecords(ctx context.Context, name string) ([]cfworkers.DNSRecord, error)
	CreateDNSRecord(ctx context.Context, record cfworkers.DNSRecord) (cfworkers.DNSRecord, error)
	FallbackOrigin(ctx context.Context) (cfworkers.FallbackOrigin, error)
	SetFallbackOrigin(ctx context.Context, origin string) (cfworkers.FallbackOrigin, error)
	WorkerRoutes(ctx context.Context) ([]cfworkers.WorkerRoute, error)
	CreateWorkerRoute(ctx context.Context, pattern, script string) (cfworkers.WorkerRoute, error)
}

// Plan is one install's zone: Origin is both the fallback origin and the CNAME target customers use.
type Plan struct {
	BaseDomain   string
	Origin       string
	RouterScript string
	// Apply writes; without it the run only reports what it would do.
	Apply bool
}

func (p Plan) validate() error {
	if !hostnamePattern.MatchString(p.Origin) || !strings.HasSuffix(p.Origin, "."+p.BaseDomain) {
		return fmt.Errorf("the origin %q must be a hostname under %q", p.Origin, p.BaseDomain)
	}
	if !scriptPattern.MatchString(p.RouterScript) {
		return fmt.Errorf("the router script %q is not a Worker name", p.RouterScript)
	}
	return nil
}

// Run checks each step in order and stops at the first that fails.
func Run(ctx context.Context, zone ZoneAPI, plan Plan, out io.Writer) error {
	if err := plan.validate(); err != nil {
		return err
	}
	steps := []func(context.Context, ZoneAPI, Plan, io.Writer) error{ensureOriginRecord, ensureFallbackOrigin, ensureCatchAllRoute}
	for _, step := range steps {
		if err := step(ctx, zone, plan, out); err != nil {
			return err
		}
	}
	if !plan.Apply {
		fmt.Fprintln(out, "dry run: nothing was changed; run again with --apply to make these changes")
	}
	return nil
}

func ensureOriginRecord(ctx context.Context, zone ZoneAPI, plan Plan, out io.Writer) error {
	records, err := zone.DNSRecords(ctx, plan.Origin)
	if err != nil {
		return fmt.Errorf("read the DNS records of %s: %w", plan.Origin, err)
	}
	if len(records) > 0 {
		for _, record := range records {
			if record.Type != "AAAA" || record.Content != originlessAddress || !record.Proxied {
				return fmt.Errorf("%s already has a %s record to %s (proxied=%v); remove or fix it by hand: it must be one proxied AAAA %s",
					plan.Origin, record.Type, record.Content, record.Proxied, originlessAddress)
			}
		}
		fmt.Fprintf(out, "DNS: %s already is a proxied AAAA %s\n", plan.Origin, originlessAddress)
		return nil
	}
	if !plan.Apply {
		fmt.Fprintf(out, "DNS: would create a proxied AAAA %s -> %s\n", plan.Origin, originlessAddress)
		return nil
	}
	record := cfworkers.DNSRecord{Type: "AAAA", Name: plan.Origin, Content: originlessAddress, Proxied: true}
	if _, err := zone.CreateDNSRecord(ctx, record); err != nil {
		return fmt.Errorf("create the DNS record of %s: %w", plan.Origin, err)
	}
	fmt.Fprintf(out, "DNS: created a proxied AAAA %s -> %s\n", plan.Origin, originlessAddress)
	return nil
}

func ensureFallbackOrigin(ctx context.Context, zone ZoneAPI, plan Plan, out io.Writer) error {
	current, err := zone.FallbackOrigin(ctx)
	if err != nil {
		return fmt.Errorf("read the zone's fallback origin: %w", err)
	}
	switch {
	case current.Origin == plan.Origin:
		fmt.Fprintf(out, "fallback origin: already %s (status %s)%s\n", plan.Origin, current.Status, originErrors(current))
		return nil
	case current.Origin != "":
		return fmt.Errorf("the zone's fallback origin is %s, not %s; change it by hand if that is intended", current.Origin, plan.Origin)
	case !plan.Apply:
		fmt.Fprintf(out, "fallback origin: would set the fallback origin to %s\n", plan.Origin)
		return nil
	}
	set, err := zone.SetFallbackOrigin(ctx, plan.Origin)
	if err != nil {
		return fmt.Errorf("set the zone's fallback origin (is Cloudflare for SaaS enabled on the zone?): %w", err)
	}
	fmt.Fprintf(out, "fallback origin: set to %s (status %s; wait for active)%s\n", plan.Origin, set.Status, originErrors(set))
	return nil
}

func originErrors(origin cfworkers.FallbackOrigin) string {
	if len(origin.Errors) == 0 {
		return ""
	}
	return "; Cloudflare says: " + strings.Join(origin.Errors, "; ")
}

func ensureCatchAllRoute(ctx context.Context, zone ZoneAPI, plan Plan, out io.Writer) error {
	routes, err := zone.WorkerRoutes(ctx)
	if err != nil {
		return fmt.Errorf("read the zone's Worker routes: %w", err)
	}
	for _, route := range routes {
		if route.Pattern != catchAllRoute {
			continue
		}
		if route.Script != plan.RouterScript {
			return fmt.Errorf("the route %s already runs %q, not %q; change it by hand if that is intended", catchAllRoute, route.Script, plan.RouterScript)
		}
		fmt.Fprintf(out, "Worker route: %s already runs %s\n", catchAllRoute, plan.RouterScript)
		return nil
	}
	if !plan.Apply {
		fmt.Fprintf(out, "Worker route: would route %s to %s\n", catchAllRoute, plan.RouterScript)
		return nil
	}
	if _, err := zone.CreateWorkerRoute(ctx, catchAllRoute, plan.RouterScript); err != nil {
		return fmt.Errorf("create the Worker route %s: %w", catchAllRoute, err)
	}
	fmt.Fprintf(out, "Worker route: created %s -> %s\n", catchAllRoute, plan.RouterScript)
	return nil
}
