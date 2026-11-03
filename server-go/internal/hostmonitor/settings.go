package hostmonitor

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

// ErrInvalidSettings is an alert rule the platform refuses.
var ErrInvalidSettings = errors.New("invalid alert settings")

// ErrVaultSealed refuses a webhook change while the vault cannot store it.
var ErrVaultSealed = errors.New("the vault is sealed")

const (
	maxRecipients = 10
	maxWindow     = 60
)

// SettingsStore keeps the one row of rules.
type SettingsStore interface {
	GetMonitoringAlertSettings(ctx context.Context) (domain.MonitoringAlertSettings, bool, error)
	SaveMonitoringAlertSettings(ctx context.Context, settings domain.MonitoringAlertSettings) error
}

// SettingsView is the rules as an admin reads them; the webhook by its host only.
type SettingsView struct {
	domain.MonitoringAlertSettings
	WebhookURL *string `json:"webhookUrl"`
	WebhookSet bool    `json:"webhookSet"`
}

// SettingsUpdate replaces every rule. WebhookURL nil keeps the webhook, "" removes it.
type SettingsUpdate struct {
	Enabled            bool     `json:"enabled"`
	CPUPercent         *float64 `json:"cpuPercent"`
	MemoryPercent      *float64 `json:"memoryPercent"`
	DiskPercent        *float64 `json:"diskPercent"`
	TemperatureCelsius *float64 `json:"temperatureCelsius"`
	Load5              *float64 `json:"load5"`
	NetworkMbps        *float64 `json:"networkMbps"`
	WindowMinutes      int      `json:"windowMinutes"`
	DownMinutes        int      `json:"downMinutes"`
	Emails             []string `json:"emails"`
	WebhookURL         *string  `json:"webhookUrl"`
}

// Settings reads and changes the host's alert rules.
type Settings struct {
	store    SettingsStore
	vault    Vault
	notifier *Notifier
}

func NewSettings(store SettingsStore, vault Vault) *Settings {
	return &Settings{store: store, vault: vault}
}

// WithNotifier lets Test send through the alert channels.
func (s *Settings) WithNotifier(notifier *Notifier) *Settings {
	s.notifier = notifier
	return s
}

// Test sends a test notice to the saved channels.
func (s *Settings) Test(ctx context.Context) ([]ChannelResult, error) {
	current, err := s.Current(ctx)
	if err != nil || s.notifier == nil {
		return nil, err
	}
	return s.notifier.Test(ctx, current), nil
}

// Current is the saved rules, or the defaults before the first save.
func (s *Settings) Current(ctx context.Context) (domain.MonitoringAlertSettings, error) {
	saved, found, err := s.store.GetMonitoringAlertSettings(ctx)
	if err != nil {
		return saved, err
	}
	if !found {
		return domain.DefaultMonitoringAlertSettings(), nil
	}
	return saved, nil
}

func (s *Settings) Get(ctx context.Context) (SettingsView, error) {
	current, err := s.Current(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	return viewOf(current), nil
}

func viewOf(settings domain.MonitoringAlertSettings) SettingsView {
	view := SettingsView{MonitoringAlertSettings: settings, WebhookSet: settings.WebhookDisplay != ""}
	if view.WebhookSet {
		display := settings.WebhookDisplay
		view.WebhookURL = &display
	}
	if view.Emails == nil {
		view.Emails = []string{}
	}
	return view
}

func (s *Settings) Update(ctx context.Context, update SettingsUpdate) (SettingsView, error) {
	next, err := validate(update)
	if err != nil {
		return SettingsView{}, err
	}
	current, err := s.Current(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	next.WebhookDisplay = current.WebhookDisplay
	if update.WebhookURL != nil {
		if next.WebhookDisplay, err = s.changeWebhook(strings.TrimSpace(*update.WebhookURL)); err != nil {
			return SettingsView{}, err
		}
	}
	if err := s.store.SaveMonitoringAlertSettings(ctx, next); err != nil {
		return SettingsView{}, err
	}
	return viewOf(next), nil
}

// changeWebhook stores or removes the URL and answers what the row shows of it.
func (s *Settings) changeWebhook(raw string) (string, error) {
	endpoint := ""
	if raw != "" {
		parsed, err := logdrain.ParseEndpoint(raw)
		if err != nil {
			return "", fmt.Errorf("%w: the webhook must be a public https:// URL on port 443 without credentials", ErrInvalidSettings)
		}
		endpoint = parsed
	}
	if s.vault == nil || s.vault.Sealed() {
		return "", ErrVaultSealed
	}
	if endpoint == "" {
		if err := s.vault.Delete(WebhookVaultPath); err != nil {
			return "", fmt.Errorf("remove the webhook: %w", err)
		}
		return "", nil
	}
	if err := s.vault.Put(WebhookVaultPath, map[string]string{"url": endpoint}); err != nil {
		return "", fmt.Errorf("store the webhook: %w", err)
	}
	return logdrain.DisplayURL(endpoint), nil
}

type bound struct {
	name   string
	value  *float64
	upTo   float64
	suffix string
}

func validate(update SettingsUpdate) (domain.MonitoringAlertSettings, error) {
	for _, rule := range []bound{
		{"cpuPercent", update.CPUPercent, 100, "%"}, {"memoryPercent", update.MemoryPercent, 100, "%"},
		{"diskPercent", update.DiskPercent, 100, "%"}, {"temperatureCelsius", update.TemperatureCelsius, 150, " °C"},
		{"load5", update.Load5, 10000, ""}, {"networkMbps", update.NetworkMbps, 1000000, " Mbit/s"},
	} {
		if rule.value != nil && (*rule.value <= 0 || *rule.value > rule.upTo) {
			return domain.MonitoringAlertSettings{}, fmt.Errorf("%w: %s must be above 0 and at most %g%s, or empty to turn it off", ErrInvalidSettings, rule.name, rule.upTo, rule.suffix)
		}
	}
	if update.WindowMinutes < 1 || update.WindowMinutes > maxWindow {
		return domain.MonitoringAlertSettings{}, fmt.Errorf("%w: windowMinutes must be 1 to %d", ErrInvalidSettings, maxWindow)
	}
	if update.DownMinutes < 0 || update.DownMinutes > maxWindow {
		return domain.MonitoringAlertSettings{}, fmt.Errorf("%w: downMinutes must be 0 to %d", ErrInvalidSettings, maxWindow)
	}
	emails, err := recipients(update.Emails)
	if err != nil {
		return domain.MonitoringAlertSettings{}, err
	}
	return domain.MonitoringAlertSettings{
		Enabled: update.Enabled, CPUPercent: update.CPUPercent, MemoryPercent: update.MemoryPercent, DiskPercent: update.DiskPercent,
		TemperatureCelsius: update.TemperatureCelsius, Load5: update.Load5, NetworkMbps: update.NetworkMbps,
		WindowMinutes: update.WindowMinutes, DownMinutes: update.DownMinutes, Emails: emails,
	}, nil
}

// recipients are bare addresses, each once, at most maxRecipients.
func recipients(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parsed, err := mail.ParseAddress(entry)
		if err != nil || parsed.Address != entry || len(entry) > 254 {
			return nil, fmt.Errorf("%w: %q is not an email address", ErrInvalidSettings, entry)
		}
		if key := strings.ToLower(entry); !seen[key] {
			seen[key] = true
			out = append(out, entry)
		}
	}
	if len(out) > maxRecipients {
		return nil, fmt.Errorf("%w: at most %d email recipients", ErrInvalidSettings, maxRecipients)
	}
	return out, nil
}
