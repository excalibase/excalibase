package config

import (
	"strings"
	"testing"
)

const pinnedDenoImage = "excalibase/deno-runtime:1.7.1"

func TestLoadDenoRuntimeImageHasNoFloatingDefault(t *testing.T) {
	t.Setenv("DENO_RUNTIME_IMAGE", "")
	if got := Load().DenoRuntimeImage; got != "" {
		t.Fatalf("DenoRuntimeImage default = %q, want none: a default would drift with the registry", got)
	}
}

func TestCheckDenoRuntimeImage(t *testing.T) {
	cases := map[string]struct {
		cfg     AppConfig
		wantErr string
	}{
		"k8s with a release tag":    {cfg: AppConfig{AppTmpPercent: 25, ProvisionerMode: "k8s", DenoRuntimeSecret: "s", DenoRuntimeImage: pinnedDenoImage}},
		"k8s without an image":      {cfg: AppConfig{AppTmpPercent: 25, ProvisionerMode: "k8s", DenoRuntimeSecret: "s"}, wantErr: "DENO_RUNTIME_IMAGE"},
		"k8s latest":                {cfg: AppConfig{AppTmpPercent: 25, ProvisionerMode: "k8s", DenoRuntimeSecret: "s", DenoRuntimeImage: "excalibase/deno-runtime:latest"}, wantErr: "accepted"},
		"k8s untagged":              {cfg: AppConfig{AppTmpPercent: 25, ProvisionerMode: "k8s", DenoRuntimeSecret: "s", DenoRuntimeImage: "excalibase/deno-runtime"}, wantErr: "tag"},
		"docker mode runs no pod":   {cfg: AppConfig{AppTmpPercent: 25, ProvisionerMode: "docker"}},
		"docker mode, bad override": {cfg: AppConfig{AppTmpPercent: 25, ProvisionerMode: "docker", DenoRuntimeImage: "x:latest"}, wantErr: "accepted"},
		"no functions secret":       {cfg: AppConfig{AppTmpPercent: 25, ProvisionerMode: "k8s"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.cfg.CheckDenoRuntimeImage()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("got %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want an error naming %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadDenoRuntimeImageKeepsTheValueItWillValidate(t *testing.T) {
	t.Setenv("DENO_RUNTIME_IMAGE", " "+pinnedDenoImage+"\n")
	cfg := Load()
	if cfg.DenoRuntimeImage != " "+pinnedDenoImage+"\n" {
		t.Fatalf("Load changed the image to %q", cfg.DenoRuntimeImage)
	}
	cfg.ProvisionerMode, cfg.DenoRuntimeSecret = "k8s", "s"
	if err := cfg.CheckDenoRuntimeImage(); err == nil {
		t.Fatal("an image with stray whitespace was accepted")
	}
}
