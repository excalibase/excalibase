package domain

import "testing"

// A project is servable only when something has proved it works. RESTORING
// joins the deletion states: the row carries working-looking credentials, but
// nothing has yet confirmed the recovered database answers — and if the
// process driving the restore dies, nothing ever will.
func TestIsNotServableCoversRestoringAndDeletion(t *testing.T) {
	notServable := []string{
		string(StatusDeleting),
		string(StatusBackupsPendingDelete),
		string(StatusRestoring),
	}
	for _, status := range notServable {
		if !IsNotServable(status) {
			t.Errorf("%s must not be servable", status)
		}
	}

	servable := []string{"ACTIVE", string(StatusPaused), string(StatusPausing), string(StatusResuming), StatusProvisioning}
	for _, status := range servable {
		if IsNotServable(status) {
			t.Errorf("%s must stay servable", status)
		}
	}
}

func TestNotServableReasonNamesWhatIsHappening(t *testing.T) {
	if got := NotServableReason(string(StatusRestoring)); got != "project is being restored" {
		t.Errorf("restoring reason: got %q", got)
	}
	for _, status := range []string{string(StatusDeleting), string(StatusBackupsPendingDelete)} {
		if got := NotServableReason(status); got != "project is being deleted" {
			t.Errorf("%s reason: got %q", status, got)
		}
	}
}

// A restore abandoned by a dead process leaves a RESTORING row. It has to be
// deletable, or the target id is stuck forever.
func TestRestoringProjectsMayBeDeleted(t *testing.T) {
	if IsBuildingStatus(string(StatusRestoring)) {
		t.Error("a RESTORING project must be deletable: its restore may never finish")
	}
}
