package domain

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// EXC-667: a copy is a restore into a new project whose point is now, the
// latest backup, or a time.

func decodeCopy(t *testing.T, body string) CopyRequest {
	t.Helper()
	var req CopyRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return req
}

func TestCopyRequest_DefaultsToNow(t *testing.T) {
	restore, err := decodeCopy(t, `{"name":"  staging  "}`).RestoreRequest()
	if err != nil {
		t.Fatalf("RestoreRequest: %v", err)
	}
	if !restore.CopyNow || !restore.Copy || restore.NewProjectName != "staging" || restore.TargetTime != nil || restore.InPlace() {
		t.Fatalf("restore = %+v, want a new-project copy of now named staging", restore)
	}
	if kind, value := restore.RestoreTargetKind(); kind != RestoreTargetNow || value != "" {
		t.Errorf("target kind = %q %q, want now", kind, value)
	}
	if err := restore.Validate(); err != nil {
		t.Errorf("a copy of now is a valid restore request: %v", err)
	}
}

func TestCopyRequest_LatestIsARestoreWithNoTarget(t *testing.T) {
	restore, err := decodeCopy(t, `{"name":"x","point":"latest"}`).RestoreRequest()
	if err != nil {
		t.Fatalf("RestoreRequest: %v", err)
	}
	if restore.CopyNow || restore.TargetTime != nil {
		t.Fatalf("restore = %+v, want the latest backup", restore)
	}
	if kind, _ := restore.RestoreTargetKind(); kind != "latest" {
		t.Errorf("target kind = %q, want latest", kind)
	}
}

func TestCopyRequest_TimeCarriesTheTarget(t *testing.T) {
	restore, err := decodeCopy(t, `{"name":"x","point":"time","targetTime":"2026-10-29T10:00:00+07:00"}`).RestoreRequest()
	if err != nil {
		t.Fatalf("RestoreRequest: %v", err)
	}
	want := time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)
	if restore.CopyNow || restore.TargetTime == nil || !restore.TargetTime.Equal(want) {
		t.Fatalf("restore = %+v, want the time %s", restore, want)
	}
}

func TestCopyRequest_Refusals(t *testing.T) {
	long := make([]byte, MaxProjectNameLength+1)
	for i := range long {
		long[i] = 'a'
	}
	cases := map[string]string{
		"no name":            `{}`,
		"blank name":         `{"name":"   "}`,
		"name too long":      `{"name":"` + string(long) + `"}`,
		"unknown point":      `{"name":"x","point":"yesterday"}`,
		"time without time":  `{"name":"x","point":"time"}`,
		"now with a time":    `{"name":"x","targetTime":"2026-10-29T10:00:00Z"}`,
		"latest with a time": `{"name":"x","point":"latest","targetTime":"2026-10-29T10:00:00Z"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := decodeCopy(t, body).RestoreRequest()
			if !errors.Is(err, ErrCopyRequestInvalid) {
				t.Errorf("RestoreRequest = %v, want ErrCopyRequestInvalid", err)
			}
		})
	}
}

// A copy of now names no other target: the platform takes the backup it
// restores from, and a restore that asked for both would be ambiguous.
func TestRestoreRequest_CopyNowTakesNoOtherTarget(t *testing.T) {
	at := &ZonedTime{Time: time.Now()}
	for name, req := range map[string]RestoreRequest{
		"time":     {NewProjectName: "x", CopyNow: true, TargetTime: at},
		"backup":   {NewProjectName: "x", CopyNow: true, BackupID: "b1"},
		"xid":      {NewProjectName: "x", CopyNow: true, TargetXID: "5"},
		"in place": {CopyNow: true, Mode: RestoreModeInPlace, ConfirmReplace: true},
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s: a copy of now with another target was accepted", name)
		}
	}
}

// CopyNow is decided by the copy route only; a restore body cannot set it.
func TestRestoreRequest_CopyNowIsNotReadFromJSON(t *testing.T) {
	var req RestoreRequest
	if err := json.Unmarshal([]byte(`{"newProjectName":"x","copyNow":true,"CopyNow":true}`), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.CopyNow {
		t.Error("a restore body switched on a copy of now")
	}
}
