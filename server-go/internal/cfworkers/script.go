package cfworkers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"regexp"
	"strings"
)

// Routing values Cloudflare's static-assets configuration accepts.
const (
	HTMLAutoTrailingSlash = "auto-trailing-slash"
	NotFoundSinglePage    = "single-page-application"
	NotFound404Page       = "404-page"
	// NotFoundNone hands every path no asset matches to the Worker's code, as a server site needs.
	NotFoundNone = "none"
)

// compatibilityDate pins the runtime behaviour every static site Worker is published with.
const compatibilityDate = "2025-09-01"

// Module part types Cloudflare's script upload accepts.
const (
	ModuleJavaScript = "application/javascript+module"
	ModuleWasm       = "application/wasm"
	ModuleText       = "text/plain"
	ModuleData       = "application/octet-stream"
	ModuleSourceMap  = "application/source-map"
)

// Binding types a site Worker may hold; ASSETS is added by PutScript itself.
const (
	BindingPlainText  = "plain_text"
	BindingSecretText = "secret_text"
	bindingAssets     = "assets"
	assetsBindingName = "ASSETS"
)

var moduleTypes = map[string]bool{ModuleJavaScript: true, ModuleWasm: true, ModuleText: true, ModuleData: true, ModuleSourceMap: true}

// moduleNamePattern keeps a module name inside the form header and the script's own tree.
var moduleNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/@+-]{0,255}$`)

var bindingNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// AssetConfig is the routing baked into one published deploy.
type AssetConfig struct {
	HTMLHandling     string `json:"html_handling"`
	NotFoundHandling string `json:"not_found_handling"`
}

// Module is one code file of a server Worker; Open is called once, while the request streams.
type Module struct {
	Name        string
	ContentType string
	Open        func() (io.ReadCloser, error)
}

// Binding is one variable of the Worker's env. Text is a secret for secret_text and is never formatted.
type Binding struct {
	Type string
	Name string
	Text string
}

func (b Binding) String() string   { return fmt.Sprintf("%s binding %s", b.Type, b.Name) }
func (b Binding) GoString() string { return b.String() }

// ScriptLimits are the per-script CPU and subrequest caps, a guard behind the dispatcher's per-call ones.
type ScriptLimits struct {
	CPUMillis   int `json:"cpu_ms,omitempty"`
	Subrequests int `json:"subrequests,omitempty"`
}

// ScriptVersion is one publish of a site Worker. With no MainModule it is
// assets-only (a static site); otherwise it runs the modules, with the ASSETS
// binding and the bindings named here only.
type ScriptVersion struct {
	AssetsJWT          string
	Assets             AssetConfig
	MainModule         string
	Modules            []Module
	CompatibilityDate  string
	CompatibilityFlags []string
	Bindings           []Binding
	Limits             *ScriptLimits
	Tags               []string
}

// scriptMetadata never carries keep_bindings: each publish states every binding it has.
type scriptMetadata struct {
	MainModule         string          `json:"main_module,omitempty"`
	CompatibilityDate  string          `json:"compatibility_date"`
	CompatibilityFlags []string        `json:"compatibility_flags,omitempty"`
	Bindings           []bindingRecord `json:"bindings,omitempty"`
	Assets             scriptAssets    `json:"assets"`
	Limits             *ScriptLimits   `json:"limits,omitempty"`
	Tags               []string        `json:"tags,omitempty"`
}

type bindingRecord struct {
	Type string `json:"type"`
	Name string `json:"name"`
	Text string `json:"text,omitempty"`
}

type scriptAssets struct {
	JWT    string      `json:"jwt"`
	Config AssetConfig `json:"config"`
}

// PutScript publishes script as version, with the assets its completion token stands for.
func (c *Client) PutScript(ctx context.Context, script string, version ScriptVersion) error {
	target, err := c.scriptPath(script)
	if err != nil {
		return err
	}
	metadata, err := buildMetadata(version)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode script metadata: %w", err)
	}
	body, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() { writer.CloseWithError(writeScriptParts(form, encoded, version.Modules)) }()
	_, _, err = c.call(ctx, http.MethodPut, target, c.token, form.FormDataContentType(), body)
	// The writer may still be blocked on a pipe nobody reads after a failed call.
	_ = body.CloseWithError(io.ErrClosedPipe)
	return err
}

func buildMetadata(version ScriptVersion) (scriptMetadata, error) {
	assets := scriptAssets{JWT: version.AssetsJWT, Config: version.Assets}
	if version.MainModule == "" {
		if len(version.Modules) > 0 || len(version.Bindings) > 0 || len(version.CompatibilityFlags) > 0 || version.Limits != nil {
			return scriptMetadata{}, errors.New("an assets-only worker carries no modules, bindings, flags or limits")
		}
		return scriptMetadata{CompatibilityDate: compatibilityDate, Assets: assets}, nil
	}
	if err := checkModules(version.MainModule, version.Modules); err != nil {
		return scriptMetadata{}, err
	}
	if version.CompatibilityDate == "" {
		return scriptMetadata{}, errors.New("a worker with modules needs a compatibility date")
	}
	bindings, err := bindingRecords(version.Bindings)
	if err != nil {
		return scriptMetadata{}, err
	}
	return scriptMetadata{
		MainModule: version.MainModule, CompatibilityDate: version.CompatibilityDate,
		CompatibilityFlags: version.CompatibilityFlags, Bindings: bindings, Assets: assets,
		Limits: version.Limits, Tags: version.Tags,
	}, nil
}

// ValidModuleName is whether name may be sent as a module: it stays inside the form header and the script's own tree.
func ValidModuleName(name string) bool {
	return moduleNamePattern.MatchString(name) && name != "metadata" && !strings.Contains(name, "..") && !strings.Contains(name, "//")
}

func checkModules(main string, modules []Module) error {
	seen := map[string]bool{}
	mainFound := false
	for _, mod := range modules {
		if !ValidModuleName(mod.Name) {
			return fmt.Errorf("invalid module name %q", mod.Name)
		}
		if seen[mod.Name] {
			return fmt.Errorf("module %s is listed twice", mod.Name)
		}
		seen[mod.Name] = true
		if !moduleTypes[mod.ContentType] {
			return fmt.Errorf("module %s has a type Cloudflare does not run", mod.Name)
		}
		if mod.Name == main {
			mainFound = mod.ContentType == ModuleJavaScript
		}
	}
	if !mainFound {
		return fmt.Errorf("the main module %q is not among the JavaScript modules", main)
	}
	return nil
}

// bindingRecords puts ASSETS first, then the caller's text bindings, refusing anything else.
func bindingRecords(bindings []Binding) ([]bindingRecord, error) {
	records := []bindingRecord{{Type: bindingAssets, Name: assetsBindingName}}
	seen := map[string]bool{assetsBindingName: true}
	for _, binding := range bindings {
		if binding.Type != BindingPlainText && binding.Type != BindingSecretText {
			return nil, fmt.Errorf("a site worker may not hold a %s binding", binding.Type)
		}
		if !bindingNamePattern.MatchString(binding.Name) || seen[binding.Name] {
			return nil, fmt.Errorf("binding name %q is invalid or taken", binding.Name)
		}
		seen[binding.Name] = true
		records = append(records, bindingRecord(binding))
	}
	return records, nil
}

func writeScriptParts(form *multipart.Writer, metadata []byte, modules []Module) error {
	if err := writeFormPart(form, "metadata", "", "application/json", metadata); err != nil {
		return err
	}
	for _, mod := range modules {
		if err := writeModulePart(form, mod); err != nil {
			return err
		}
	}
	if err := form.Close(); err != nil {
		return fmt.Errorf("close script form: %w", err)
	}
	return nil
}

func writeModulePart(form *multipart.Writer, mod Module) error {
	part, err := form.CreatePart(partHeader(mod.Name, mod.Name, mod.ContentType))
	if err != nil {
		return fmt.Errorf("start module %s: %w", mod.Name, err)
	}
	content, err := mod.Open()
	if err != nil {
		return fmt.Errorf("open module %s: %w", mod.Name, err)
	}
	defer content.Close()
	if _, err := io.Copy(part, content); err != nil {
		return fmt.Errorf("write module %s: %w", mod.Name, err)
	}
	return nil
}

// DeleteScript removes script; one that is already gone counts as deleted.
func (c *Client) DeleteScript(ctx context.Context, script string) error {
	target, err := c.scriptPath(script)
	if err != nil {
		return err
	}
	_, _, err = c.call(ctx, http.MethodDelete, target+"?force=true", c.token, "", nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func partHeader(name, filename, contentType string) textproto.MIMEHeader {
	header := textproto.MIMEHeader{}
	disposition := fmt.Sprintf(`form-data; name=%q`, name)
	if filename != "" {
		disposition += fmt.Sprintf(`; filename=%q`, filename)
	}
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", contentType)
	return header
}

func writeFormPart(form *multipart.Writer, name, filename, contentType string, content []byte) error {
	part, err := form.CreatePart(partHeader(name, filename, contentType))
	if err != nil {
		return fmt.Errorf("start %s part: %w", name, err)
	}
	if _, err := part.Write(content); err != nil {
		return fmt.Errorf("write %s part: %w", name, err)
	}
	return nil
}
