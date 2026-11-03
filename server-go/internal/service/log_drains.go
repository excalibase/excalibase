package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// Log drains (EXC-658): provisioning reads a project's database log from
// Loki with that project's own selector and leases batches to the forwarder
// pod, which only POSTs them. A drain's cursor moves on a delivered ack of
// the batch it leased.

const (
	MaxLogDrainsPerProject = 3
	// MaxLogDrainFailures in a row disable a drain until an Admin enables it.
	MaxLogDrainFailures = 10
)

var (
	// ErrLogDrainsUnavailable is an install or project without the log store.
	ErrLogDrainsUnavailable = errors.New("log forwarding needs the platform log store, which this project's logs are not in")
	ErrInvalidLogDrainAck   = errors.New("invalid log drain ack")
)

// LogDrainStore keeps drains, their cursors and leases.
type LogDrainStore interface {
	CreateLogDrain(ctx context.Context, drain *domain.LogDrain, limit int) error
	ListLogDrains(ctx context.Context, projectID string) ([]domain.LogDrain, error)
	GetLogDrain(ctx context.Context, projectID, id string) (*domain.LogDrain, error)
	FindLogDrain(ctx context.Context, id string) (*domain.LogDrain, error)
	UpdateLogDrain(ctx context.Context, drain *domain.LogDrain) error
	SetLogDrainEnabled(ctx context.Context, projectID, id string, enabled bool, cursor int64, at time.Time) error
	DeleteLogDrain(ctx context.Context, projectID, id string) error
	DueLogDrains(ctx context.Context, now time.Time, limit int) ([]domain.LogDrain, error)
	LeaseLogDrain(ctx context.Context, lease domain.LogDrainLease) (bool, error)
	AdvanceLogDrain(ctx context.Context, adv domain.LogDrainAdvance) (bool, error)
	FailLogDrain(ctx context.Context, failure domain.LogDrainFailure) (bool, error)
	DeferLogDrain(ctx context.Context, id string, next time.Time) error
}

// ProjectLogReader reads lines in [fromNS, toNS) of one selector, oldest
// first, each cut to logdrain.MaxLineBytes.
type ProjectLogReader interface {
	ReadLines(ctx context.Context, selector string, fromNS, toNS int64, limit int) ([]logdrain.Line, error)
	CountLines(ctx context.Context, selector string, fromNS, toNS int64) (int64, error)
}

type LogDrainDeps struct {
	Store     LogDrainStore
	Vault     vaultclient.VaultClient
	Instances func(projectID string) (*domain.DatabaseInstance, error)
	// Logs is nil when the install has no log store.
	Logs ProjectLogReader
	Now  func() time.Time
}

type LogDrainService struct {
	store     LogDrainStore
	vault     vaultclient.VaultClient
	instances func(projectID string) (*domain.DatabaseInstance, error)
	logs      ProjectLogReader
	now       func() time.Time
	// pullTime is how long one pull keeps reading before it answers.
	pullTime time.Duration
}

func NewLogDrainService(deps LogDrainDeps) *LogDrainService {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &LogDrainService{store: deps.Store, vault: deps.Vault, instances: deps.Instances, logs: deps.Logs, now: now, pullTime: logPullTime}
}

// CreateLogDrainInput is a new drain as an Admin asked for it.
type CreateLogDrainInput struct {
	Name        string
	Kind        string
	DatadogSite string
	URL         string
	HeaderName  string
	Secret      string
}

// UpdateLogDrainInput changes the fields that are set; an empty Secret keeps
// the stored one, and a URL equal to the shown one keeps the stored URL.
type UpdateLogDrainInput struct {
	Name        *string
	DatadogSite *string
	URL         *string
	HeaderName  *string
	Secret      *string
	Enabled     *bool
}

// drainVaultEntry is what the vault holds for a drain: the key or header
// value and, for an HTTPS drain, the full endpoint URL.
type drainVaultEntry struct {
	Secret   string
	Endpoint string
}

func (s *LogDrainService) List(ctx context.Context, projectID string) ([]domain.LogDrain, error) {
	return s.store.ListLogDrains(ctx, projectID)
}

// HasSecret reports whether a key is stored for the drain, never the key.
func (s *LogDrainService) HasSecret(drain *domain.LogDrain) (bool, error) {
	entry, err := s.readEntry(drain)
	return entry.Secret != "", err
}

