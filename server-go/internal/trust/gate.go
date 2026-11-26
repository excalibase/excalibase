package trust

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// CodeTrustRequired is the machine-readable code of a refusal, for Studio and agents.
const CodeTrustRequired = "trust_required"

var (
	// ErrVerificationRequired is what a limited account meets when it asks for a container.
	ErrVerificationRequired = errors.New("trust: the account must be verified to run containers")
	// ErrCheckUnavailable means the owner's trust could not be read. The action is
	// refused: an unknown answer must not let a container run.
	ErrCheckUnavailable = errors.New("the account's verification could not be checked; try again")
	// ErrContainerGateOff refuses a feature that spends money on containers while the gate is off.
	ErrContainerGateOff = errors.New("trust: the container gate is off")
)

// IsRefusal reports whether err is one of the gate's refusals.
func IsRefusal(err error) bool {
	return errors.Is(err, ErrVerificationRequired) || errors.Is(err, ErrCheckUnavailable)
}

// VerificationRequiredError carries the sentence the caller reads.
type VerificationRequiredError struct{ Message string }

func (e *VerificationRequiredError) Error() string { return e.Message }

func (e *VerificationRequiredError) Is(target error) bool { return target == ErrVerificationRequired }

// Policy is the verification rule as configured, so the refusal can say what
// to do without a number living in this file.
type Policy struct {
	MinAgeDays         int
	MinPublicEvents    int
	ActivityWindowDays int
	MinRepos           int
	RepoMinAgeDays     int
}

// Message tells a limited account what verifies it and how.
func (p Policy) Message() string {
	return fmt.Sprintf("Running containers needs a verified account. Verify with your GitHub account in Studio, then try again. "+
		"The GitHub account must be at least %d days old and show activity: %d public events in the last %d days, "+
		"or %d public repositories, one of them older than %d days. Databases are not affected.",
		p.MinAgeDays, p.MinPublicEvents, p.ActivityWindowDays, p.MinRepos, p.RepoMinAgeDays)
}

// ProjectTrust answers the trust of a project's owner; Resolver does.
type ProjectTrust interface {
	ForProject(ctx context.Context, projectID string) (State, error)
}

// Gate refuses to start a container for a project whose owner is limited. A
// nil Gate allows everything, and so does the flag being off (the resolver
// answers full without reading).
type Gate struct {
	trust   ProjectTrust
	message string
	bypass  func(context.Context) bool
}

func NewGate(trust ProjectTrust, policy Policy) *Gate {
	return &Gate{trust: trust, message: policy.Message()}
}

// WithBypass lets a platform admin act on a limited owner's project, as an
// admin may resume work in a suspended organisation.
func (g *Gate) WithBypass(bypass func(context.Context) bool) *Gate {
	g.bypass = bypass
	return g
}

// CheckProject refuses when the project's org owner is limited, and when that
// cannot be read.
func (g *Gate) CheckProject(ctx context.Context, projectID string) error {
	if g == nil || (g.bypass != nil && g.bypass(ctx)) {
		return nil
	}
	state, err := g.trust.ForProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrCheckUnavailable, err)
	}
	if state != Full {
		return &VerificationRequiredError{Message: g.message}
	}
	return nil
}

// ContainerGateOn reports whether containers are gated by trust now. Credits
// ($300 of spend) must not start without it.
func ContainerGateOn(ctx context.Context, flags features.Flags) bool {
	return features.Enabled(ctx, flags, features.LimitedTrial)
}

// RequireContainerGate is the boot guard a money feature calls: it refuses to
// start while the gate is off.
func RequireContainerGate(ctx context.Context, flags features.Flags, feature string) error {
	if ContainerGateOn(ctx, flags) {
		return nil
	}
	return fmt.Errorf("%w: %s spends on containers, so FEATURE_LIMITEDTRIAL must be on first", ErrContainerGateOff, feature)
}

// WriteRefusal answers a gate refusal: 403 with code trust_required, or 503
// when the check could not be read.
func WriteRefusal(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	if errors.Is(err, ErrVerificationRequired) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "code": CodeTrustRequired, "status": http.StatusForbidden})
		return
	}
	w.Header().Set("Retry-After", "5")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": ErrCheckUnavailable.Error(), "status": http.StatusServiceUnavailable})
}
