package sites

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

const (
	// maxRuntimeVars leaves room under Cloudflare's 128 variables for the platform's own.
	maxRuntimeVars = 64
	// maxRuntimeVarBytes is Cloudflare's limit for one variable.
	maxRuntimeVarBytes = 5 * 1024
	// PublishableKeyVar is the one platform-prefixed name a site sets itself: keys are shown once, so the platform cannot inject it.
	PublishableKeyVar = "EXCALIBASE_PUBLISHABLE_KEY"
)

// MaxRuntimeVarsBodyBytes bounds a request that sets variables: the most the validator allows (a name of at most
// 128 bytes and a value of at most 5 KB, 64 times), each byte as heavily escaped as JSON can write it (\u00XX,
// six bytes), with room for the JSON around them.
const MaxRuntimeVarsBodyBytes = maxRuntimeVars*(6*(maxRuntimeVarBytes+128)+16) + 4096

var runtimeVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// reservedRuntimePrefixes are names the platform or OpenNext's storage overrides own.
var reservedRuntimePrefixes = []string{"EXCALIBASE_", "NEXT_INC_CACHE", "NEXT_TAG_CACHE", "NEXT_CACHE_"}

var reservedRuntimeNames = map[string]bool{"ASSETS": true, "WORKER_SELF_REFERENCE": true}

// ValidateRuntimeVars checks a whole set of variables a server site may be given.
func ValidateRuntimeVars(vars map[string]string) error {
	if len(vars) > maxRuntimeVars {
		return &InputError{Reason: fmt.Sprintf("at most %d variables", maxRuntimeVars)}
	}
	for name, value := range vars {
		if !runtimeVarName.MatchString(name) {
			return &InputError{Reason: fmt.Sprintf("%q is not a variable name (letters, digits and _, not starting with a digit)", name)}
		}
		if runtimeVarReserved(name) {
			return &InputError{Reason: fmt.Sprintf("%s is reserved by the platform", name)}
		}
		if len(value) > maxRuntimeVarBytes {
			return &InputError{Reason: fmt.Sprintf("%s is larger than %d bytes", name, maxRuntimeVarBytes)}
		}
	}
	return nil
}

func runtimeVarReserved(name string) bool {
	if name == PublishableKeyVar {
		return false
	}
	upper := strings.ToUpper(name)
	if reservedRuntimeNames[upper] {
		return true
	}
	for _, prefix := range reservedRuntimePrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// SetRuntimeVars replaces a server site's variables; they reach its code with its next deploy.
func (s *Service) SetRuntimeVars(ctx context.Context, projectID, siteID string, vars map[string]string) error {
	site, err := s.runtimeVarSite(ctx, projectID, siteID)
	if err != nil {
		return err
	}
	if err := ValidateRuntimeVars(vars); err != nil {
		return err
	}
	return s.store.SetRuntimeVars(ctx, projectID, site.ID, vars)
}

// RuntimeVars answers a server site's variables.
func (s *Service) RuntimeVars(ctx context.Context, projectID, siteID string) (map[string]string, error) {
	site, err := s.runtimeVarSite(ctx, projectID, siteID)
	if err != nil {
		return nil, err
	}
	return s.store.RuntimeVars(ctx, projectID, site.ID)
}

func (s *Service) runtimeVarSite(ctx context.Context, projectID, siteID string) (*Site, error) {
	if !s.Available() {
		return nil, ErrSitesUnavailable
	}
	if err := s.requireKind(ctx, KindServer); err != nil {
		return nil, err
	}
	site, err := s.mustGet(ctx, projectID, siteID)
	if err != nil {
		return nil, err
	}
	if site.Kind != KindServer {
		return nil, &InputError{Reason: "only a server-rendered site has runtime variables"}
	}
	return site, nil
}

// runtimeBindings are the plain variables a server deploy's code runs with: the
// platform's (ADR 0045 decision 4), then the site's, snapshotted with the deploy.
// Nothing from the repository is bound.
func (s *Service) runtimeBindings(deploy *Deploy) []cfworkers.Binding {
	bindings := []cfworkers.Binding{
		{Type: cfworkers.BindingPlainText, Name: "EXCALIBASE_URL", Text: s.options.APIBaseURL},
		{Type: cfworkers.BindingPlainText, Name: "EXCALIBASE_PROJECT_ID", Text: deploy.ProjectID},
	}
	if deploy.Server == nil {
		return bindings
	}
	names := make([]string, 0, len(deploy.Server.RuntimeVars))
	for name := range deploy.Server.RuntimeVars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		bindings = append(bindings, cfworkers.Binding{Type: cfworkers.BindingPlainText, Name: name, Text: deploy.Server.RuntimeVars[name]})
	}
	return bindings
}
