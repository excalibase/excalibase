package mcpserver

import "fmt"

// appRestarts is what the runtime saw of an app's containers (EXC-651): an
// app it restarted still reads ACTIVE and its deploy succeeded.
type appRestarts struct {
	Count        int    `json:"count"`
	LastReason   string `json:"lastReason,omitempty"`
	LastExitCode *int   `json:"lastExitCode,omitempty"`
	LastAt       string `json:"lastAt,omitempty"`
}

// restartNote says what an app's restarts mean, or nothing for an app that
// has not restarted.
func restartNote(app appView) string {
	restarts := app.Restarts
	if restarts == nil || restarts.Count == 0 {
		return ""
	}
	note := fmt.Sprintf("%s restarted %d time(s) since its current deploy started it", app.Name, restarts.Count)
	if restarts.LastReason != "" {
		note += "; the last stop was " + restarts.LastReason
		if restarts.LastExitCode != nil {
			note += fmt.Sprintf(" (exit %d)", *restarts.LastExitCode)
		}
	}
	if restarts.LastAt != "" {
		note += " at " + restarts.LastAt
	}
	switch restarts.LastReason {
	case "OOMKilled":
		note += ": it ran out of its memory limit and was killed. Lower its memory use, or move the project to a plan with more memory."
	case "Error":
		note += ": the app exited on its own. get_logs source app shows what it printed before it stopped."
	default:
		note += ". get_logs source app shows what it printed."
	}
	return note
}

func restartNotes(apps []appView) []string {
	var notes []string
	for _, app := range apps {
		if note := restartNote(app); note != "" {
			notes = append(notes, note)
		}
	}
	return notes
}

func addRestartNote(app appView, out map[string]any) {
	if note := restartNote(app); note != "" {
		out["restarted"] = note
	}
}
