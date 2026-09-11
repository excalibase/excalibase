package edgefn

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// DeriveRuntimeSecret returns a per-project runtime secret derived from the
// master secret and the project id via HMAC-SHA256. Each project's Deno pod is
// injected with its own derived value (never the master), and the internal
// runtime routes validate the presented token against the derivation for the
// project named in the request — so a leaked project secret authenticates only
// that project and reveals neither the master nor any sibling's secret
// (SEC-C5). An empty master yields an empty string so callers keep their
// fail-closed "no secret configured" behaviour.
func DeriveRuntimeSecret(master, projectID string) string {
	if master == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(master))
	mac.Write([]byte(projectID))
	return hex.EncodeToString(mac.Sum(nil))
}
