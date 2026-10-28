package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

func TestASingleHostDatabaseIsReachableFromThisHostOnly(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, map[string]provisioner.ContainerState{
		singleHostContainer: runningWithPorts(map[string]int{"5432": 32801}),
	})
	view, err := endpoints.AllowedSources(context.Background(), "proj-docsvc001")
	if err != nil {
		t.Fatalf("AllowedSources: %v", err)
	}
	if view.Access != domain.DBSourceAccessHost || !view.SingleHost || len(view.Sources) != 0 {
		t.Fatalf("view %+v", view)
	}
	list, _ := domain.ParseDBSourceAllowList(false, []string{"198.51.100.7"})
	if _, err := endpoints.SetAllowedSources(context.Background(), "proj-docsvc001", list); !errors.Is(err, ErrDBEndpointUnsupported) {
		t.Fatalf("want ErrDBEndpointUnsupported, got %v", err)
	}
}

func TestASingleHostPublishingOnEveryInterfaceSaysAnyAddress(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, map[string]provisioner.ContainerState{
		singleHostContainer: runningWithPorts(map[string]int{"5432": 32801}),
	})
	endpoints.PublishesOnEveryInterface(true)
	view, err := endpoints.AllowedSources(context.Background(), "proj-docsvc001")
	if err != nil || view.Access != domain.DBSourceAccessAny {
		t.Fatalf("view %+v, %v", view, err)
	}
}
