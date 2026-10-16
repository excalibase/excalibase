package cfworkers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

// Routing values Cloudflare's static-assets configuration accepts.
const (
	HTMLAutoTrailingSlash = "auto-trailing-slash"
	NotFoundSinglePage    = "single-page-application"
	NotFound404Page       = "404-page"
)

// compatibilityDate pins the runtime behaviour every site Worker is published with.
const compatibilityDate = "2025-09-01"

// AssetConfig is the routing baked into one published deploy.
type AssetConfig struct {
	HTMLHandling     string `json:"html_handling"`
	NotFoundHandling string `json:"not_found_handling"`
}

// scriptMetadata publishes an assets-only Worker: no module, Cloudflare serves the files itself.
type scriptMetadata struct {
	CompatibilityDate string       `json:"compatibility_date"`
	Assets            scriptAssets `json:"assets"`
}

type scriptAssets struct {
	JWT    string      `json:"jwt"`
	Config AssetConfig `json:"config"`
}

// PutScript publishes script with the assets the completion token stands for.
func (c *Client) PutScript(ctx context.Context, script, completionJWT string, config AssetConfig) error {
	target, err := c.scriptPath(script)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(scriptMetadata{
		CompatibilityDate: compatibilityDate,
		Assets:            scriptAssets{JWT: completionJWT, Config: config},
	})
	if err != nil {
		return fmt.Errorf("encode script metadata: %w", err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if err := writeFormPart(form, "metadata", "", "application/json", metadata); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return fmt.Errorf("close script form: %w", err)
	}
	_, _, err = c.call(ctx, http.MethodPut, target, c.token, form.FormDataContentType(), &body)
	return err
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

func writeFormPart(form *multipart.Writer, name, filename, contentType string, content []byte) error {
	header := textproto.MIMEHeader{}
	disposition := fmt.Sprintf(`form-data; name=%q`, name)
	if filename != "" {
		disposition += fmt.Sprintf(`; filename=%q`, filename)
	}
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", contentType)
	part, err := form.CreatePart(header)
	if err != nil {
		return fmt.Errorf("start %s part: %w", name, err)
	}
	if _, err := part.Write(content); err != nil {
		return fmt.Errorf("write %s part: %w", name, err)
	}
	return nil
}
