package sites

import (
	"context"
	"fmt"
	"log"
	"sort"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// SecretReader reads the secrets a project keeps (Studio, Edge Functions, Secrets).
type SecretReader interface {
	Secrets(ctx context.Context, projectID string) (map[string]string, error)
}

// ServerSecret is one project secret and whether server-rendered sites see it.
type ServerSecret struct {
	Name          string `json:"name"`
	ServerVisible bool   `json:"serverVisible"`
}

// ServerSecretVisibility lists the project's secrets by name with whether its
// server sites see them; values never leave.
func (s *Service) ServerSecretVisibility(ctx context.Context, projectID string) ([]ServerSecret, error) {
	if err := s.serverSecretsReady(ctx); err != nil {
		return nil, err
	}
	values, err := s.options.Secrets.Secrets(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("read the project's secrets: %w", err)
	}
	marked, err := s.store.ServerVisibleSecrets(ctx, projectID)
	if err != nil {
		return nil, err
	}
	visible := map[string]bool{}
	for _, name := range marked {
		visible[name] = true
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ServerSecret, 0, len(names))
	for _, name := range names {
		out = append(out, ServerSecret{Name: name, ServerVisible: visible[name]})
	}
	return out, nil
}

// SetServerVisibleSecrets makes exactly these secrets visible to the project's
// server sites (ADR 0045 decision 3). Their values are bound into the Worker,
// which runs the repository's code: Studio warns.
func (s *Service) SetServerVisibleSecrets(ctx context.Context, projectID string, names []string, actor string) error {
	if err := s.serverSecretsReady(ctx); err != nil {
		return err
	}
	values, err := s.options.Secrets.Secrets(ctx, projectID)
	if err != nil {
		return fmt.Errorf("read the project's secrets: %w", err)
	}
	for _, name := range names {
		if _, held := values[name]; !held {
			return &InputError{Reason: fmt.Sprintf("the project has no secret named %q", name)}
		}
		if !runtimeVarName.MatchString(name) || runtimeVarReserved(name) {
			return &InputError{Reason: fmt.Sprintf("%s cannot be a server variable: it is not a plain name or the platform owns it", name)}
		}
	}
	return s.store.SetServerVisibleSecrets(ctx, projectID, names, actor)
}

func (s *Service) serverSecretsReady(ctx context.Context) error {
	if !s.Available() {
		return ErrSitesUnavailable
	}
	if err := s.requireKind(ctx, KindServer); err != nil {
		return err
	}
	if s.options.Secrets == nil {
		return &InputError{Reason: "this install keeps no project secrets"}
	}
	return nil
}

// secretBindings are the marked secrets' current values as secret text. Nothing
// is read when none is marked; an unreadable vault fails the publish, since the
// code would otherwise run without secrets it was told it has. A secret marked
// but since deleted is skipped.
func (s *Service) secretBindings(ctx context.Context, projectID string) ([]cfworkers.Binding, error) {
	if s.options.Secrets == nil {
		return nil, nil
	}
	marked, err := s.store.ServerVisibleSecrets(ctx, projectID)
	if err != nil || len(marked) == 0 {
		return nil, err
	}
	values, err := s.options.Secrets.Secrets(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("read the project's secrets: %w", err)
	}
	var bindings []cfworkers.Binding
	for _, name := range marked {
		// Checked again here: a marking can predate a longer reserved list, and a secret must never displace
		// a platform binding or make Cloudflare refuse the whole publish with a name it will not take.
		if !bindableName(name) {
			log.Printf("site project %s: marked secret %s is reserved or invalid, not bound", projectID, name)
			continue
		}
		if value, held := values[name]; held {
			bindings = append(bindings, cfworkers.Binding{Type: cfworkers.BindingSecretText, Name: name, Text: value})
		}
	}
	return bindings, nil
}

// withSecrets adds the secret bindings and drops a plain variable a secret replaces.
func withSecrets(plain, secrets []cfworkers.Binding) []cfworkers.Binding {
	if len(secrets) == 0 {
		return plain
	}
	taken := map[string]bool{}
	for _, secret := range secrets {
		taken[secret.Name] = true
	}
	out := make([]cfworkers.Binding, 0, len(plain)+len(secrets))
	for _, binding := range plain {
		if !taken[binding.Name] {
			out = append(out, binding)
		}
	}
	return append(out, secrets...)
}
