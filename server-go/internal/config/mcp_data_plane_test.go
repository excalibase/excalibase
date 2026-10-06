package config

import "testing"

// The probe calls the data plane from the server, so it goes only where an
// operator said: never to PUBLIC_BASE_URL's built-in default.
func TestMCPDataPlaneURLIsNeverAGuess(t *testing.T) {
	cases := []struct {
		name, dataPlane, publicBase, want string
	}{
		{"nothing set", "", "", ""},
		{"public base set", "", "https://api.example.test", "https://api.example.test"},
		{"in-cluster gateway set", "http://gateway.platform:8080/", "https://api.example.test", "http://gateway.platform:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_DATA_PLANE_URL", tc.dataPlane)
			t.Setenv("PUBLIC_BASE_URL", tc.publicBase)
			if got := Load().MCPDataPlaneURL; got != tc.want {
				t.Fatalf("MCPDataPlaneURL = %q, want %q", got, tc.want)
			}
		})
	}
}
