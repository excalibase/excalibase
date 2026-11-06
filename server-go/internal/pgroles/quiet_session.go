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
