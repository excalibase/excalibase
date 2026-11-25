package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// appSuspender marks a paused workload as stopped on purpose; the Kubernetes
// runtime has it, a single host has no crash guard.
type appSuspender interface {
	SuspendAppWorkload(ctx context.Context, namespace, appID, kind, reason string, at time.Time) error
}

// SuspendForCrashLoop stops an app that kept crashing (EXC-839): the same
// pause an owner makes, then the reason and time recorded on the workload and
// on the app, for Studio to show. Start again (ResumeApp) or a deploy clears it.
func (s *AppDeployService) SuspendForCrashLoop(ctx context.Context, projectID, appID string, crashes int, window time.Duration) (*apphost.Suspension, error) {
	if _, err := s.PauseApp(ctx, projectID, appID); err != nil {
		return nil, err
	}
	suspension := &apphost.Suspension{
		Kind:          apphost.SuspensionCrashLoop,
		Reason:        fmt.Sprintf("kept crashing (%d restarts in the last %s)", crashes, windowWords(window)),
		Restarts:      crashes,
		WindowMinutes: int(window / time.Minute),
		At:            time.Now().UTC(),
	}
	if err := s.markSuspended(ctx, projectID, appID, suspension); err != nil {
		return suspension, err
	}
	return suspension, nil
}

func windowWords(window time.Duration) string {
	if window == time.Hour {
		return "hour"
	}
	return window.String()
}

// markSuspended writes the marker the pause leaves no reason on. The app is
// already stopped, so a failure here is reported and the stop stands.
func (s *AppDeployService) markSuspended(ctx context.Context, projectID, appID string, suspension *apphost.Suspension) error {
	namespace, err := s.namespaceFor(projectID)
	if err != nil {
		return err
	}
	if marker, ok := s.runtime.(appSuspender); ok {
		if err := marker.SuspendAppWorkload(ctx, namespace, appID, suspension.Kind, suspension.Reason, suspension.At); err != nil {
			return fmt.Errorf("mark the workload suspended: %w", err)
		}
	}
	store, ok := s.apps.(apphost.SuspensionRecorder)
	if !ok {
		return nil
	}
	return store.RecordSuspension(projectID, appID, suspension)
}

// clearSuspension ends the stop on the app's record. Best effort: the app is
// already running again, and a stale note is hidden by its status.
func (s *AppDeployService) clearSuspension(projectID, appID string) {
	store, ok := s.apps.(apphost.SuspensionRecorder)
	if !ok {
		return
	}
	if err := store.RecordSuspension(projectID, appID, nil); err != nil {
		log.Printf("clear suspension of app %s/%s: %v", projectID, appID, err)
	}
}
