package config

import "testing"

func TestParseHostLogsDefaults(t *testing.T) {
	got, err := ParseHostLogs(" http://loki:3100/ ", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.LokiURL != "http://loki:3100" || got.DailyBytesPerProject != 64<<20 {
		t.Errorf("host logs = %+v", got)
	}
	if got, err := ParseHostLogs("", ""); err != nil || got.LokiURL != "" {
		t.Errorf("unset = %+v %v", got, err)
	}
}

func TestParseHostLogsDailyBudget(t *testing.T) {
	got, err := ParseHostLogs("http://loki:3100", "16")
	if err != nil || got.DailyBytesPerProject != 16<<20 {
		t.Errorf("16 MiB = %+v %v", got, err)
	}
	for _, bad := range []string{"0", "-1", "abc", "1.5", "102401"} {
		if _, err := ParseHostLogs("http://loki:3100", bad); err == nil {
			t.Errorf("HOST_LOGS_DAILY_MB=%q accepted", bad)
		}
	}
}

func TestParseHostLogsRefusesAnAddressThatIsNotPlainHTTP(t *testing.T) {
	for _, bad := range []string{"loki:3100", "ftp://loki", "http://", "http://user:pw@loki:3100", "http://loki:3100?x=1", "http://loki:3100/#f"} {
		if _, err := ParseHostLogs(bad, ""); err == nil {
			t.Errorf("HOST_LOGS_LOKI_URL=%q accepted", bad)
		}
	}
}
