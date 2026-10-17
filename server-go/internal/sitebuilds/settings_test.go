package sitebuilds

import (
	"strings"
	"testing"
)

func validSettings() Settings {
	return Settings{Branch: "main", OutputDir: "dist", NodeVersion: "22"}
}

func TestSettings_Normalise(t *testing.T) {
	settings := Settings{Branch: " main ", RootDir: "./apps/web/", OutputDir: "dist/", NodeVersion: "", BuildCommand: " npm run build "}
	normalised, err := settings.Normalise()
	if err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if normalised.Branch != "main" || normalised.RootDir != "apps/web" || normalised.OutputDir != "dist" ||
		normalised.NodeVersion != DefaultNodeVersion || normalised.BuildCommand != "npm run build" {
		t.Errorf("normalised %+v", normalised)
	}
}

func TestSettings_Refusals(t *testing.T) {
	cases := map[string]func(*Settings){
		"no branch":               func(s *Settings) { s.Branch = "" },
		"branch with ..":          func(s *Settings) { s.Branch = "a..b" },
		"branch with space":       func(s *Settings) { s.Branch = "a b" },
		"branch starting with -":  func(s *Settings) { s.Branch = "-x" },
		"root escaping":           func(s *Settings) { s.RootDir = "../other" },
		"absolute root":           func(s *Settings) { s.RootDir = "/etc" },
		"output escaping":         func(s *Settings) { s.OutputDir = "dist/../../x" },
		"no output":               func(s *Settings) { s.OutputDir = "" },
		"output is the repo root": func(s *Settings) { s.OutputDir = "." },
		"unknown node":            func(s *Settings) { s.NodeVersion = "16" },
		"multi-line command":      func(s *Settings) { s.BuildCommand = "npm run build\ncurl evil" },
		"huge command":            func(s *Settings) { s.InstallCommand = strings.Repeat("x", 1025) },
		"bad env name":            func(s *Settings) { s.Env = map[string]string{"1BAD": "x"} },
		"reserved env":            func(s *Settings) { s.Env = map[string]string{"HOME": "/x"} },
		"kubernetes env":          func(s *Settings) { s.Env = map[string]string{"KUBERNETES_SERVICE_HOST": "1.2.3.4"} },
		"huge env value":          func(s *Settings) { s.Env = map[string]string{"BIG": strings.Repeat("x", MaxEnvValueBytes+1)} },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			settings := validSettings()
			breakIt(&settings)
			if _, err := settings.Normalise(); err == nil {
				t.Fatal("want a refusal")
			}
		})
	}
}

func TestSettings_TooManyVariables(t *testing.T) {
	settings := validSettings()
	settings.Env = map[string]string{}
	for i := 0; i <= MaxEnvVars; i++ {
		settings.Env["VAR_"+strings.Repeat("A", i%20)+string(rune('A'+i%26))+strings.Repeat("B", i/26)] = "v"
	}
	if _, err := settings.Normalise(); err == nil {
		t.Fatal("more variables than allowed must be refused")
	}
}

func TestNodeImage(t *testing.T) {
	// The default is every offered major pinned by digest, so a moved tag never changes a build.
	for _, version := range NodeVersions {
		if got := NodeImage("", version); !strings.HasPrefix(got, "node:"+version+"-bookworm@sha256:") || len(got) != len("node:"+version+"-bookworm@sha256:")+64 {
			t.Errorf("Node %s runs %s", version, got)
		}
	}
	if got := NodeImage("mirror.example.com/library/node", "20"); got != "mirror.example.com/library/node:20-bookworm" {
		t.Errorf("got %s", got)
	}
}

func TestDeployLabel(t *testing.T) {
	build := &Build{CommitSHA: strings.Repeat("a", 40), CommitMessage: "feat: new header\n\nlong body", Settings: Settings{Branch: "main"}}
	label := build.DeployLabel()
	if label != "aaaaaaa main: feat: new header" {
		t.Errorf("label %q", label)
	}
	build.CommitMessage = strings.Repeat("é", 300)
	if len(build.DeployLabel()) > 200 {
		t.Errorf("label too long: %d", len(build.DeployLabel()))
	}
	build.CommitMessage = "tab\there"
	if strings.ContainsAny(build.DeployLabel(), "\t\n\r") {
		t.Error("control characters must not reach the deploy label")
	}
}

func TestRedact(t *testing.T) {
	logs := "token=s3cr3t-value and again s3cr3t-value; short=ab"
	got := redact(logs, map[string]string{"API_KEY": "s3cr3t-value", "SHORT": "ab"})
	if strings.Contains(got, "s3cr3t-value") {
		t.Errorf("secret left in the log: %s", got)
	}
	if !strings.Contains(got, "[redacted API_KEY]") {
		t.Errorf("got %s", got)
	}
	if !strings.Contains(got, "short=ab") {
		t.Error("values too short to be meaningful are left, or every 'ab' would vanish")
	}
}

func TestFailureMessage(t *testing.T) {
	limits := Limits{Memory: "2Gi", Disk: "5Gi", TimeoutSeconds: 600}
	cases := map[FailureKind]string{
		FailOutOfMemory: "2Gi of memory",
		FailTimeout:     "10 minutes",
		FailDisk:        "5Gi of disk",
		FailNoOutput:    "dist",
	}
	for kind, want := range cases {
		message := failureMessage(RunStatus{Phase: RunFailed, Failure: kind, Detail: "d"}, limits, Settings{OutputDir: "dist"})
		if !strings.Contains(message, want) {
			t.Errorf("%s: %q lacks %q", kind, message, want)
		}
	}
}

// A log tail cut inside a secret leaves its end right after the cut mark; that end is redacted too.
func TestRedactCoversASecretCutByTheTail(t *testing.T) {
	got := redact("== build ==\n…cr3t-value printed\n", map[string]string{"API_KEY": "s3cr3t-value"})
	if strings.Contains(got, "cr3t-value") || !strings.Contains(got, "…[redacted API_KEY]") {
		t.Fatalf("got %q", got)
	}
}
