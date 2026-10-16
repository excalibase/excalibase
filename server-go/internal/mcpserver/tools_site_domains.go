package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const customDomainsUnavailableMessage = "custom domains are not set up on this install; the site keeps serving at its own hostname (list_sites)"

type siteDomainArgs struct {
	siteArgs
	DomainID string `json:"domain_id" jsonschema:"the custom domain's id (list_site_domains or add_site_domain)"`
}

type addSiteDomainArgs struct {
	siteArgs
	Hostname   string `json:"hostname" jsonschema:"the user's own hostname, such as www.example.com; not a bare registered domain, an address, or a platform domain"`
	Validation string `json:"validation,omitempty" jsonschema:"how the certificate is validated: http (default; nothing to add beyond the CNAME) or txt (TXT records, so the certificate is issued before the CNAME moves)"`
}

func siteDomainTools() []entry {
	return []entry{
		tool("list_site_domains", "List a site's custom domains with their status (PENDING, ACTIVE or FAILED) and the DNS records each needs.", readTool, listSiteDomains),
		tool("add_site_domain", "Add the user's own domain to a site, such as www.example.com (a subdomain: a bare example.com cannot point a CNAME). "+
			"The answer lists the DNS records the user adds at their DNS provider: always a CNAME to the platform's target, plus TXT records with txt validation. "+
			"Cloudflare issues and renews the certificate. The domain serves once it is active: call check_site_domain after the records are in place.", writeTool, addSiteDomain),
		tool("check_site_domain", "Check a custom domain now and route it once it and its certificate are active; while pending, this asks Cloudflare to validate again. "+
			"DNS changes can take minutes to hours to be seen.", writeTool, checkSiteDomain),
		tool("remove_site_domain", "Remove a custom domain from a site: it stops serving at once and its certificate is revoked.", writeTool, removeSiteDomain),
	}
}

func siteDomainPath(c *call, in siteDomainArgs) (string, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return "", err
	}
	domainID, err := segment("domain_id", in.DomainID)
	if err != nil {
		return "", err
	}
	return path + "/domains/" + domainID, nil
}

// domainFailure tells an install without custom domains from one without Sites at all.
func domainFailure(err error) error {
	if isStatus(err, http.StatusServiceUnavailable) && strings.Contains(err.Error(), "custom domains") {
		return errors.New(customDomainsUnavailableMessage)
	}
	return siteFailure(err)
}

func listSiteDomains(ctx context.Context, c *call, in siteArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	var domains []map[string]any
	if err := c.get(ctx, path+"/domains", nil, &domains); err != nil {
		return nil, domainFailure(err)
	}
	if domains == nil {
		domains = []map[string]any{}
	}
	return map[string]any{"domains": domains}, nil
}

func addSiteDomain(ctx context.Context, c *call, in addSiteDomainArgs) (any, error) {
	path, err := sitePath(c, in.ProjectID, in.SiteID)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"hostname": in.Hostname}
	if in.Validation != "" {
		body["validation"] = in.Validation
	}
	var added map[string]any
	if err := c.send(ctx, http.MethodPost, path+"/domains", nil, body, &added); err != nil {
		return nil, domainFailure(err)
	}
	return map[string]any{"domain": added, "next": nextForRecords(added)}, nil
}

// nextForRecords spells out the records for the user to add, then how to check.
func nextForRecords(added map[string]any) string {
	records, _ := added["records"].([]any)
	lines := make([]string, 0, len(records))
	for _, entry := range records {
		if record, ok := entry.(map[string]any); ok {
			lines = append(lines, fmt.Sprintf("%v %v -> %v", record["type"], record["name"], record["value"]))
		}
	}
	return fmt.Sprintf("ask the user to add these DNS records at their DNS provider: %s. Then call check_site_domain with domain_id %v until its status is ACTIVE",
		strings.Join(lines, "; "), added["id"])
}

func checkSiteDomain(ctx context.Context, c *call, in siteDomainArgs) (any, error) {
	path, err := siteDomainPath(c, in)
	if err != nil {
		return nil, err
	}
	var checked map[string]any
	if err := c.send(ctx, http.MethodPost, path+"/refresh", nil, nil, &checked); err != nil {
		return nil, domainFailure(err)
	}
	return map[string]any{"domain": checked}, nil
}

func removeSiteDomain(ctx context.Context, c *call, in siteDomainArgs) (any, error) {
	path, err := siteDomainPath(c, in)
	if err != nil {
		return nil, err
	}
	if err := c.send(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		return nil, domainFailure(err)
	}
	return map[string]any{"removed": in.DomainID}, nil
}
