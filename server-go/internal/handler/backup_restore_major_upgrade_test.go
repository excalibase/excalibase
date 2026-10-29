package handler

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// convertedRuns is a project upgraded once, from 16 to 17.
type convertedRuns struct {
	storage.MajorUpgradeStore
	run domain.MajorUpgrade
}

func (c convertedRuns) ConvertedMajorUpgrades(context.Context, string) ([]domain.MajorUpgrade, error) {
	return []domain.MajorUpgrade{c.run}, nil
}

// EXC-668: replacing a project's database with a point from before its major
// upgrade is refused before a job is filed; a copy is the way to reach it.
func TestRestoreInPlaceToAPointBeforeAMajorUpgradeIsRefused(t *testing.T) {
	project := backedUpProject()
	project.PostgresVersion = "17"
	h := newRestoreModesHarness(t, project)
	stopped := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	converted := stopped.Add(10 * time.Minute)
	h.svc.SetMajorUpgradeArchives(service.NewMajorUpgradeArchives(convertedRuns{run: domain.MajorUpgrade{
		ID: "mu", ProjectID: "p1", FromVersion: "16", ToVersion: "17", Converted: true,
		WritesStoppedAt: &stopped, ConvertedAt: &converted,
	}}))
	w, body := h.post(`{"mode":"in_place","confirmReplace":true,"targetTime":"2026-10-19T10:00:00Z"}`)
	if w.Code != 409 || body["error"] != service.ErrRestorePointBeforeMajorUpgrade.Error() {
		t.Fatalf("%d %v", w.Code, body)
	}
	if len(h.jobs.jobs) != 0 {
		t.Fatal("a job was filed for a refused restore")
	}
}
