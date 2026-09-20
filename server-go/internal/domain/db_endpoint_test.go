package domain

import (
	"errors"
	"testing"
)

func TestNewPortRangeRejectsUnusableWindows(t *testing.T) {
	cases := []struct {
		name     string
		min, max int
	}{
		{"zero min", 0, 100},
		{"negative min", -1, 100},
		{"max below min", 200, 100},
		{"max above 65535", 60000, 70000},
		{"privileged min", 80, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewPortRange(tc.min, tc.max); !errors.Is(err, ErrInvalidPortRange) {
				t.Fatalf("NewPortRange(%d,%d) error = %v, want ErrInvalidPortRange", tc.min, tc.max, err)
			}
		})
	}
}

func TestNewPortRangeAcceptsASingleUsablePort(t *testing.T) {
	r, err := NewPortRange(30000, 30000)
	if err != nil {
		t.Fatalf("NewPortRange: %v", err)
	}
	if r.Size() != 1 {
		t.Fatalf("Size = %d, want 1", r.Size())
	}
	if !r.Contains(30000) || r.Contains(30001) {
		t.Fatalf("Contains wrong for %+v", r)
	}
}

func TestDBEndpointHostIsTheProjectUnderTheConfiguredSuffix(t *testing.T) {
	host, err := DBEndpointHost("proj-abc", "db.excalibase.io")
	if err != nil {
		t.Fatalf("DBEndpointHost: %v", err)
	}
	if host != "proj-abc.db.excalibase.io" {
		t.Fatalf("host = %q", host)
	}
}

func TestDBEndpointHostRefusesUnusableInput(t *testing.T) {
	cases := []struct{ project, suffix string }{
		{"", "db.excalibase.io"},
		{"proj-abc", ""},
		{"proj abc", "db.excalibase.io"},
		{"proj-abc", "localhost"},
		{"proj-abc", "db.internal"},
	}
	for _, tc := range cases {
		if _, err := DBEndpointHost(tc.project, tc.suffix); err == nil {
			t.Fatalf("DBEndpointHost(%q,%q) succeeded, want error", tc.project, tc.suffix)
		}
	}
}

func TestDBConnectionStringCarriesTheSSLMode(t *testing.T) {
	got := DBConnectionString("proj-abc.db.excalibase.io", 30111, "app_user", "appdb", SSLModeVerifyFull)
	want := "postgresql://app_user@proj-abc.db.excalibase.io:30111/appdb?sslmode=verify-full"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDBConnectionStringEscapesTheUser(t *testing.T) {
	got := DBConnectionString("h", 5432, "we ird@user", "db", SSLModePrefer)
	want := "postgresql://we%20ird%40user@h:5432/db?sslmode=prefer"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDBEndpointConnectionStringsShowBothChoices(t *testing.T) {
	strings := DBEndpointConnectionStrings("proj-abc.db.excalibase.io", 30111, "app_user", "appdb")
	if strings.RequireTLS != "postgresql://app_user@proj-abc.db.excalibase.io:30111/appdb?sslmode=verify-full" {
		t.Fatalf("RequireTLS = %q", strings.RequireTLS)
	}
	if strings.AllowPlaintext != "postgresql://app_user@proj-abc.db.excalibase.io:30111/appdb?sslmode=prefer" {
		t.Fatalf("AllowPlaintext = %q", strings.AllowPlaintext)
	}
}

func TestDBEndpointServiceNameIsDerivedFromTheProject(t *testing.T) {
	if got := DBEndpointServiceName("proj-abc"); got != "proj-abc-postgres-public" {
		t.Fatalf("service name = %q", got)
	}
}

func TestDBEndpointIsPublicOnlyWhenEnabledAndAllocated(t *testing.T) {
	cases := []struct {
		name string
		ep   DBEndpoint
		want bool
	}{
		{"off", DBEndpoint{PublicEnabled: false, Port: 30111}, false},
		{"on but unallocated", DBEndpoint{PublicEnabled: true, Port: 0}, false},
		{"on and allocated", DBEndpoint{PublicEnabled: true, Port: 30111}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ep.IsPublic(); got != tc.want {
				t.Fatalf("IsPublic = %v want %v", got, tc.want)
			}
		})
	}
}

func TestDefaultDBEndpointIsOffAndRequiresTLS(t *testing.T) {
	ep := DefaultDBEndpoint("proj-abc")
	if ep.PublicEnabled {
		t.Fatal("a project must not be publicly reachable until it asks")
	}
	if !ep.RequireTLS {
		t.Fatal("TLS must be required by default")
	}
	if ep.Port != 0 {
		t.Fatalf("Port = %d, want none held", ep.Port)
	}
}

func TestDBEndpointSSLModeFollowsTheSetting(t *testing.T) {
	if got := (DBEndpoint{RequireTLS: true}).SSLMode(); got != SSLModeVerifyFull {
		t.Fatalf("SSLMode = %q", got)
	}
	if got := (DBEndpoint{RequireTLS: false}).SSLMode(); got != SSLModePrefer {
		t.Fatalf("SSLMode = %q", got)
	}
}

func TestZeroPortRangeIsNotAWindowToAllocateFrom(t *testing.T) {
	if err := (PortRange{}).Validate(); !errors.Is(err, ErrInvalidPortRange) {
		t.Fatalf("error = %v, want ErrInvalidPortRange; a zero range names port 0", err)
	}
	if err := (PortRange{Min: 30000, Max: 30099}).Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
