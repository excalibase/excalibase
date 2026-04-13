package vaultclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// VaultClient defines the interface for vault operations.
// Both *vault.Vault (in-process) and *HTTPClient (remote) satisfy this.
type VaultClient interface {
	Get(path string) (map[string]string, error)
	Put(path string, data map[string]string) error
	Delete(path string) error
	List(prefix string) ([]string, error)
	Sealed() bool
	GetPublicKey() (string, error)
}

// HTTPClient talks to the vault service over HTTP.
type HTTPClient struct {
	baseURL    string
	pat        string
	httpClient *http.Client
}

func NewHTTPClient(baseURL, pat string) *HTTPClient {
	return &HTTPClient{
		baseURL: baseURL,
		pat:     pat,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *HTTPClient) Get(path string) (map[string]string, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/secrets/"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("secret not found: %s", path)
	}
	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("vault is sealed")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault returned %d", resp.StatusCode)
	}

	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return data, nil
}

func (c *HTTPClient) Put(path string, data map[string]string) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal data: %w", err)
	}

	req, err := http.NewRequest("PUT", c.baseURL+"/secrets/"+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vault request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("vault is sealed")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault returned %d", resp.StatusCode)
	}
	return nil
}

func (c *HTTPClient) Delete(path string) error {
	req, err := http.NewRequest("DELETE", c.baseURL+"/secrets/"+path, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vault request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return fmt.Errorf("vault is sealed")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault returned %d", resp.StatusCode)
	}
	return nil
}

func (c *HTTPClient) List(prefix string) ([]string, error) {
	url := c.baseURL + "/secrets-list"
	if prefix != "" {
		url += "?prefix=" + prefix
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vault request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return nil, fmt.Errorf("vault is sealed")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vault returned %d", resp.StatusCode)
	}

	var result struct {
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result.Paths, nil
}

func (c *HTTPClient) Sealed() bool {
	req, err := http.NewRequest("GET", c.baseURL+"/status", nil)
	if err != nil {
		return true
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true
	}
	defer resp.Body.Close()

	var status struct {
		Sealed bool `json:"sealed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return true
	}
	return status.Sealed
}

func (c *HTTPClient) GetPublicKey() (string, error) {
	req, err := http.NewRequest("GET", c.baseURL+"/pki/public-key", nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("vault request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		return "", fmt.Errorf("vault is sealed")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vault returned %d", resp.StatusCode)
	}

	var data map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	return data["key"], nil
}

func (c *HTTPClient) setAuth(req *http.Request) {
	if c.pat != "" {
		req.Header.Set("Authorization", "Bearer "+c.pat)
	}
}
