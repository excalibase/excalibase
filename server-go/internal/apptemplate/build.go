package apptemplate

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ErrSecretGeneration: the platform could not draw a value; a fault of ours, not of the template.
var ErrSecretGeneration = errors.New("could not generate a secret")

// secretAlphabet keeps a generated value safe inside a URL, a shell word and a config file.
var secretAlphabet = charRange('A', 'Z') + charRange('a', 'z') + charRange('0', '9')

func charRange(first, last byte) string {
	out := make([]byte, 0, last-first+1)
	for c := first; c <= last; c++ {
		out = append(out, c)
	}
	return string(out)
}

// GenerateSecret draws n characters from crypto/rand.
func GenerateSecret(n int) (string, error) {
	if n < MinSecretLength || n > MaxSecretLength {
		return "", fmt.Errorf("a secret is %d to %d characters", MinSecretLength, MaxSecretLength)
	}
	limit := big.NewInt(int64(len(secretAlphabet)))
	var b strings.Builder
	b.Grow(n)
	for range n {
		index, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("draw a secret: %w", err)
		}
		b.WriteByte(secretAlphabet[index.Int64()])
	}
	return b.String(), nil
}

// BuildInput is what a deploy knows about the project.
type BuildInput struct {
	ProjectID string
	Tier      domain.TierType
	// DatabaseName is the project's database, or "" when it has none that serves.
	DatabaseName string
	NewID        func() string
	Secret       func(n int) (string, error)
}

// SecretValues maps a vault path to the value stored there. It never prints its values.
type SecretValues map[string]string

func (SecretValues) String() string   { return "[secret values]" }
func (SecretValues) GoString() string { return "[secret values]" }

// Built is what a deploy creates: the app records, whose secret variables
// point at their own vault entries, and the values to store in those entries.
type Built struct {
	Apps    []*apphost.App
	Secrets SecretValues
}

type resolved struct {
	value  string
	secret bool
	ref    *apphost.ReferenceTarget
}

// Build resolves the template for one project. Generated values go only to
// Built.Secrets; no error names one.
func (t *Template) Build(in BuildInput) (*Built, error) {
	if in.NewID == nil || in.Secret == nil {
		return nil, errors.New("a build needs an id source and a secret source")
	}
	specs := make(map[string]*AppSpec, len(t.Apps))
	values := make(map[string]map[string]resolved, len(t.Apps))
	built := &Built{Secrets: SecretValues{}}
	for i := range t.Apps {
		spec := &t.Apps[i]
		specs[spec.Name] = spec
		built.Apps = append(built.Apps, spec.record(in.NewID(), in.ProjectID, in.Tier))
		values[spec.Name] = map[string]resolved{}
	}
	// Plain values first, so a variable read from another app is already known.
	for _, pass := range []bool{false, true} {
		for _, spec := range t.Apps {
			for _, v := range spec.Env {
				parts, err := parseValue(v.Value)
				if err != nil {
					return nil, invalid("app %q, %s: %v", spec.Name, v.Name, err)
				}
				if readsAppVariable(parts) != pass {
					continue
				}
				value, err := resolve(parts, in, specs, values)
				if err != nil {
					return nil, fmt.Errorf("app %q, %s: %w", spec.Name, v.Name, err)
				}
				values[spec.Name][v.Name] = value
			}
		}
	}
	for i, spec := range t.Apps {
		app := built.Apps[i]
		for _, v := range spec.Env {
			app.Env = append(app.Env, envVar(app, v.Name, values[spec.Name][v.Name], built.Secrets))
		}
		if err := app.Validate(); err != nil {
			return nil, fmt.Errorf("app %q: %w", spec.Name, err)
		}
	}
	return built, nil
}

func readsAppVariable(parts []part) bool {
	for _, p := range parts {
		if p.expr != nil && p.expr.kind == exprAppEnv {
			return true
		}
	}
	return false
}

func resolve(parts []part, in BuildInput, specs map[string]*AppSpec, values map[string]map[string]resolved) (resolved, error) {
	if len(parts) == 1 && parts[0].expr != nil && parts[0].expr.kind == exprDatabase {
		if in.DatabaseName == "" {
			return resolved{}, errors.New("the template needs the project's database, and the project has none that is ready")
		}
		return resolved{ref: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: in.DatabaseName, Variable: parts[0].expr.variable,
		}}, nil
	}
	var out resolved
	var b strings.Builder
	for _, p := range parts {
		if p.expr == nil {
			b.WriteString(p.literal)
			continue
		}
		switch p.expr.kind {
		case exprSecret:
			value, err := in.Secret(p.expr.length)
			if err != nil {
				return resolved{}, ErrSecretGeneration
			}
			b.WriteString(value)
			out.secret = true
		case exprAppHost:
			b.WriteString(p.expr.app)
		case exprAppPort:
			b.WriteString(strconv.Itoa(specs[p.expr.app].port()))
		case exprAppEnv:
			read, ok := values[p.expr.app][p.expr.variable]
			if !ok || read.ref != nil {
				return resolved{}, fmt.Errorf("apps.%s.env.%s has no plain value", p.expr.app, p.expr.variable)
			}
			b.WriteString(read.value)
			out.secret = out.secret || read.secret
		default:
			return resolved{}, errors.New("a database reference must be the whole value")
		}
	}
	out.value = b.String()
	return out, nil
}

// envVar: a value holding anything generated is a secret in the app's own vault entry.
func envVar(app *apphost.App, name string, value resolved, secrets SecretValues) apphost.EnvVar {
	switch {
	case value.ref != nil:
		return apphost.EnvVar{Name: name, Kind: apphost.KindReference, Reference: value.ref}
	case value.secret:
		ref := apphost.AppSecretRef(app.ProjectID, app.ID, name)
		secrets[ref.Path] = value.value
		return apphost.EnvVar{Name: name, Kind: apphost.KindSecret, Secret: &ref}
	default:
		literal := value.value
		return apphost.EnvVar{Name: name, Kind: apphost.KindLiteral, Value: &literal}
	}
}
