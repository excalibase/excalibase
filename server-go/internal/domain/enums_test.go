package domain

import (
	"encoding/json"
	"testing"
)

func TestDatabaseTypeSerialization(t *testing.T) {
	tests := []struct {
		dt   DatabaseType
		want string
	}{
		{PostgreSQL, `"POSTGRESQL"`},
		{MySQL, `"MYSQL"`},
		{MongoDB, `"MONGODB"`},
	}
	for _, tt := range tests {
		b, err := json.Marshal(tt.dt)
		if err != nil {
			t.Fatalf("marshal %s: %v", tt.dt, err)
		}
		if string(b) != tt.want {
			t.Errorf("got %s, want %s", b, tt.want)
		}
	}
}

func TestTierTypeSerialization(t *testing.T) {
	tests := []struct {
		tier TierType
		want string
	}{
		{Free, `"FREE"`},
		{Standard, `"STANDARD"`},
		{Enterprise, `"ENTERPRISE"`},
	}
	for _, tt := range tests {
		b, err := json.Marshal(tt.tier)
		if err != nil {
			t.Fatalf("marshal %s: %v", tt.tier, err)
		}
		if string(b) != tt.want {
			t.Errorf("got %s, want %s", b, tt.want)
		}
	}
}

func TestProvisioningStageSerialization(t *testing.T) {
	tests := []struct {
		stage ProvisioningStage
		want  string
	}{
		{StageValidating, `"VALIDATING"`},
		{StageCompleted, `"COMPLETED"`},
		{StageFailed, `"FAILED"`},
	}
	for _, tt := range tests {
		b, _ := json.Marshal(tt.stage)
		if string(b) != tt.want {
			t.Errorf("got %s, want %s", b, tt.want)
		}
	}
}
