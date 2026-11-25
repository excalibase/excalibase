package imageref

import (
	"strings"
	"testing"
)

const digest = "sha256:7b2f9feb21b2a92b34c3f7b136399c619d460bf5d296215b8662a7a334126d9e"

func TestRequirePinned(t *testing.T) {
	cases := map[string]struct {
		ref     string
		wantErr string
	}{
		"release version":        {ref: "excalibase/deno-runtime:1.7.1"},
		"trunk tag":              {ref: "excalibase/deno-runtime:main-0a1b2c3"},
		"digest only":            {ref: "docker.io/excalibase/deno-runtime@" + digest},
		"tag and digest":         {ref: "excalibase/deno-runtime:1.7.1@" + digest},
		"latest with a digest":   {ref: "excalibase/deno-runtime:latest@" + digest},
		"registry with a port":   {ref: "registry.local:5000/deno-runtime:1.7.1"},
		"empty":                  {ref: "", wantErr: "not set"},
		"blank":                  {ref: "   ", wantErr: "whitespace"},
		"leading space":          {ref: " excalibase/deno-runtime:1.2.3", wantErr: "whitespace"},
		"trailing space":         {ref: "excalibase/deno-runtime:1.2.3 ", wantErr: "whitespace"},
		"trailing newline":       {ref: "excalibase/deno-runtime:1.2.3\n", wantErr: "whitespace"},
		"v prefixed version":     {ref: "excalibase/deno-runtime:v1.7.2"},
		"release branch build":   {ref: "excalibase/deno-runtime:release-1.7-0a1b2c3"},
		"main":                   {ref: "excalibase/deno-runtime:main", wantErr: "accepted"},
		"release line":           {ref: "excalibase/deno-runtime:release-1.7", wantErr: "accepted"},
		"minor only":             {ref: "excalibase/deno-runtime:1.7", wantErr: "accepted"},
		"major only":             {ref: "excalibase/deno-runtime:1", wantErr: "accepted"},
		"stable":                 {ref: "excalibase/deno-runtime:stable", wantErr: "accepted"},
		"short main sha":         {ref: "excalibase/deno-runtime:main-abc123", wantErr: "accepted"},
		"prerelease":             {ref: "excalibase/deno-runtime:1.7.2-rc.1", wantErr: "accepted"},
		"bare digest":            {ref: "@" + digest, wantErr: "name"},
		"two tags":               {ref: "excalibase/deno-runtime:a:b", wantErr: "tag"},
		"uppercase repository":   {ref: "excalibase/Deno-Runtime:1.2.3", wantErr: "lowercase"},
		"uppercase with digest":  {ref: "Excalibase/deno-runtime@" + digest, wantErr: "lowercase"},
		"latest":                 {ref: "excalibase/deno-runtime:latest", wantErr: "accepted"},
		"latest in any case":     {ref: "excalibase/deno-runtime:LATEST", wantErr: "accepted"},
		"no tag":                 {ref: "excalibase/deno-runtime", wantErr: "tag"},
		"registry port, no tag":  {ref: "registry.local:5000/deno-runtime", wantErr: "tag"},
		"empty tag":              {ref: "excalibase/deno-runtime:", wantErr: "tag"},
		"short digest":           {ref: "excalibase/deno-runtime@sha256:abc", wantErr: "digest"},
		"inner whitespace":       {ref: "excalibase/deno-runtime:1.7.1 --privileged", wantErr: "whitespace"},
		"digest of another kind": {ref: "excalibase/deno-runtime@md5:" + strings.Repeat("a", 64), wantErr: "digest"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := RequirePinned(tc.ref)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("RequirePinned(%q) = %v, want nil", tc.ref, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("RequirePinned(%q) = %v, want an error naming %q", tc.ref, err, tc.wantErr)
			}
		})
	}
}