func (s *LogDrainService) Create(ctx context.Context, projectID, actor string, input CreateLogDrainInput) (*domain.LogDrain, error) {
	if err := s.checkAvailable(projectID); err != nil {
		return nil, err
	}
	drain := &domain.LogDrain{ProjectID: projectID, Name: input.Name, Kind: input.Kind, DatadogSite: input.DatadogSite,
		HeaderName: input.HeaderName, Enabled: true, Status: domain.LogDrainActive, CreatedBy: actor}
	entry := drainVaultEntry{Secret: input.Secret}
	if input.URL != "" {
		if input.Kind != logdrain.KindHTTPS {
			return nil, fmt.Errorf("%w: a Datadog drain takes a site, not a URL", logdrain.ErrInvalidDrain)
		}
		endpoint, err := logdrain.ParseEndpoint(input.URL)
		if err != nil {
			return nil, err
		}
		entry.Endpoint, drain.URL = endpoint, logdrain.DisplayURL(endpoint)
	}
	if err := validateDrain(drain, entry); err != nil {
		return nil, err
	}
	id, err := newLogDrainID()
	if err != nil {
		return nil, err
	}
	now := s.now()
	drain.ID, drain.CursorNS, drain.NextAttemptAt, drain.CreatedAt, drain.UpdatedAt = id, now.UnixNano(), now, now, now
	if err := s.writeEntry(drain, entry); err != nil {
		return nil, err
	}
	if err := s.store.CreateLogDrain(ctx, drain, MaxLogDrainsPerProject); err != nil {
		_ = s.deleteEntry(drain)
		return nil, err
	}
	return drain, nil
}

func (s *LogDrainService) Update(ctx context.Context, projectID, id string, input UpdateLogDrainInput) (*domain.LogDrain, error) {
	current, err := s.store.GetLogDrain(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	stored, err := s.readEntry(current)
	if err != nil {
		return nil, err
	}
	updated, entry, err := applyDrainChange(current, stored, input)
	if err != nil {
		return nil, err
	}
	if entry != stored {
		if err := s.writeEntry(updated, entry); err != nil {
			return nil, err
		}
	}
	updated.UpdatedAt = s.now()
	if err := s.store.UpdateLogDrain(ctx, updated); err != nil {
		if entry != stored {
			// The vault must not send to a destination the drain does not show.
			if restoreErr := s.writeEntry(current, stored); restoreErr != nil {
				log.Printf("log drain %s: restore vault entry: %v", id, restoreErr)
			}
		}
		return nil, err
	}
	if input.Enabled != nil && *input.Enabled != current.Enabled {
		now := s.now()
		// Enabled again, a drain starts from now: no backfill of what it missed.
		if err := s.store.SetLogDrainEnabled(ctx, projectID, id, *input.Enabled, now.UnixNano(), now); err != nil {
			return nil, err
		}
	}
	return s.store.GetLogDrain(ctx, projectID, id)
}

// applyDrainChange is the drain and vault entry an update asks for.
func applyDrainChange(current *domain.LogDrain, stored drainVaultEntry, input UpdateLogDrainInput) (*domain.LogDrain, drainVaultEntry, error) {
	updated, entry := *current, stored
	if input.Name != nil {
		updated.Name = *input.Name
	}
	if input.DatadogSite != nil {
		updated.DatadogSite = *input.DatadogSite
	}
	if input.HeaderName != nil {
		updated.HeaderName = *input.HeaderName
	}
	if input.URL != nil && *input.URL != current.URL {
		if current.Kind != logdrain.KindHTTPS {
			return nil, entry, fmt.Errorf("%w: a Datadog drain takes a site, not a URL", logdrain.ErrInvalidDrain)
		}
		endpoint, err := logdrain.ParseEndpoint(*input.URL)
		if err != nil {
			return nil, entry, err
		}
		entry.Endpoint, updated.URL = endpoint, logdrain.DisplayURL(endpoint)
	}
	newSecret := ""
	if input.Secret != nil {
		newSecret = *input.Secret
	}
	if err := applySecretChange(current, &updated, stored, &entry, newSecret); err != nil {
		return nil, entry, err
	}
	if err := validateDrain(&updated, entry); err != nil {
		return nil, entry, err
	}
	return &updated, entry, nil
}

// applySecretChange sets the entry's secret, refusing to send a stored header
// value to another host or under another header without it entered again:
// the value is write-only, so an Admin who did not enter it must not be able
// to point it at a server of their own.
func applySecretChange(current, updated *domain.LogDrain, stored drainVaultEntry, entry *drainVaultEntry, newSecret string) error {
	if updated.Kind == logdrain.KindHTTPS && updated.HeaderName == "" {
		if newSecret != "" {
			return fmt.Errorf("%w: a header value needs a header name", logdrain.ErrInvalidDrain)
		}
		entry.Secret = ""
		return nil
	}
	if newSecret != "" {
		entry.Secret = newSecret
		return nil
	}
	// Any new endpoint counts, path included: one host can serve many tenants.
	retargeted := stored.Endpoint != entry.Endpoint || current.HeaderName != updated.HeaderName
	if updated.Kind == logdrain.KindHTTPS && entry.Secret != "" && retargeted {
		return fmt.Errorf("%w: enter the header value again to send it to a new endpoint or header", logdrain.ErrInvalidDrain)
	}
	return nil
}

func (s *LogDrainService) Delete(ctx context.Context, projectID, id string) error {
	drain, err := s.store.GetLogDrain(ctx, projectID, id)
	if err != nil {
		return err
	}
	// The key goes first: a drain left without it fails and stops, while a
	// key left without its drain would outlive what the Admin deleted.
	if err := s.deleteEntry(drain); err != nil {
		return err
	}
	return s.store.DeleteLogDrain(ctx, projectID, id)
}

// validateDrain checks a drain's settings with what the vault will hold.
func validateDrain(drain *domain.LogDrain, entry drainVaultEntry) error {
	if err := logdrain.ValidateName(drain.Name); err != nil {
		return err
	}
	if entry.Secret != "" {
		if err := logdrain.ValidateSecret(entry.Secret); err != nil {
			return err
		}
	}
	switch drain.Kind {
	case logdrain.KindDatadog:
		return validateDatadogDrain(drain, entry)
	case logdrain.KindHTTPS:
		return validateHTTPSDrain(drain, entry)
	default:
		return fmt.Errorf("%w: kind must be datadog or https", logdrain.ErrInvalidDrain)
	}
}

func validateDatadogDrain(drain *domain.LogDrain, entry drainVaultEntry) error {
	if _, err := logdrain.DatadogIntakeURL(drain.DatadogSite); err != nil {
		return err
	}
	if entry.Endpoint != "" || drain.HeaderName != "" {
		return fmt.Errorf("%w: a Datadog drain takes a site and an API key only", logdrain.ErrInvalidDrain)
	}
	if entry.Secret == "" {
		return fmt.Errorf("%w: a Datadog drain needs an API key", logdrain.ErrInvalidDrain)
	}
	return nil
}

func validateHTTPSDrain(drain *domain.LogDrain, entry drainVaultEntry) error {
	if entry.Endpoint == "" || drain.DatadogSite != "" {
		return fmt.Errorf("%w: an HTTPS drain needs a URL", logdrain.ErrInvalidDrain)
	}
	if drain.HeaderName == "" {
		if entry.Secret != "" {
			return fmt.Errorf("%w: a header value needs a header name", logdrain.ErrInvalidDrain)
		}
		return nil
	}
	if err := logdrain.ValidateHeaderName(drain.HeaderName); err != nil {
		return err
	}
	if entry.Secret == "" {
		return fmt.Errorf("%w: the header needs a value", logdrain.ErrInvalidDrain)
	}
	return nil
}

func (s *LogDrainService) checkAvailable(projectID string) error {
	inst, err := s.instances(projectID)
	if err != nil {
		return err
	}
	if s.logs == nil || inst.DeploymentMode == domain.ModeDocker || inst.Namespace == "" {
		return ErrLogDrainsUnavailable
	}
	return nil
}

func newLogDrainID() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("log drain id: %w", err)
	}
	return "ld_" + hex.EncodeToString(raw), nil
}

