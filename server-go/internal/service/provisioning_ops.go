package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const (
	postgresSuffix = "-postgres"
	errGetCRDFmt   = "get cluster CRD: %w"
)

// UpgradeVersion re-pins the CNPG Cluster onto the catalogue's image for the
// project's own major, which triggers a rolling restart onto the newest patch.
func (s *ProvisioningService) UpgradeVersion(ctx context.Context, projectID, newVersion string) error {
	// Resolve before touching the cluster: an unsupported major must fail
	// without having patched anything.
	image, err := config.PostgresImage(newVersion)
	if err != nil {
		return err
	}

	inst, release, err := s.holdProject(ctx, projectID, OperationUpgrade, requireActive)
	if err != nil {
		return err
	}
	defer release()
	// A major change needs pg_upgrade, which is not verified for our operator.
	if inst.PostgresVersion != newVersion {
		return fmt.Errorf("project %s runs postgres %q; moving it to major %q is a major upgrade, which is not supported", projectID, inst.PostgresVersion, newVersion)
	}

	clusterName := projectID + postgresSuffix
	existing, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf(errGetCRDFmt, err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	spec["imageName"] = image

	return s.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// GetLogs returns the last N lines of the project's postgres pod log.
// Prefers Loki when configured (sees logs across pod restarts, doesn't hang
// on busy minikube apiservers); falls back to kubectl-exec tail in dev/test
// environments without Loki.
func (s *ProvisioningService) GetLogs(ctx context.Context, projectID string, lines int) (string, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return "", err
	}

	if s.lokiURL != "" {
		return s.getLogsFromLoki(ctx, inst.Namespace, projectID, lines)
	}

	pod := projectID + "-postgres-1"
	return s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"sh", "-c", fmt.Sprintf("tail -%d /controller/log/postgres.csv", lines)})
}

// getLogsFromLoki queries Loki for the last N postgres pod lines from the
// project's namespace. Returns plain-text concatenated lines (one per row)
// so the existing handler-side response shape stays unchanged.
func (s *ProvisioningService) getLogsFromLoki(ctx context.Context, namespace, projectID string, lines int) (string, error) {
	if lines <= 0 || lines > 5000 {
		lines = 100
	}
	logql := fmt.Sprintf(`{namespace=%q,cnpg_io_cluster=%q}`, namespace, projectID+postgresSuffix)
	end := time.Now()
	start := end.Add(-1 * time.Hour)
	u := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=%d&direction=backward",
		s.lokiURL, urlEscape(logql), start.UnixNano(), end.UnixNano(), lines)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, "GET", u, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("loki query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("loki returned %d", resp.StatusCode)
	}

	var body struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("loki decode: %w", err)
	}

	var sb strings.Builder
	for _, stream := range body.Data.Result {
		for _, v := range stream.Values {
			if len(v) >= 2 {
				sb.WriteString(v[1])
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String(), nil
}

func urlEscape(s string) string {
	return url.QueryEscape(s)
}

// SetMaintenanceWindow sets the maintenance window config.
func (s *ProvisioningService) SetMaintenanceWindow(ctx context.Context, projectID string, cfg domain.MaintenanceWindowConfig) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationMaintenance, anyStatus)
	if err != nil {
		return err
	}
	defer release()
	inst.MaintenanceWindow = cfg.Window
	inst.MaintenanceWindowDurationMinutes = &cfg.DurationMinutes
	inst.AutoMinorVersionUpgrade = &cfg.AutoUpgrade
	return s.store.UpdateIfStatus(inst, inst.Status)
}

// GetMaintenanceWindow returns the maintenance window config.
func (s *ProvisioningService) GetMaintenanceWindow(projectID string) (*domain.MaintenanceWindowConfig, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}
	dur := 0
	if inst.MaintenanceWindowDurationMinutes != nil {
		dur = *inst.MaintenanceWindowDurationMinutes
	}
	autoUpgrade := false
	if inst.AutoMinorVersionUpgrade != nil {
		autoUpgrade = *inst.AutoMinorVersionUpgrade
	}
	return &domain.MaintenanceWindowConfig{
		Window:          inst.MaintenanceWindow,
		DurationMinutes: dur,
		AutoUpgrade:     autoUpgrade,
	}, nil
}

// ErrProjectNotActive refuses a cluster change on a project that is not
// running: paused, failed, being built, deleted or restored.
var ErrProjectNotActive = errors.New("this change needs an active project")

// statusAdmission decides, under the lease, whether an operation may act on
// a project in the given status.
type statusAdmission func(projectID, status string) error

// anyStatus admits every status; the store still refuses a row a teardown owns.
func anyStatus(string, string) error { return nil }

// requireServable refuses a project being deleted or restored.
func requireServable(projectID, status string) error {
	if domain.IsNotServable(status) {
		return fmt.Errorf("%w: %s", notServableErr(status), projectID)
	}
	return nil
}

// requireActive admits only a settled, running project.
func requireActive(projectID, status string) error {
	if status != string(domain.StatusActive) {
		return fmt.Errorf("project %s is %s; %w", projectID, status, ErrProjectNotActive)
	}
	return nil
}

