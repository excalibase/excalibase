package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	if inst, err := s.GetInstance(projectID); err == nil && inst.DBType == domain.MySQL {
		return s.upgradeMySQL(ctx, projectID, newVersion)
	}
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

	// The fetched object carries its resourceVersion, which a create-first
	// apply is refused for; an update keeps the concurrency check.
	return s.k8sClient.UpdateCRD(ctx, k8s.CNPGClusterGVR, inst.Namespace, existing)
}

// ErrLogsUnavailable is a log backend that did not answer.
var ErrLogsUnavailable = errors.New("the database's logs are not available right now")

var errLogsOnSingleHost = fmt.Errorf("%w: the log search reads Kubernetes pod logs; this installation runs its databases on a single host", ErrLogsUnavailable)

// maxLokiAnswer bounds one Loki reply: 5000 lines with their labels.
const maxLokiAnswer = 32 << 20

// ErrInvalidLogSearch is a search term the log read cannot use.
var ErrInvalidLogSearch = errors.New("the log search term must be printable text of at most 200 characters")

// maxLogSearchTerm bounds the Logs page's search term.
const maxLogSearchTerm = 200

// defaultLogSearchWindow is how far back stored logs are read when no range is asked.
const defaultLogSearchWindow = time.Hour

// LogSearch narrows a read of a project's database log.
type LogSearch struct {
	Lines int
	// Term is matched literally and case-insensitively; empty keeps every line.
	Term string
	// Since is how far back Loki is searched; zero is an hour. The live pod log
	// has only what the pod printed since it started.
	Since time.Duration
}

// GetLogs returns the last N lines of the project's database pod log.
func (s *ProvisioningService) GetLogs(ctx context.Context, projectID string, lines int) (string, error) {
	return s.SearchLogs(ctx, projectID, LogSearch{Lines: lines})
}

// SearchLogs returns the last matching lines of the project's database log.
// Prefers Loki when configured (it keeps logs across pod restarts) and reads
// the pod's log through the Kubernetes API when Loki is unset or does not answer.
// Either way only the project's own namespace is read.
func (s *ProvisioningService) SearchLogs(ctx context.Context, projectID string, search LogSearch) (string, error) {
	if err := validateLogSearchTerm(search.Term); err != nil {
		return "", err
	}
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return "", err
	}
	if s.k8sClient == nil || inst.DeploymentMode == domain.ModeDocker {
		return "", errLogsOnSingleHost
	}

	lokiAnswered := false
	if s.lokiURL != "" {
		out, err := s.getLogsFromLoki(ctx, databaseLogSelector(inst), projectID, search)
		if err == nil && out != "" {
			return out, nil
		}
		// No lines yet (a pod the collector has not shipped) or no answer: the live pod log.
		lokiAnswered = err == nil
	}

	out, err := s.primaryPodLog(ctx, inst, int64(clampLogLines(search.Lines)))
	if err != nil {
		log.Printf("logs of %s: pod log: %v", projectID, err)
		if lokiAnswered {
			return "", nil
		}
		return "", ErrLogsUnavailable
	}
	return filterLogLines(out, search.Term), nil
}

// primaryPodLog is the live log of the database container on the primary.
// Never a command on /controller/log/postgres.csv: CNPG's collector reads that pipe.
func (s *ProvisioningService) primaryPodLog(ctx context.Context, inst *domain.DatabaseInstance, lines int64) (string, error) {
	if inst.DBType != domain.MySQL {
		return s.k8sClient.PodLogTail(ctx, inst.Namespace, inst.ProjectID+primaryPodSuffix, "postgres", lines)
	}
	// MOCO moves the primary on failover; its cluster status names the current one.
	cluster, err := s.k8sClient.GetCRD(ctx, k8s.MOCOClusterGVR, inst.Namespace, k8s.MySQLClusterName(inst.ProjectID))
	if err != nil {
		return "", err
	}
	return s.k8sClient.PodLogTail(ctx, inst.Namespace, k8s.MySQLPrimaryPod(cluster), mysqlContainer, lines)
}

// clampLogLines is how many lines one search returns, from Loki or the pod log.
func clampLogLines(lines int) int {
	if lines <= 0 || lines > 5000 {
		return 100
	}
	return lines
}

func validateLogSearchTerm(term string) error {
	if len(term) > maxLogSearchTerm || !utf8.ValidString(term) {
		return ErrInvalidLogSearch
	}
	for _, r := range term {
		if unicode.IsControl(r) {
			return ErrInvalidLogSearch
		}
	}
	return nil
}