// A drain's vault entry sits beside the project's function secrets, under
// the project prefix that project deletion purges.
func logDrainSecretPath(drain *domain.LogDrain) string {
	return fmt.Sprintf("projects/%s/logdrains/%s", drain.ProjectID, drain.ID)
}

func (s *LogDrainService) writeEntry(drain *domain.LogDrain, entry drainVaultEntry) error {
	if s.vault == nil {
		return errNoVault
	}
	data := map[string]string{}
	if entry.Secret != "" {
		data["secret"] = entry.Secret
	}
	if entry.Endpoint != "" {
		data["url"] = entry.Endpoint
	}
	if len(data) == 0 {
		return s.deleteEntry(drain)
	}
	return s.vault.Put(logDrainSecretPath(drain), data)
}

// readEntry answers an empty entry for a drain with nothing stored.
func (s *LogDrainService) readEntry(drain *domain.LogDrain) (drainVaultEntry, error) {
	if s.vault == nil {
		return drainVaultEntry{}, errNoVault
	}
	data, err := s.vault.Get(logDrainSecretPath(drain))
	if errors.Is(err, vault.ErrNotFound) {
		return drainVaultEntry{}, nil
	}
	if err != nil {
		return drainVaultEntry{}, fmt.Errorf("log drain key: %w", err)
	}
	return drainVaultEntry{Secret: data["secret"], Endpoint: data["url"]}, nil
}

func (s *LogDrainService) deleteEntry(drain *domain.LogDrain) error {
	if s.vault == nil {
		return errNoVault
	}
	if err := s.vault.Delete(logDrainSecretPath(drain)); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return fmt.Errorf("remove log drain key: %w", err)
	}
	return nil
}
