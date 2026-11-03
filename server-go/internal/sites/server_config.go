package sites

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"time"
)

// Compatibility dates a server deploy may run on: OpenNext needs 2024-09-23
// or later, and nothing past the runtime the build was tested on (EXC-661 spike: workerd 1.20261006.1).
const (
	EarliestCompatibilityDate = "2024-09-23"
	LatestCompatibilityDate   = "2026-10-06"
	DefaultCompatibilityDate  = "2025-09-01"
)

// maxSiteJSONBytes bounds site.json, which is read into memory.
const maxSiteJSONBytes = 16 << 10

// allowedCompatibilityFlags are the runtime switches a site may ask for; any other could weaken the sandbox or our tests.
var allowedCompatibilityFlags = []string{"nodejs_compat", "nodejs_als", strictlyPublicFetch}

// strictlyPublicFetch sends a fetch to a hostname on one of our zones out to the internet,
// through its WAF and routes, never straight to its origin; every server deploy runs with it.
const strictlyPublicFetch = "global_fetch_strictly_public"

// Frameworks a server deploy may name; it only labels the deploy.
var knownFrameworks = []string{"nextjs", "astro", "sveltekit", "nuxt", "react-router", "other"}

// ServerConfig is a server deploy's site.json, checked.
type ServerConfig struct {
	Main               string   `json:"main"`
	CompatibilityDate  string   `json:"compatibilityDate"`
	CompatibilityFlags []string `json:"compatibilityFlags"`
	Framework          string   `json:"framework,omitempty"`
}

type siteJSON struct {
	Kind               string   `json:"kind"`
	Main               string   `json:"main"`
	CompatibilityDate  string   `json:"compatibilityDate"`
	CompatibilityFlags []string `json:"compatibilityFlags"`
	Framework          string   `json:"framework"`
}

// parseSiteJSON reads and checks site.json; the main module is checked once the modules are known.
func parseSiteJSON(reader io.Reader, size int64) (*ServerConfig, error) {
	if size > maxSiteJSONBytes {
		return nil, refuse("site.json is larger than %d bytes", maxSiteJSONBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, size))
	if err != nil {
		return nil, refuseRead(err, "could not read site.json from the archive: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var parsed siteJSON
	if err := decoder.Decode(&parsed); err != nil {
		return nil, refuse("site.json is not valid: %v", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, refuse("site.json holds more than one JSON object")
	}
	return checkSiteJSON(parsed)
}

func checkSiteJSON(parsed siteJSON) (*ServerConfig, error) {
	if parsed.Kind != KindServer {
		return nil, refuse(`site.json must say "kind": "server" for a server-rendered site`)
	}
	if parsed.Main == "" {
		return nil, refuse("site.json must name the main module (main), relative to server/")
	}
	date, err := checkCompatibilityDate(parsed.CompatibilityDate)
	if err != nil {
		return nil, err
	}
	if err := checkCompatibilityFlags(parsed.CompatibilityFlags); err != nil {
		return nil, err
	}
	if parsed.Framework != "" && !slices.Contains(knownFrameworks, parsed.Framework) {
		return nil, refuse("site.json names framework %q; use one of %v", parsed.Framework, knownFrameworks)
	}
	flags := append([]string{}, parsed.CompatibilityFlags...)
	if !slices.Contains(flags, strictlyPublicFetch) {
		flags = append(flags, strictlyPublicFetch)
	}
	return &ServerConfig{Main: parsed.Main, CompatibilityDate: date, CompatibilityFlags: flags, Framework: parsed.Framework}, nil
}

func checkCompatibilityDate(date string) (string, error) {
	if date == "" {
		return DefaultCompatibilityDate, nil
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return "", refuse("compatibilityDate %q is not a YYYY-MM-DD date", date)
	}
	// ISO dates order as strings.
	if date < EarliestCompatibilityDate || date > LatestCompatibilityDate {
		return "", refuse("compatibilityDate must be between %s and %s", EarliestCompatibilityDate, LatestCompatibilityDate)
	}
	return date, nil
}

func checkCompatibilityFlags(flags []string) error {
	seen := map[string]bool{}
	for _, flag := range flags {
		if !slices.Contains(allowedCompatibilityFlags, flag) {
			return refuse("compatibility flag %q is not offered; use any of %v", flag, allowedCompatibilityFlags)
		}
		if seen[flag] {
			return refuse("compatibility flag %s is listed twice", flag)
		}
		seen[flag] = true
	}
	return nil
}
