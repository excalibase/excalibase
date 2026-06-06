package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/schema"
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
	return s.store.Save(inst)
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
	spec["imageName"] = fmt.Sprintf("ghcr.io/cloudnative-pg/postgresql:%s", newVersion)

	return s.k8sClient.ApplyCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// CloneDatabase creates a new cluster using pg_basebackup from the source.
func (s *ProvisioningService) CloneDatabase(ctx context.Context, projectID string, req domain.CloneRequest) (*domain.ProvisioningResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}

	newNamespace := fmt.Sprintf("%s-%s", inst.OrgID, req.NewProjectName)

	if err := s.k8sClient.CreateNamespace(ctx, newNamespace); err != nil {
		return nil, fmt.Errorf("create clone namespace: %w", err)
	}

	cloneObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      req.NewProjectName + postgresSuffix,
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
		ProjectID:    req.NewProjectName,
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

// RotateCredentials generates a new password and updates the database.
func (s *ProvisioningService) RotateCredentials(ctx context.Context, projectID string) (*domain.CredentialsResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}

	newPassword := generatePassword(48)
	pod := projectID + "-postgres-1"
	sql := fmt.Sprintf("ALTER USER %s PASSWORD %s", schema.QuoteIdent(inst.Username), schema.QuoteLiteral(newPassword))

	_, err = s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"psql", "-U", "postgres", "-c", sql})
	if err != nil {
		return nil, fmt.Errorf("rotate password: %w", err)
	}

	inst.Password = newPassword
	if err := s.store.Save(inst); err != nil {
		log.Printf("WARN: failed to persist instance state: %v", err)
	}

	return s.GetCredentials(projectID)
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
	return s.store.Save(inst)
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
	return s.store.Save(inst)
}

func generatePassword(length int) string {
	b := make([]byte, length)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:length]
}

// orgIDPattern allows lowercase alphanumerics, hyphens, and underscores so
// both DNS-style slugs and UUIDs (which include hyphens) pass through.
var orgIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// supportedPostgresVersions lists versions CNPG will accept. Keeping this explicit
// catches typos early (e.g. "9.2", "15.4") before we spend time on K8s operations.
var supportedPostgresVersions = map[string]bool{
	"14": true, "15": true, "16": true, "17": true,
}

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

	if req.PostgresVersion != "" && !supportedPostgresVersions[req.PostgresVersion] {
		return fmt.Errorf("postgres version %q is not supported (allowed: 14, 15, 16, 17)", req.PostgresVersion)
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
