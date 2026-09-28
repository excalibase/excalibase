package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ParseBootstrapServicePermissions reads BOOTSTRAP_SERVICE_PERMISSIONS, the
// comma-separated capability list of the svc-bootstrap token (EXC-485).
// Validation of each entry is the capability parser's job.
func ParseBootstrapServicePermissions(raw string) []string {
	permissions := []string{}
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			permissions = append(permissions, entry)
		}
	}
	return permissions
}

// ParseServiceTokenCeilings reads SERVICE_TOKEN_CEILINGS: a JSON object from
// service principal name to the widest permission list a service token that
// manages it may mint (EXC-485). Unset means none; malformed is an error.
func ParseServiceTokenCeilings(raw string) (map[string][]string, error) {
	ceilings := map[string][]string{}
	if strings.TrimSpace(raw) == "" {
		return ceilings, nil
	}
	if err := json.Unmarshal([]byte(raw), &ceilings); err != nil {
		return nil, fmt.Errorf("SERVICE_TOKEN_CEILINGS is not a JSON object of permission lists: %w", err)
	}
	return ceilings, nil
}

// LoadServiceTokenCeilings reads SERVICE_TOKEN_CEILINGS from the environment.
func LoadServiceTokenCeilings() (map[string][]string, error) {
	return ParseServiceTokenCeilings(os.Getenv("SERVICE_TOKEN_CEILINGS"))
}
