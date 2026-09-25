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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	postgresSuffix = "-postgres"
	errGetCRDFmt   = "get cluster CRD: %w"
)

// ScaleTier changes the instance count and resources by patching the CNPG Cluster CRD.
func (s *ProvisioningService) ScaleTier(ctx context.Context, projectID string, newTier domain.TierType) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	tc, err := s.tierConfig(ctx, newTier)
	if err != nil {
		return err
	}

	// Get existing cluster CRD and update spec
	clusterName := projectID + postgresSuffix
	existing, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf(errGetCRDFmt, err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	spec["instances"] = int64(tc.Instances)
	spec["resources"] = map[string]interface{}{
		"requests": map[string]interface{}{"memory": tc.Memory, "cpu": tc.CPU},
		"limits":   map[string]interface{}{"memory": tc.Memory, "cpu": tc.CPU},
	}

	if err := s.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing); err != nil {
		return fmt.Errorf("patch cluster CRD: %w", err)
	}

	inst.Tier = newTier
	return s.store.Update(inst)
}

// ResizeStorage patches the CNPG Cluster CRD storage size.
func (s *ProvisioningService) ResizeStorage(ctx context.Context, projectID, newSize string) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	clusterName := projectID + postgresSuffix
	existing, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf(errGetCRDFmt, err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	storage := spec["storage"].(map[string]interface{})
	storage["size"] = newSize

	return s.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// UpgradeVersion patches the CNPG Cluster CRD imageName to trigger a rolling restart.
func (s *ProvisioningService) UpgradeVersion(ctx context.Context, projectID, newVersion string) error {
	// Resolve before touching the cluster: an unsupported major must fail
	// without having patched anything.
	image, err := config.PostgresImage(newVersion)
	if err != nil {
		return err
	}

	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
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

// CloneDatabase creates a new cluster using pg_basebackup from the source.
func (s *ProvisioningService) CloneDatabase(ctx context.Context, projectID string, req domain.CloneRequest) (*domain.ProvisioningResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}

	// The clone's id is generated exactly as a provision's is, so the caller
	// cannot point a clone at an id another project already holds (EXC-415).
	// req.NewProjectName stays what it says: a display name.
	cloneRef, err := s.generateUniqueProjectRef()
	if err != nil {
		return nil, err
	}

	newNamespace := fmt.Sprintf("%s-%s", inst.OrgID, cloneRef)

	if err := s.k8sClient.CreateProjectNamespace(ctx, newNamespace, inst.OrgID); err != nil {
		return nil, fmt.Errorf("create clone namespace: %w", err)
	}

	cloneObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      cloneRef + postgresSuffix,
				"namespace": newNamespace,
			},
			"spec": map[string]interface{}{
				"instances": int64(1),
				"storage":   map[string]interface{}{"size": "5Gi"},
				"bootstrap": map[string]interface{}{
					"pg_basebackup": map[string]interface{}{
						"source": projectID + postgresSuffix,
					},
				},
				"externalClusters": []interface{}{
					map[string]interface{}{
						"name": projectID + postgresSuffix,
						"connectionParameters": map[string]interface{}{
							"host":   inst.Host,
							"dbname": inst.DatabaseName,
						},
					},
				},
			},
		},
	}

	if err := s.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, newNamespace, cloneObj); err != nil {
		return nil, fmt.Errorf("apply clone CRD: %w", err)
	}

	now := &domain.FlexTime{Time: time.Now()}
	return &domain.ProvisioningResponse{
		ProjectID:    cloneRef,
		ProjectName:  req.NewProjectName,
		Status:       "CLONING",
		CurrentStage: domain.StageWaitingForReady,
		Namespace:    newNamespace,
		CreatedAt:    now,
	}, nil
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
func (s *ProvisioningService) SetMaintenanceWindow(projectID string, cfg domain.MaintenanceWindowConfig) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}
	inst.MaintenanceWindow = cfg.Window
	inst.MaintenanceWindowDurationMinutes = &cfg.DurationMinutes
	inst.AutoMinorVersionUpgrade = &cfg.AutoUpgrade
	return s.store.Update(inst)
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

// UpdateParameters patches PostgreSQL parameters on the CNPG Cluster CRD (triggers rolling restart).
func (s *ProvisioningService) UpdateParameters(ctx context.Context, projectID string, params map[string]string) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	clusterName := projectID + postgresSuffix
	existing, err := s.k8sClient.GetCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, clusterName)
	if err != nil {
		return fmt.Errorf(errGetCRDFmt, err)
	}

	spec := existing.Object["spec"].(map[string]interface{})
	pg := spec["postgresql"].(map[string]interface{})
	pgParams, ok := pg["parameters"].(map[string]interface{})
	if !ok {
		pgParams = make(map[string]interface{})
		pg["parameters"] = pgParams
	}

	for k, v := range params {
		pgParams[k] = v
	}

	return s.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// EnablePooler creates a CNPG Pooler CRD (PgBouncer) for connection pooling.
func (s *ProvisioningService) EnablePooler(ctx context.Context, projectID string, settings domain.PoolerSettings) error {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return err
	}

	poolMode := "transaction"
	if settings.PoolMode != "" {
		poolMode = settings.PoolMode
	}
	poolSize := int64(20)
	if settings.PoolSize > 0 {
		poolSize = int64(settings.PoolSize)
	}

	poolerObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Pooler",
			"metadata": map[string]interface{}{
				"name":      projectID + "-postgres-pooler",
				"namespace": inst.Namespace,
			},
			"spec": map[string]interface{}{
				"cluster": map[string]interface{}{
					"name": projectID + postgresSuffix,
				},
				"instances": int64(1),
				"type":      "rw",
				"pgbouncer": map[string]interface{}{
					"poolMode": poolMode,
					"parameters": map[string]interface{}{
						"default_pool_size": fmt.Sprintf("%d", poolSize),
					},
				},
			},
		},
	}

	poolerGVR := k8s.CNPGClusterGVR // same group, different resource
	poolerGVR.Resource = "poolers"

	if err := s.k8sClient.ApplyCRD(ctx, poolerGVR, inst.Namespace, poolerObj); err != nil {
		return fmt.Errorf("apply pooler CRD: %w", err)
	}

	enabled := true
	inst.PoolerEnabled = &enabled
	inst.PoolerHost = fmt.Sprintf("%s-postgres-pooler.%s.svc.cluster.local", projectID, inst.Namespace)
	return s.store.Update(inst)
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
