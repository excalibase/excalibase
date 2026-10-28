package hostmonitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type settingsTable struct {
	saved *domain.MonitoringAlertSettings
	err   error
}

func (s *settingsTable) GetMonitoringAlertSettings(context.Context) (domain.MonitoringAlertSettings, bool, error) {
	if s.err != nil {
		return domain.MonitoringAlertSettings{}, false, s.err
	}
	if s.saved == nil {
		return domain.MonitoringAlertSettings{}, false, nil
	}
	return *s.saved, true, nil
}

func (s *settingsTable) SaveMonitoringAlertSettings(_ context.Context, settings domain.MonitoringAlertSettings) error {
	if s.err != nil {
		return s.err
	}
	s.saved = &settings
	return nil
}

func validUpdate() SettingsUpdate {
	return SettingsUpdate{Enabled: true, CPUPercent: f64(80), MemoryPercent: f64(85), DiskPercent: f64(90), TemperatureCelsius: f64(80),
		WindowMinutes: 5, DownMinutes: 2, Emails: []string{"ops@example.com"}}
}

func TestSettingsAreTheDefaultsUntilSaved(t *testing.T) {
	settings := NewSettings(&settingsTable{}, &memoryVault{})
	view, err := settings.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !view.Enabled || *view.CPUPercent != 90 || view.WindowMinutes != 5 || view.WebhookSet || view.WebhookURL != nil || view.Emails == nil {
		t.Errorf("defaults = %+v", view)
	}
}

func TestSaveKeepsTheWebhookInTheVaultAndShowsOnlyItsHost(t *testing.T) {
	table, vault := &settingsTable{}, &memoryVault{}
	settings := NewSettings(table, vault)
	update := validUpdate()
	hook := "https://hooks.slack.com/services/T000/B000/secret-token"
	update.WebhookURL = &hook
	view, err := settings.Update(context.Background(), update)
	if err != nil {
		t.Fatal(err)
	}
	if !view.WebhookSet || view.WebhookURL == nil || *view.WebhookURL != "https://hooks.slack.com/..." || strings.Contains(table.saved.WebhookDisplay, "secret") {
		t.Errorf("view = %+v, saved %+v", view, table.saved)
	}
	if vault.entries[WebhookVaultPath]["url"] != hook {
		t.Errorf("vault = %v", vault.entries)
	}
	update.WebhookURL = nil
	update.CPUPercent = nil
	view, err = settings.Update(context.Background(), update)
	if err != nil || !view.WebhookSet || view.CPUPercent != nil {
		t.Errorf("an update without the webhook keeps it; a nil threshold is off: %+v %v", view, err)
	}
	empty := ""
	update.WebhookURL = &empty
	view, err = settings.Update(context.Background(), update)
	if err != nil || view.WebhookSet || view.WebhookURL != nil {
		t.Errorf("an empty webhook clears it: %+v %v", view, err)
	}
	if _, stored := vault.entries[WebhookVaultPath]; stored {
		t.Error("the cleared webhook left the vault")
	}
}

func TestUpdateRefusesWhatIsNotARule(t *testing.T) {
	settings := NewSettings(&settingsTable{}, &memoryVault{})
	refused := map[string]func(*SettingsUpdate){
		"cpu over 100":     func(u *SettingsUpdate) { u.CPUPercent = f64(101) },
		"memory zero":      func(u *SettingsUpdate) { u.MemoryPercent = f64(0) },
		"disk negative":    func(u *SettingsUpdate) { u.DiskPercent = f64(-5) },
		"temperature 200":  func(u *SettingsUpdate) { u.TemperatureCelsius = f64(200) },
		"load zero":        func(u *SettingsUpdate) { u.Load5 = f64(0) },
		"network negative": func(u *SettingsUpdate) { u.NetworkMbps = f64(-1) },
		"window zero":      func(u *SettingsUpdate) { u.WindowMinutes = 0 },
		"window two hours": func(u *SettingsUpdate) { u.WindowMinutes = 120 },
		"down negative":    func(u *SettingsUpdate) { u.DownMinutes = -1 },
		"bad email":        func(u *SettingsUpdate) { u.Emails = []string{"not an address"} },
		"named email":      func(u *SettingsUpdate) { u.Emails = []string{"Ops <ops@example.com>"} },
		"eleven emails": func(u *SettingsUpdate) {
			u.Emails = nil
			for i := range 11 {
				u.Emails = append(u.Emails, fmt.Sprintf("ops%d@example.com", i))
			}
		},
		"http webhook":        func(u *SettingsUpdate) { hook := "http://hooks.example.com/x"; u.WebhookURL = &hook },
		"internal webhook":    func(u *SettingsUpdate) { hook := "https://10.0.0.5/hook"; u.WebhookURL = &hook },
		"credentials webhook": func(u *SettingsUpdate) { hook := "https://user:pass@hooks.example.com/x"; u.WebhookURL = &hook },
	}
	for name, mutate := range refused {
		update := validUpdate()
		mutate(&update)
		if _, err := settings.Update(context.Background(), update); !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("%s: err = %v, want ErrInvalidSettings", name, err)
		}
	}
}

func TestASealedVaultRefusesAWebhookChange(t *testing.T) {
	settings := NewSettings(&settingsTable{}, &memoryVault{sealed: true})
	update := validUpdate()
	hook := "https://hooks.example.com/x"
	update.WebhookURL = &hook
	if _, err := settings.Update(context.Background(), update); !errors.Is(err, ErrVaultSealed) {
		t.Errorf("err = %v, want ErrVaultSealed", err)
	}
	update.WebhookURL = nil
	if _, err := settings.Update(context.Background(), update); err != nil {
		t.Errorf("rules without a webhook change save while sealed: %v", err)
	}
}

func TestEmailsAreTrimmedAndDeduplicated(t *testing.T) {
	table := &settingsTable{}
	settings := NewSettings(table, &memoryVault{})
	update := validUpdate()
	update.Emails = []string{" ops@example.com ", "OPS@example.com", "", "dev@example.com"}
	if _, err := settings.Update(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if strings.Join(table.saved.Emails, ",") != "ops@example.com,dev@example.com" {
		t.Errorf("emails = %v", table.saved.Emails)
	}
}

func TestAStoreFailureIsReturned(t *testing.T) {
	settings := NewSettings(&settingsTable{err: errors.New("db down")}, &memoryVault{})
	if _, err := settings.Get(context.Background()); err == nil {
		t.Error("a failed read is an error")
	}
	if _, err := settings.Update(context.Background(), validUpdate()); err == nil {
		t.Error("a failed save is an error")
	}
}

func TestTestSendsThroughTheSavedChannels(t *testing.T) {
	mail := &sentMail{}
	table := &settingsTable{saved: &domain.MonitoringAlertSettings{Emails: []string{"ops@example.com"}, WindowMinutes: 5}}
	settings := NewSettings(table, &memoryVault{}).WithNotifier(NewNotifier(NotifierDeps{Mail: mail}))
	results, err := settings.Test(context.Background())
	if err != nil || len(results) != 1 || !results[0].OK || len(mail.messages) != 1 || mail.messages[0].Subject != "[Excalibase] Test notice" {
		t.Errorf("test = %+v %v, mail %+v", results, err, mail.messages)
	}
	if results, err := NewSettings(table, nil).Test(context.Background()); err != nil || results != nil {
		t.Errorf("without a notifier nothing is sent: %v %v", results, err)
	}
}
