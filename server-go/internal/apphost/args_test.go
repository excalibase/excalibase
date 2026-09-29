package apphost

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func argsApp(args []string) *App {
	return &App{ID: "a1", ProjectID: "p1", Name: "cache", Image: "redis:7.4-alpine", Port: 6379,
		Args: args, Replicas: 1, Tier: domain.Free, Status: StatusCreated, Env: []EnvVar{}}
}

func TestValidateArgs(t *testing.T) {
	tooMany := make([]string, MaxArgs+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "none", args: nil},
		{name: "arguments that name an env variable", args: []string{"--requirepass", "$(REDIS_PASSWORD)"}},
		{name: "too many", args: tooMany, wantErr: "at most"},
		{name: "one too long", args: []string{strings.Repeat("a", MaxArgLength+1)}, wantErr: "exceeds"},
		{name: "empty argument", args: []string{""}, wantErr: "empty"},
		{name: "a NUL byte", args: []string{"a\x00b"}, wantErr: "control"},
		{name: "a newline", args: []string{"a\nb"}, wantErr: "control"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := argsApp(tc.args).Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want valid, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateArgsTotalSize(t *testing.T) {
	arg := strings.Repeat("a", MaxArgLength)
	args := make([]string, 0, MaxArgs)
	for len(args)*MaxArgLength <= MaxTotalArgBytes {
		args = append(args, arg)
	}
	if err := argsApp(args).Validate(); err == nil || !strings.Contains(err.Error(), "in total") {
		t.Fatalf("want a total-size refusal, got %v", err)
	}
}

func TestDeployConfigFreezesTheArgs(t *testing.T) {
	app := argsApp([]string{"--appendonly", "yes"})
	cfg := ConfigFromApp(app)
	app.Args[0] = "--changed"
	if cfg.Args[0] != "--appendonly" {
		t.Fatalf("a later edit reached the frozen config: %v", cfg.Args)
	}
	back := cfg.ToApp(app.ID, app.ProjectID, app.Name)
	if len(back.Args) != 2 || back.Args[1] != "yes" {
		t.Fatalf("ToApp lost the args: %v", back.Args)
	}
	back.Args[1] = "no"
	if cfg.Args[1] != "yes" {
		t.Fatalf("the rebuilt app shares the frozen args: %v", cfg.Args)
	}
}
