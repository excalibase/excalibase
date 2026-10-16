package cfworkers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

// ManifestEntry is one file of a deploy as Cloudflare's manifest names it.
type ManifestEntry struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// UploadSession is what Cloudflare still needs: Buckets are the hashes to
// upload, one request per bucket. With no buckets JWT is already the
// completion token.
type UploadSession struct {
	JWT     string
	Buckets [][]string
}

// Asset is one file body to upload; Open is called once, while the request streams.
type Asset struct {
	Hash        string
	ContentType string
	Open        func() (io.ReadCloser, error)
}

// StartUploadSession hands Cloudflare the manifest of a deploy of script.
func (c *Client) StartUploadSession(ctx context.Context, script string, manifest map[string]ManifestEntry) (UploadSession, error) {
	target, err := c.scriptPath(script, "assets-upload-session")
	if err != nil {
		return UploadSession{}, err
	}
	payload, err := json.Marshal(map[string]any{"manifest": manifest})
	if err != nil {
		return UploadSession{}, fmt.Errorf("encode manifest: %w", err)
	}
	_, result, err := c.call(ctx, http.MethodPost, target, c.token, "application/json", bytes.NewReader(payload))
	if err != nil {
		return UploadSession{}, err
	}
	var session struct {
		JWT     string     `json:"jwt"`
		Buckets [][]string `json:"buckets"`
	}
	if err := json.Unmarshal(result, &session); err != nil {
		return UploadSession{}, fmt.Errorf("decode upload session: %w", err)
	}
	if session.JWT == "" {
		return UploadSession{}, fmt.Errorf("cloudflare opened an upload session without a token")
	}
	return UploadSession{JWT: session.JWT, Buckets: session.Buckets}, nil
}

// UploadBucket sends one bucket's files, authorised by the session's upload
// token. It answers the completion token once Cloudflare holds every file
// (201), and "" for an earlier bucket.
func (c *Client) UploadBucket(ctx context.Context, uploadJWT string, assets []Asset) (string, error) {
	body, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() { writer.CloseWithError(writeAssetParts(form, assets)) }()
	target := c.accountPath("workers", "assets", "upload") + "?base64=true"
	status, result, err := c.call(ctx, http.MethodPost, target, uploadJWT, form.FormDataContentType(), body)
	// The writer may still be blocked on a pipe nobody reads after a failed call.
	_ = body.CloseWithError(io.ErrClosedPipe)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", nil
	}
	var completion struct {
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal(result, &completion); err != nil {
		return "", fmt.Errorf("decode upload answer: %w", err)
	}
	if completion.JWT == "" {
		return "", fmt.Errorf("cloudflare finished the upload without a completion token")
	}
	return completion.JWT, nil
}

func writeAssetParts(form *multipart.Writer, assets []Asset) error {
	for _, asset := range assets {
		if err := writeAssetPart(form, asset); err != nil {
			return err
		}
	}
	return form.Close()
}

func writeAssetPart(form *multipart.Writer, asset Asset) error {
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, asset.Hash, asset.Hash))
	header.Set("Content-Type", asset.ContentType)
	part, err := form.CreatePart(header)
	if err != nil {
		return fmt.Errorf("start asset part: %w", err)
	}
	content, err := asset.Open()
	if err != nil {
		return fmt.Errorf("open asset %s: %w", asset.Hash, err)
	}
	defer content.Close()
	encoder := base64.NewEncoder(base64.StdEncoding, part)
	if _, err := io.Copy(encoder, content); err != nil {
		return fmt.Errorf("encode asset %s: %w", asset.Hash, err)
	}
	return encoder.Close()
}
