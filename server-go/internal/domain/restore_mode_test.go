package domain

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRestoreRequestModes(t *testing.T) {
	cases := []struct {
		name    string
		r       RestoreRequest
		wantErr error
		inPlace bool
	}{
		{"no mode is a new project", RestoreRequest{NewProjectName: "copy"}, nil, false},
		{"new project by name", RestoreRequest{Mode: RestoreModeNewProject, NewProjectName: "copy"}, nil, false},
		{"in place confirmed needs no name", RestoreRequest{Mode: RestoreModeInPlace, ConfirmReplace: true}, nil, true},
		{"in place unconfirmed is refused", RestoreRequest{Mode: RestoreModeInPlace}, ErrRestoreReplaceUnconfirmed, true},
		{"unknown mode is refused", RestoreRequest{Mode: "swap", NewProjectName: "copy"}, ErrRestoreModeUnknown, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.r.Validate()
			if !errors.Is(err, c.wantErr) || (c.wantErr == nil && err != nil) {
				t.Fatalf("Validate() = %v, want %v", err, c.wantErr)
			}
			if c.r.InPlace() != c.inPlace {
				t.Errorf("InPlace() = %v, want %v", c.r.InPlace(), c.inPlace)
			}
		})
	}
}

func TestRestoreRequestInPlaceStillChecksTargets(t *testing.T) {
	r := RestoreRequest{Mode: RestoreModeInPlace, ConfirmReplace: true, TargetXID: "1", TargetLSN: "0/1"}
	if err := r.Validate(); err == nil {
		t.Fatal("two targets must be refused in place too")
	}
}

func TestRestoreRequestModeFromJSON(t *testing.T) {
	var r RestoreRequest
	if err := json.Unmarshal([]byte(`{"mode":"in_place","confirmReplace":true}`), &r); err != nil {
		t.Fatal(err)
	}
	if !r.InPlace() || !r.ConfirmReplace {
		t.Fatalf("decoded %+v", r)
	}
}

func TestRestoreRequestModeOrDefault(t *testing.T) {
	if (RestoreRequest{}).ModeOrDefault() != RestoreModeNewProject {
		t.Error("a request without a mode is a copy")
	}
	if (RestoreRequest{Mode: RestoreModeInPlace}).ModeOrDefault() != RestoreModeInPlace {
		t.Error("in_place is kept")
	}
}

func TestRestoreJobModeIsPublic(t *testing.T) {
	raw, err := json.Marshal(RestoreJob{ID: "j", Mode: RestoreModeInPlace})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["mode"] != RestoreModeInPlace {
		t.Fatalf("job JSON %s", raw)
	}
}
