package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

func withCatalog(rig *tplRig, on bool) *tplRig {
	if on {
		rig.svc.deps.Flags = features.NewStatic(features.TemplateCatalog)
	} else {
		rig.svc.deps.Flags = features.NewStatic()
	}
	return rig
}

func listedIDs(t *testing.T, rig *tplRig) []string {
	t.Helper()
	views, err := rig.svc.List(context.Background(), tplProject, true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := []string{}
	for _, view := range views {
		ids = append(ids, view.ID)
	}
	return ids
}

// Off, the catalog answers as if it did not exist; the starters are unchanged.
func TestTemplateCatalog_OffIsInvisible(t *testing.T) {
	rig := withCatalog(newTemplateRig(t), false)
	if got := strings.Join(listedIDs(t, rig), ","); got != "redis,web-redis,web-postgres,storefront-demo" {
		t.Fatalf("listed %s", got)
	}
	if _, err := rig.svc.Get(context.Background(), tplProject, "valkey", true); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "valkey", "dev", adminConfirmed); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("deploy: %v", err)
	}
	rig.expectNothingDone(t)
	rig.svc.deps.Flags = nil
	if _, err := rig.svc.Get(context.Background(), tplProject, "valkey", true); !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("no flags is off: %v", err)
	}
}

func TestTemplateCatalog_OnListsItAfterTheStarters(t *testing.T) {
	rig := withCatalog(newTemplateRig(t), true)
	ids := listedIDs(t, rig)
	if len(ids) != 6 || ids[3] != "storefront-demo" || ids[4] != "valkey" {
		t.Fatalf("listed %v", ids)
	}
	view, err := rig.svc.Get(context.Background(), tplProject, "rabbitmq", true)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if view.Category != "queue" || !strings.HasPrefix(view.License, "MPL") || !view.Catalog || view.Apps[0].MinMemory != "1Gi" {
		t.Fatalf("view %+v", view)
	}
}

// The deployer sees the connection URL once, in the deploy's answer; it is never printed.
func TestTemplateCatalog_DeployShowsTheGeneratedURLOnce(t *testing.T) {
	rig := withCatalog(newTemplateRig(t), true)
	result, err := rig.svc.Deploy(context.Background(), tplProject, "valkey", "dev", adminConfirmed)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	valkey := rig.apps.held[0]
	password := rig.vault.values[apphost.AppSecretRef(tplProject, valkey.ID, "VALKEY_PASSWORD").Path]
	if len(password) != 32 || len(result.Credentials) != 1 || result.Credentials[0].Name != "VALKEY_URL" ||
		result.Credentials[0].Value != "redis://:"+password+"@valkey:6379/0" {
		t.Fatalf("credentials: %d", len(result.Credentials))
	}
	if strings.Contains(fmt.Sprintf("%v %+v %#v", result, result, result), password) {
		t.Fatal("a shown value was printed")
	}
}

func TestTemplateCatalog_MemoryAboveThePlanIsRefused(t *testing.T) {
	rig := withCatalog(newTemplateRig(t), true)
	_, err := rig.svc.Deploy(context.Background(), tplProject, "rabbitmq", "dev", adminConfirmed)
	refusedWith(t, err, `app "rabbitmq" needs at least 1Gi of memory per copy and the FREE plan gives 256Mi`)
	rig.expectNothingDone(t)
	view, err := rig.svc.Get(context.Background(), tplProject, "rabbitmq", true)
	if err != nil || len(view.Fit.Refusals) != 1 {
		t.Fatalf("the card says why: %+v %v", view.Fit, err)
	}

	rig.svc.deps.Plans = fixedPlan{tier: domain.Standard}
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "rabbitmq", "dev", adminConfirmed); err != nil {
		t.Fatalf("standard holds rabbitmq: %v", err)
	}
}

// On a single host a project's apps always share its network (ADR 0039): no switch, no confirm.
func TestTemplate_SingleHostSharesTheProjectNetwork(t *testing.T) {
	rig := newTemplateRig(t)
	rig.project.DeploymentMode = domain.ModeDocker
	developer := TemplateDeployOptions{}
	view, err := rig.svc.Get(context.Background(), tplProject, "web-redis", false)
	if err != nil || !view.Fit.PrivateNetworkOn || len(view.Fit.Refusals) != 0 {
		t.Fatalf("view %+v %v", view.Fit, err)
	}
	result, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", developer)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if result.PrivateNetworkTurnedOn || rig.log.count("network ") != 0 || rig.log.count("create ") != 2 {
		t.Fatalf("result %+v, events %v", result, rig.log.all())
	}

	rig = newTemplateRig(t)
	rig.project.DeploymentMode = domain.ModeDocker
	rig.svc.deps.Network = nil
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "redis", "dev", developer); err != nil {
		t.Fatalf("a single host needs no network service: %v", err)
	}
}
