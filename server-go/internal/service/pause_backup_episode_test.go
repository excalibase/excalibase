package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A pause that fails after the backup used to take a whole new backup on
// every retry — four a day, forever, for a project that can never pause.
// The completed backup belongs to the pause episode, not to the attempt.
func TestRetriesReuseTheBackupTakenForThisPauseEpisode(t *testing.T) {
	f := newObservedPause(t)
	f.pauser.pauseErr = errors.New("hibernate keeps failing")

	for i := 0; i < 3; i++ {
		if err := f.pause(t); !errors.Is(err, ErrPauseNotObserved) {
			t.Fatalf("attempt %d: got %v, want ErrPauseNotObserved", i+1, err)
		}
	}

	if f.backups.triggers != 1 {
		t.Errorf("three attempts filed %d backups; the episode's backup must be reused", f.backups.triggers)
	}
	inst := f.reload(t)
	if inst.PauseBackupID == "" || inst.PauseBackupAt == nil {
		t.Errorf("the episode's backup must be recorded on the row: %+v", inst)
	}
}

// A backup wait that timed out leaves the Backup CR running. The retry must
// observe THAT backup rather than filing another one beside it.
func TestATimedOutBackupIsObservedByTheRetryNotRefiled(t *testing.T) {
	f := newObservedPause(t)
	f.backups.statuses = []string{"IN_PROGRESS"}

	if err := f.pause(t); !errors.Is(err, ErrPauseBackupNotCompleted) {
		t.Fatalf("first attempt: got %v", err)
	}
	inst := f.reload(t)
	if inst.PauseBackupID == "" {
		t.Fatal("the backup that was left running must be recorded so the retry can watch it")
	}

	// It finished in the meantime; the retry must see it, not start another.
	f.backups.statuses = []string{"COMPLETED"}
	if err := f.pause(t); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if f.backups.triggers != 1 {
		t.Errorf("the retry filed %d backups; it must observe the one already running", f.backups.triggers)
	}
}

// A recorded backup that is no longer the project's latest, or has gone
// stale, is not a recovery point for this pause: take a fresh one.
func TestAStaleOrSupersededEpisodeBackupIsNotReused(t *testing.T) {
	for name, age := range map[string]time.Duration{
		"too old": pauseBackupReuseWindow + time.Minute,
		"fresh":   time.Minute,
	} {
		t.Run(name, func(t *testing.T) {
			f := newObservedPause(t)
			inst := f.reload(t)
			inst.Status = string(domain.StatusPausing)
			inst.PauseBackupID = "bk-old"
			inst.PauseBackupAt = &domain.FlexTime{Time: time.Now().Add(-age)}
			if err := f.store.Update(inst); err != nil {
				t.Fatalf("seed episode: %v", err)
			}
			f.backups.latestID = "bk-old"

			if err := f.pause(t); err != nil {
				t.Fatalf("Pause: %v", err)
			}
			wantTriggers := 0
			if age > pauseBackupReuseWindow {
				wantTriggers = 1
			}
			if f.backups.triggers != wantTriggers {
				t.Errorf("triggers: got %d, want %d", f.backups.triggers, wantTriggers)
			}
		})
	}
}

// Once the project settles the episode is over, so the next pause takes its
// own backup rather than leaning on an old one.
func TestSettlingClearsTheEpisodeBackup(t *testing.T) {
	f := newObservedPause(t)
	if err := f.pause(t); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if inst := f.reload(t); inst.PauseBackupID != "" || inst.PauseBackupAt != nil {
		t.Errorf("PAUSED must clear the episode: %+v", inst)
	}

	if err := f.svc.Resume(context.Background(), observedPauseProject); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if inst := f.reload(t); inst.PauseBackupID != "" {
		t.Errorf("ACTIVE must clear the episode: %+v", inst)
	}
}
