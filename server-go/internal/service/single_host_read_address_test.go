package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

func TestASingleHostDatabaseHasNoReadAddress(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, map[string]provisioner.ContainerState{
		singleHostContainer: runningWithPorts(map[string]int{"5432": 32801}),
	})
	view, err := endpoints.ReadAddress(context.Background(), "proj-docsvc001")
	if err != nil {
		t.Fatalf("ReadAddress: %v", err)
	}
	if !view.SingleHost || view.Reason != domain.ReadAddressSingleHost || view.Replicas != 0 ||
		view.Internal.Host != "" || view.Port != 0 || view.Enabled {
		t.Fatalf("view %+v", view)
	}
}

func TestASingleHostRefusesAReadPort(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, nil)
	_, err := endpoints.SetReadAddress(context.Background(), "proj-docsvc001", true)
	if !errors.Is(err, ErrDBEndpointUnsupported) || !strings.Contains(err.Error(), "one copy") {
		t.Fatalf("enable on a single host: %v", err)
	}
}

func TestASingleHostReadAddressOfAnUnknownProjectIsRefused(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, nil)
	if _, err := endpoints.ReadAddress(context.Background(), "proj-missing01"); err == nil {
		t.Fatal("an unknown project was described")
	}
}
