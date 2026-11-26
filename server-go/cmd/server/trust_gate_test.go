package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

const trustTestProject = "proj-a"

// fakeTrustPlatform answers one project's owner and that owner's decision.
type fakeTrustPlatform struct{ record *trust.Record }

func (f fakeTrustPlatform) Trust() trust.Store { return f }

func (f fakeTrustPlatform) Find(context.Context, string) (*trust.Record, error) { return f.record, nil }

func (f fakeTrustPlatform) Save(context.Context, trust.Record, string) error { return nil }

func (fakeTrustPlatform) FindOrgByID(_ context.Context, id string) (*domain.Org, error) {
	return &domain.Org{ID: id, OwnerID: "owner-a"}, nil
}

func (fakeTrustPlatform) FindByProjectIDContext(_ context.Context, projectID string) (*domain.DatabaseInstance, error) {
	return &domain.DatabaseInstance{ProjectID: projectID, OrgID: "org-a"}, nil
}

func TestNewTrustGate_FlagOffAllowsAndFlagOnRefusesALimitedOwner(t *testing.T) {
	platform := fakeTrustPlatform{record: &trust.Record{UserID: "owner-a", State: trust.Limited}}
	off := newTrustGate(trustCfg(t, "false"), platform, platform)
	if err := off.CheckProject(context.Background(), trustTestProject); err != nil {
		t.Fatalf("flag off refused: %v", err)
	}
	on := newTrustGate(trustCfg(t, "true"), platform, platform)
	err := on.CheckProject(context.Background(), trustTestProject)
	if !errors.Is(err, trust.ErrVerificationRequired) || !strings.Contains(err.Error(), "90 days") {
		t.Fatalf("flag on: %v, want the refusal naming the configured threshold", err)
	}
	if err := on.CheckProject(orgguard.WithPlatformOverride(context.Background()), trustTestProject); err != nil {
		t.Fatalf("a platform admin was refused: %v", err)
	}
}

// A store that cannot hold trust decisions must refuse containers, not allow them.
func TestNewTrustGate_FlagOnWithNoTrustStoreFailsClosed(t *testing.T) {
	gate := newTrustGate(trustCfg(t, "true"), struct{}{}, struct{}{})
	if err := gate.CheckProject(context.Background(), trustTestProject); !errors.Is(err, trust.ErrCheckUnavailable) {
		t.Fatalf("got %v, want ErrCheckUnavailable", err)
	}
}

// Every consumer of the gate is wired in production: the router, the app
// service, template deploys and git builds. A missing line is a container path that is open.
func TestProductionWiresTheContainerGateEverywhere(t *testing.T) {
	for file, want := range map[string][]string{
		"main.go":        {"trustGate:             newTrustGate(cfg, store, sqlStore)", "appDeploySvc.SetContainerGate(newTrustGate(cfg, store, sqlStore))", "gate: newTrustGate(cfg, store, sqlStore)"},
		"site_builds.go": {"ContainerGate: newTrustGate(cfg, args.sqlStore, args.sqlStore)"},
		"app_network.go": {"ContainerGate: a.gate"},
		"org_guard.go":   {"RequireVerifiedForContainers(d.trustGate)"},
	} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range want {
			if !strings.Contains(string(source), line) {
				t.Errorf("%s does not contain %q: a container path is not gated", file, line)
			}
		}
	}
}

// Credits spend on containers: they refuse to start unless the gate is on.
func TestCreditsNeedTheContainerGate(t *testing.T) {
	if err := trust.RequireContainerGate(context.Background(), trustCfg(t, "false").Features(), "credits"); !errors.Is(err, trust.ErrContainerGateOff) {
		t.Fatalf("flag off: %v", err)
	}
	if err := trust.RequireContainerGate(context.Background(), trustCfg(t, "true").Features(), "credits"); err != nil {
		t.Fatalf("flag on: %v", err)
	}
}
