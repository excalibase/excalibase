package pgroles

import (
	"strings"
)

// quietSettings keep a session's statements out of the server log, which a
// project's Viewer reads. Only a superuser can set them, and customers cannot
// tune them, so nothing a statement names can reach that log through them. The
// client still gets the whole error. pgaudit.log is safe to name without the
// library loaded: it is then a placeholder.
var quietSettings = [][2]string{
	{"log_statement", "none"},
	{"log_min_error_statement", "panic"},
	{"log_min_duration_statement", "-1"},
	{"log_min_duration_sample", "-1"},
	{"log_transaction_sample_rate", "0"},
	{"log_error_verbosity", "terse"},
	{"pgaudit.log", "none"},
	// A deadlock victim's DETAIL prints what this session is running, under the
	// victim's settings, so the running statement must not be on record.
	{"track_activities", "off"},
}

// QuietSessionOptions are quietSettings as connect-time -c options.
func QuietSessionOptions() []string {
	options := make([]string, 0, len(quietSettings))
	for _, setting := range quietSettings {
		options = append(options, setting[0]+"="+setting[1])
	}
	return options
}

// QuietSessionSQL does the same on an open session. It must go ahead of what
// it protects, on stdin: psql sends each statement on its own there, so the
// settings are in force when the next one arrives.
func QuietSessionSQL() string {
	var sql strings.Builder
	for _, setting := range quietSettings {
		sql.WriteString("SET " + setting[0] + " = '" + setting[1] + "';\n")
	}
	return sql.String()
}

// QuietSessionPGOptions is the same for a psql in a script, as PGOPTIONS.
func QuietSessionPGOptions() string {
	return "-c " + strings.Join(QuietSessionOptions(), " -c ")
}

// RoleSQLDoneMarker is the last thing a role script prints. psql exits 0 when
// stdin ends inside a transaction, which rolled it back, so only this says the
// script reached its end.
const RoleSQLDoneMarker = "excalibase_role_sql_done"
