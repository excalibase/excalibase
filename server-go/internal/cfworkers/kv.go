package cfworkers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// hostnameKey is the only key shape written: a lowercase hostname, so nothing reaches the path but labels and dots.
var hostnameKey = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

func (c *Client) kvPath(key string) (string, error) {
	if !hostnameKey.MatchString(key) {
		return "", fmt.Errorf("invalid hostname key %q", key)
	}
	return c.accountPath("storage", "kv", "namespaces", c.kv, "values", key), nil
}

// PutKV writes value under key in the hosts namespace.
func (c *Client) PutKV(ctx context.Context, key, value string) error {
	target, err := c.kvPath(key)
	if err != nil {
		return err
	}
	_, _, err = c.call(ctx, http.MethodPut, target, c.token, "text/plain", strings.NewReader(value))
	return err
}

// DeleteKV removes key; a key already gone counts as deleted.
func (c *Client) DeleteKV(ctx context.Context, key string) error {
	target, err := c.kvPath(key)
	if err != nil {
		return err
	}
	_, _, err = c.call(ctx, http.MethodDelete, target, c.token, "", nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}
