package trust

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

const (
	gateProject = "proj-a"
	gateOrg     = "org-a"
	gateOwner   = "owner-a"
)

var testPolicy = Policy{MinAgeDays: 90, MinPublicEvents: 5, ActivityWindowDays: 90, MinRepos: 3, RepoMinAgeDays: 30}

func gateFor(f *fixture, flags features.Flags) *Gate {
	return NewGate(f.resolver(flags), testPolicy)
}

func TestGateRefusesALimitedOwnersProject(t *testing.T) {
	f := newFixture()
	f.project(gateProject, gateOrg, gateOwner)
	f.store.records[gateOwner] = &Record{UserID: gateOwner, State: Limited}
	err := gateFor(f, on()).CheckProject(context.Background(), gateProject)
	if !errors.Is(err, ErrVerificationRequired) || !IsRefusal(err) {
		t.Fatalf("got %v, want ErrVerificationRequired", err)
	}
	for _, want := range []string{"GitHub", "90 days", "5 public events", "3 public repositories"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

func TestGateAllowsAFullOwnersProject(t *testing.T) {
	f := newFixture()
	f.project(gateProject, gateOrg, gateOwner)
	f.store.records[gateOwner] = &Record{UserID: gateOwner, State: Full}
	if err := gateFor(f, on()).CheckProject(context.Background(), gateProject); err != nil {
		t.Fatalf("refused a full owner: %v", err)
	}
}

// A limited member of a full owner's org runs at full: the org owner decides.
func TestGateJudgesTheOrgOwnerNotTheCaller(t *testing.T) {
	f := newFixture()
	f.project(gateProject, gateOrg, gateOwner)
	f.store.records[gateOwner] = &Record{UserID: gateOwner, State: Full}
	f.store.records["member"] = &Record{UserID: "member", State: Limited}
	if err := gateFor(f, on()).CheckProject(context.Background(), gateProject); err != nil {
		t.Fatalf("refused a full owner's project because of a member: %v", err)
	}
}

func TestGateFlagOffAllowsEverythingAndReadsNothing(t *testing.T) {
	f := newFixture()
	f.store.err = errors.New("must not be read")
	if err := gateFor(f, off()).CheckProject(context.Background(), gateProject); err != nil {
		t.Fatalf("flag off refused: %v", err)
	}
	if f.store.finds != 0 || f.instances.lookups != 0 {
		t.Errorf("flag off still read: %d %d", f.store.finds, f.instances.lookups)
	}
}

// An account with no decision, an unreadable decision or an unknown project is refused, never let through.
func TestGateFailsClosed(t *testing.T) {
	f := newFixture()
	f.project(gateProject, gateOrg, gateOwner)
	gate := gateFor(f, on())
	if err := gate.CheckProject(context.Background(), gateProject); !errors.Is(err, ErrVerificationRequired) {
		t.Errorf("no decision: got %v, want a refusal", err)
	}
	if err := gate.CheckProject(context.Background(), unknownProject); !errors.Is(err, ErrCheckUnavailable) {
		t.Errorf("unknown project: got %v, want ErrCheckUnavailable", err)
	}
	f.store.err = errors.New("db down")
	if err := gate.CheckProject(context.Background(), gateProject); !errors.Is(err, ErrCheckUnavailable) || !IsRefusal(err) {
		t.Errorf("unreadable decision: got %v, want ErrCheckUnavailable", err)
	}
}

func TestANilGateAllowsEverything(t *testing.T) {
	var gate *Gate
	if err := gate.CheckProject(context.Background(), gateProject); err != nil {
		t.Fatalf("nil gate refused: %v", err)
	}
}

type overrideKey struct{}

func TestGateBypassForAPlatformAdmin(t *testing.T) {
	f := newFixture()
	f.project(gateProject, gateOrg, gateOwner)
	gate := gateFor(f, on()).WithBypass(func(ctx context.Context) bool { return ctx.Value(overrideKey{}) != nil })
	if err := gate.CheckProject(context.Background(), gateProject); err == nil {
		t.Fatal("limited owner passed without the override")
	}
	if err := gate.CheckProject(context.WithValue(context.Background(), overrideKey{}, true), gateProject); err != nil {
		t.Fatalf("the override was refused: %v", err)
	}
}

func TestContainerGateOnFollowsTheFlag(t *testing.T) {
	if !ContainerGateOn(context.Background(), on()) {
		t.Error("flag on: gate reported off")
	}
	if ContainerGateOn(context.Background(), off()) || ContainerGateOn(context.Background(), nil) {
		t.Error("flag off or nil: gate reported on")
	}
}

func TestRequireContainerGateRefusesCreditsWithoutIt(t *testing.T) {
	if err := RequireContainerGate(context.Background(), off(), "credits"); !errors.Is(err, ErrContainerGateOff) || !strings.Contains(err.Error(), "credits") {
		t.Errorf("flag off: got %v", err)
	}
	if err := RequireContainerGate(context.Background(), on(), "credits"); err != nil {
		t.Errorf("flag on: got %v", err)
	}
}

func TestWriteRefusalAnswers403WithACodeAnd503ForAnUnreadableCheck(t *testing.T) {
	w := httptest.NewRecorder()
	WriteRefusal(w, &VerificationRequiredError{Message: "verify first"})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"trust_required"`) || !strings.Contains(w.Body.String(), "verify first") {
		t.Errorf("refusal: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	WriteRefusal(w, ErrCheckUnavailable)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Errorf("unavailable: %d %v", w.Code, w.Header())
	}
}