// filterLogLines keeps the lines containing term, ignoring case.
func filterLogLines(logs, term string) string {
	if term == "" {
		return logs
	}
	needle := strings.ToLower(term)
	var sb strings.Builder
	for _, line := range strings.Split(logs, "\n") {
		if line != "" && strings.Contains(strings.ToLower(line), needle) {
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// databaseLogSelector selects the project's database pods in its own
// namespace, by labels the collector sets from Kubernetes metadata.
func databaseLogSelector(inst *domain.DatabaseInstance) string {
	if inst.DBType == domain.MySQL {
		return mysqlLogSelector(inst.Namespace, inst.ProjectID)
	}
	return postgresLogSelector(inst.Namespace, inst.ProjectID)
}

func postgresLogSelector(namespace, projectID string) string {
	return fmt.Sprintf("{namespace=%s,cnpg_io_cluster=%s}", strconv.Quote(namespace), strconv.Quote(projectID+postgresSuffix))
}

// mysqlLogSelector is MOCO's mysqld container, which writes the error log to
// stdout; the agent and slow-log sidecars share its pod.
func mysqlLogSelector(namespace, projectID string) string {
	return fmt.Sprintf("{namespace=%s,app_kubernetes_io_instance=%s,container=%s}",
		strconv.Quote(namespace), strconv.Quote(k8s.MySQLClusterName(projectID)), strconv.Quote(mysqlContainer))
}

// projectLogQL keeps the selected lines containing term: a quoted literal,
// case-insensitive regex, so nothing in the term can leave the filter string
// or widen the selector.
func projectLogQL(selector, term string) string {
	if term == "" {
		return selector
	}
	return selector + " |~ " + strconv.Quote("(?i)"+regexp.QuoteMeta(term))
}

// getLogsFromLoki queries Loki for the last matching lines of the selected
// pods. Returns plain-text concatenated lines (one per row) so the existing
// handler-side response shape stays unchanged.
func (s *ProvisioningService) getLogsFromLoki(ctx context.Context, selector, projectID string, search LogSearch) (string, error) {
	lines := clampLogLines(search.Lines)
	window := search.Since
	if window <= 0 {
		window = defaultLogSearchWindow
	}
	logql := projectLogQL(selector, search.Term)
	end := time.Now()
	start := end.Add(-window)
	u := fmt.Sprintf("%s/loki/api/v1/query_range?query=%s&start=%d&end=%d&limit=%d&direction=backward",
		s.lokiURL, urlEscape(logql), start.UnixNano(), end.UnixNano(), lines)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", u, nil)
	if err != nil {
		log.Printf("logs of %s: loki request: %v", projectID, err)
		return "", ErrLogsUnavailable
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// The client error names Loki's internal address: log it, answer only that logs are unavailable.
		log.Printf("logs of %s: loki query: %v", projectID, err)
		return "", ErrLogsUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("logs of %s: loki returned %d", projectID, resp.StatusCode)
		return "", ErrLogsUnavailable
	}

	var body struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLokiAnswer)).Decode(&body); err != nil {
		log.Printf("logs of %s: loki decode: %v", projectID, err)
		return "", ErrLogsUnavailable
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
	cfg, err := normalizeMaintenanceWindow(cfg)
	if err != nil {
		return err
	}
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
	if err := validateProjectIdentity(req); err != nil {
		return err
	}
	return validateDatabaseRequest(req)
}

// validateProjectIdentity checks the project's name and organisation.
func validateProjectIdentity(req domain.ProvisioningRequest) error {
	if strings.TrimSpace(req.ProjectName) == "" {
		return fmt.Errorf("project name is required")
	}
	if _, ok := domain.NormalizeProjectName(req.ProjectName); !ok {
		return fmt.Errorf("project name must be %d characters or fewer", domain.MaxProjectNameLength)
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
	return nil
}

// validateDatabaseRequest checks the settings a database is created with.
func validateDatabaseRequest(req domain.ProvisioningRequest) error {
	if req.DBType == domain.MySQL {
		return validateMySQLDatabaseRequest(req)
	}
	if req.MySQLVersion != "" {
		return fmt.Errorf("mysqlVersion applies only to a MySQL project")
	}
	// EXC-408: the major is required and is checked against the image
	// catalogue. There is deliberately no default: silently provisioning on a
	// major the caller did not ask for is how a project ends up on bits nobody
	// chose.
	version := strings.TrimSpace(req.PostgresVersion)
	if version == "" {
		return fmt.Errorf("postgresVersion is required (supported: %s)", config.SupportedPostgresMajorsMessage())
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
	return validateOwnerAndTags(req)
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
// vault path segment and URL path.
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
