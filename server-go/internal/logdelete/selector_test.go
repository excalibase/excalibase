package logdelete

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestProjectSelector_KubernetesIsTheProjectNamespace(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "proj-abc123", OrgID: "org1", Namespace: "org1-proj-abc123", DeploymentMode: domain.ModeK8s}
	got, err := ProjectSelector(inst)
	if err != nil || got != `{namespace="org1-proj-abc123"}` {
		t.Fatalf("selector = %q %v", got, err)
	}
	// An empty mode is how rows written before the mode existed read: Kubernetes.
	inst.DeploymentMode = ""
	if got, err := ProjectSelector(inst); err != nil || got != `{namespace="org1-proj-abc123"}` {
		t.Fatalf("legacy row selector = %q %v", got, err)
	}
}

func TestProjectSelector_SingleHostIsTheProjectLabel(t *testing.T) {
	inst := &domain.DatabaseInstance{ProjectID: "proj-abc123", Namespace: "excalibase-db-proj-abc123", DeploymentMode: domain.ModeDocker}
	got, err := ProjectSelector(inst)
	if err != nil || got != `{project_id="proj-abc123"}` {
		t.Fatalf("selector = %q %v", got, err)
	}
}

// A namespace that is not the project's own is never selected: the platform
// namespace, another project's, or anything a label value could escape from.
func TestProjectSelector_RefusesANamespaceThatIsNotTheProjects(t *testing.T) {
	cases := map[string]*domain.DatabaseInstance{
		"empty namespace":       {ProjectID: "proj-a", Namespace: ""},
		"platform namespace":    {ProjectID: "proj-a", Namespace: "excalibase"},
		"another project":       {ProjectID: "proj-a", Namespace: "org1-proj-b"},
		"suffix without org":    {ProjectID: "proj-a", Namespace: "-proj-a"},
		"bare project id":       {ProjectID: "proj-a", Namespace: "proj-a"},
		"quote in namespace":    {ProjectID: "proj-a", Namespace: `x"}-proj-a`},
		"regex in namespace":    {ProjectID: "proj-a", Namespace: ".*-proj-a"},
		"upper case":            {ProjectID: "proj-a", Namespace: "ORG-proj-a"},
		"empty project id":      {ProjectID: "", Namespace: "org1-"},
		"project id with quote": {ProjectID: `a"`, Namespace: `org1-a"`},
		"too long":              {ProjectID: "proj-a", Namespace: strings.Repeat("a", 60) + "-proj-a"},
	}
	for name, inst := range cases {
		t.Run(name, func(t *testing.T) {
			inst.DeploymentMode = domain.ModeK8s
			if got, err := ProjectSelector(inst); !errors.Is(err, ErrUnsafeSelector) {
				t.Errorf("selector = %q %v, want refused", got, err)
			}
		})
	}
}

func TestProjectSelector_RefusesAnUnsafeProjectIDOnASingleHost(t *testing.T) {
	for _, id := range []string{"", ".+", `a"} or {x="y`, "A", "-a", strings.Repeat("a", 64)} {
		inst := &domain.DatabaseInstance{ProjectID: id, DeploymentMode: domain.ModeDocker}
		if got, err := ProjectSelector(inst); !errors.Is(err, ErrUnsafeSelector) {
			t.Errorf("id %q: selector = %q %v, want refused", id, got, err)
		}
	}
	if _, err := ProjectSelector(nil); !errors.Is(err, ErrUnsafeSelector) {
		t.Errorf("nil instance: %v", err)
	}
	if _, err := ProjectSelector(&domain.DatabaseInstance{ProjectID: "a", DeploymentMode: "other"}); !errors.Is(err, ErrUnsafeSelector) {
		t.Errorf("unknown mode: %v", err)
	}
}

// CheckSelector is the last gate before Loki: exactly one exact-match label,
// nothing else. Empty, wildcard and regex selectors never reach the API.
func TestCheckSelector_AdmitsOnlyOneExactProjectLabel(t *testing.T) {
	for _, ok := range []string{`{namespace="org1-proj-a"}`, `{project_id="proj-a"}`} {
		if err := CheckSelector(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		``, `{}`, `{namespace=""}`, `{namespace=~".+"}`, `{namespace=~".*"}`, `{project_id!="x"}`,
		`{namespace="a",project_id="b"}`, `{namespace="a"} |= "x"`, ` {namespace="a"}`,
		`{namespace="a"}` + "\n", `{app="excalibase"}`, `{namespace="a.b"}`, `{namespace="-a"}`,
		`{namespace="a-"}`, `{project_id="A"}`, `{namespace='a'}`,
	} {
		if err := CheckSelector(bad); !errors.Is(err, ErrUnsafeSelector) {
			t.Errorf("%q admitted: %v", bad, err)
		}
	}
}

func TestStartFor_IsBoundedByCreationAndRetention(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	maxAge := 7 * 24 * time.Hour
	created := now.Add(-48 * time.Hour)
	// A young project: a day before it was created.
	if got := StartFor(created, now, maxAge); !got.Equal(created.Add(-24 * time.Hour)) {
		t.Errorf("young project start = %v", got)
	}
	// An old one: no further back than retention and two days of slack.
	old := now.Add(-365 * 24 * time.Hour)
	if got := StartFor(old, now, maxAge); !got.Equal(now.Add(-maxAge - 48*time.Hour)) {
		t.Errorf("old project start = %v", got)
	}
	// No creation time: the retention bound alone.
	if got := StartFor(time.Time{}, now, maxAge); !got.Equal(now.Add(-maxAge - 48*time.Hour)) {
		t.Errorf("unknown creation start = %v", got)
	}
	// Whole seconds: Loki takes unix seconds, and the status match compares them.
	if got := StartFor(created.Add(500*time.Millisecond), now, maxAge); got.Nanosecond() != 0 {
		t.Errorf("start %v has a fraction", got)
	}
	// Never before Loki's schema start.
	if got := StartFor(time.Time{}, now, 20*365*24*time.Hour); got.Before(schemaStart) {
		t.Errorf("start %v is before the schema", got)
	}
}
