//go:build integration

package sitebuilds_test

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitebuildstest"
)

func TestConnect_NextjsFrameworkSurvivesTheDatabase(t *testing.T) {
	h := sitebuildstest.New(t, sitebuildstest.Options{ServerSite: true})
	ctx := context.Background()
	h.SignIn()
	result, err := h.Service.Connect(ctx, sitebuilds.ConnectRequest{ProjectID: h.Project, SiteID: h.Site.ID, UserID: h.User,
		Repository: "acme/site", Settings: sitebuilds.Settings{Framework: sitebuilds.FrameworkNextjs}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	stored, err := h.Service.Connection(ctx, h.Project, h.Site.ID)
	if err != nil || stored.Settings.Framework != sitebuilds.FrameworkNextjs || stored.Settings.OutputDir != "" {
		t.Fatalf("stored settings %+v, err %v", stored.Settings, err)
	}
	if result.Build == nil || result.Build.Settings.Framework != sitebuilds.FrameworkNextjs {
		t.Errorf("the queued build lost its framework: %+v", result.Build)
	}
}