// holdProject takes the project's lifecycle lease and reads the row under it,
// so the operation acts on the project as it is now rather than as a caller
// saw it. The row's status is what the operation's write is then pinned to.
func (s *ProvisioningService) holdProject(ctx context.Context, projectID string,
	op ProjectOperation, admit statusAdmission) (*domain.DatabaseInstance, func(), error) {

	release, claimed, err := s.claimer().Claim(ctx, projectID, op)
	if err != nil {
		return nil, nil, fmt.Errorf("claim project for %s: %w", op, err)
	}
	if !claimed {
		return nil, nil, fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	inst, err := s.GetInstance(projectID)
	if err == nil {
		err = admit(projectID, inst.Status)
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	return inst, release, nil
}

func generatePassword(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:length]
}

// orgIDPattern allows lowercase alphanumerics, hyphens, and underscores so
// both DNS-style slugs and UUIDs (which include hyphens) pass through.
var orgIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// validateProvisioningRequest performs cheap preflight checks that catch bad input
// before any side effect. Fails return plain errors (not StageError) because no
// project record exists yet — callers surface them as 400 Bad Request.
func validateProvisioningRequest(req domain.ProvisioningRequest) error {
	name := strings.TrimSpace(req.ProjectName)
	if name == "" {
		return fmt.Errorf("project name is required")
	}
	if len(name) > 100 {
		return fmt.Errorf("project name must be 100 characters or fewer")
	}

	org := strings.TrimSpace(req.OrgID)
	if org == "" {
		return fmt.Errorf("org id is required")
	}
	if len(org) > 36 {
		return fmt.Errorf("org id must be 36 characters or fewer")
	}
	if !orgIDPattern.MatchString(org) {
		return fmt.Errorf("org id must contain only lowercase letters, digits, hyphen, or underscore")
	}

	// EXC-408: the major is required and is checked against the image
	// catalogue. There is deliberately no default: silently provisioning on a
	// major the caller did not ask for is how a project ends up on bits nobody
	// chose.
	version := strings.TrimSpace(req.PostgresVersion)
	if version == "" {
		return fmt.Errorf("postgres version is required (supported: %s)", config.SupportedPostgresMajorsMessage())
	}
	if _, ok := config.LookupPostgresMajor(version); !ok {
		return fmt.Errorf("postgres version %q is not supported (supported: %s)", version, config.SupportedPostgresMajorsMessage())
	}
	if req.DocumentDB && !config.DocumentDBSupported(version) {
		return fmt.Errorf("DocumentDB is not available on postgres %s (available on: %s)", version, config.DocumentDBMajorsMessage())
	}

	if req.Backup != nil && req.Backup.Enabled {
		if req.Backup.Retention < 0 {
			return fmt.Errorf("backup retention must be non-negative")
		}
		if req.Backup.Retention > 365 {
			return fmt.Errorf("backup retention must be 365 days or fewer")
		}
	}
	return nil
}

// canonicalPostgresMajor returns the catalogue's spelling of the major a
// validated request names. What is stored has to be the catalogue's exact
// value, not whatever the caller typed, because every later resolution — the
// restore image above all — is an exact lookup against the catalogue.
func canonicalPostgresMajor(version string) (string, error) {
	entry, ok := config.LookupPostgresMajor(version)
	if !ok {
		return "", fmt.Errorf("postgres version %q is not supported (supported: %s)", version, config.SupportedPostgresMajorsMessage())
	}
	return entry.Major, nil
}

// allocateProjectID returns a fresh project id no registered project holds.
// Every project id in the platform — provisioned or restored — comes
// from here, so no caller can name the project it is creating.
func allocateProjectID(store storage.InstanceStore) (string, error) {
	if store == nil {
		return "", errors.New("allocate project id: instance store not configured")
	}
	for i := 0; i < projectRefAttempts; i++ {
		ref := generateProjectRef()
		existing, err := store.FindByProjectID(ref)
		if err != nil {
			return "", fmt.Errorf("check project id availability: %w", err)
		}
		if existing == nil {
			return ref, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique project ref after %d attempts", projectRefAttempts)
}

// projectRefAttempts bounds the collision retry loop.
const projectRefAttempts = 5

// ErrProjectIDTaken is returned when an operation would build a new project
// on an id another project already holds.
var ErrProjectIDTaken = errors.New("target project id is already registered")

// ErrTargetProjectIDMissing is returned when a restore reaches an adapter
// without a platform-generated target id.
var ErrTargetProjectIDMissing = errors.New("restore: target project id was not allocated")

// assertProjectIDAvailable is the last gate before a restore creates
// namespaces, containers or vault entries: the id must be allocated by the
// platform and still free. Callers run it before any side effect (EXC-415).
func assertProjectIDAvailable(store storage.InstanceStore, projectID string) error {
	if projectID == "" {
		return ErrTargetProjectIDMissing
	}
	if store == nil {
		return errors.New("restore: instance store not configured")
	}
	existing, err := store.FindByProjectID(projectID)
	if err != nil {
		return fmt.Errorf("check target project id: %w", err)
	}
	if existing != nil {
		return ErrProjectIDTaken
	}
	return nil
}

// generateProjectRef returns an opaque immutable identifier for a project,
// e.g. "proj-a3k9fx7b2k". Hyphen separator (not underscore) so the ref is a
// valid DNS-1123 label for use directly as K8s namespace, CNPG cluster,
// vault path segment, pgdog config, and URL path.
// 10-char alphabet [a-z0-9] → 36^10 ≈ 3.6e15 combinations; collision probability
// with 1M projects is ~10^-4, defended by a retry-on-collision check in the caller.
func generateProjectRef() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 10)
	rand.Read(b)
	out := make([]byte, 10)
	for i, x := range b {
		out[i] = alphabet[int(x)%len(alphabet)]
	}
	return "proj-" + string(out)
}
