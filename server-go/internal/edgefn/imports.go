package edgefn

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// carriedImports are the npm: specifiers the runtime image resolves with no
// network: its deno.json import map (a test keeps the two in step).
var carriedImports = map[string]bool{
	"npm:@excalibase/server@0.4.0":   true,
	"npm:@excalibase/server@0.5.0":   true,
	"npm:@excalibase/server@0.6.0":   true,
	"npm:@excalibase/server@0.7.0":   true,
	"npm:@excalibase/server@0.8.0":   true,
	"npm:@excalibase/server@0.9.0":   true,
	"npm:@excalibase/server@0.10.0":  true,
	"npm:@excalibase/server@0.11.0":  true,
	"npm:@excalibase/server@0.12.0":  true,
	"npm:@excalibase/server@0.13.0":  true,
	"npm:zod@^3.22.0":                true,
	"npm:zod-to-json-schema@^3.22.0": true,
}

// remoteImportPattern finds the module specifiers of an ESM bundle's static
// imports, re-exports and dynamic imports that the runtime fetches.
var remoteImportPattern = regexp.MustCompile(`(?:\bfrom|\bimport)\s*\(?\s*["']((?:npm:|jsr:|https?://)[^"']+)["']`)

// UnreachableImports lists the bundle's imports that the runtime does not
// carry and the project's egress (host:port entries, "*.suffix" wildcards)
// does not reach, each as "specifier (host)". The worker would hang fetching
// them until its init timeout (EXC-560), so a deploy refuses them by name.
func UnreachableImports(code string, egress []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range remoteImportPattern.FindAllStringSubmatch(code, -1) {
		spec := match[1]
		if seen[spec] || carriedImports[spec] {
			continue
		}
		seen[spec] = true
		host := importHost(spec)
		if host == "" || !egressReaches(egress, host) {
			out = append(out, spec+" ("+host+")")
		}
	}
	sort.Strings(out)
	return out
}

// importHost is the host the runtime fetches a specifier from.
func importHost(spec string) string {
	switch {
	case strings.HasPrefix(spec, "npm:"):
		return "registry.npmjs.org"
	case strings.HasPrefix(spec, "jsr:"):
		return "jsr.io"
	}
	parsed, err := url.Parse(spec)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// egressReaches reports whether an allowlist entry admits host on 443.
func egressReaches(egress []string, host string) bool {
	for _, entry := range egress {
		name, port, found := strings.Cut(withExplicitPort(entry), ":")
		if !found || port != "443" {
			continue
		}
		if name == host {
			return true
		}
		if suffix, wildcard := strings.CutPrefix(name, "*."); wildcard && strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}
