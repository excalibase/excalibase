package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnInstanceNeverSerialisesItsOwnerPassword(t *testing.T) {
	encoded, err := json.Marshal(&DatabaseInstance{ProjectID: "p", Password: "owner-secret"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "owner-secret") || strings.Contains(string(encoded), `"password"`) {
		t.Errorf("serialised instance carries the owner password: %s", encoded)
	}
}
