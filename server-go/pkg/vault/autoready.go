package vault

import (
	"fmt"
	"os"
	"strings"
)

// EnsureReady makes a selfhosted vault usable with no operator steps:
//
//   - Fresh vault (not initialized): Init(1,1) — which auto-unseals — and, when
//     the operator is not holding the key themselves (envKey == ""), persist the
//     generated key to keyFilePath (0600) so a restart can auto-unseal.
//   - Restart (initialized but sealed): unseal from envKey (operator-held,
//     e.g. a Cloudflare secret) if set, otherwise from the persisted keyFilePath.
//   - Already unsealed: no-op.
//
// This is the docker/selfhosted equivalent of the k8s bootstrap Job's vault
// init+unseal step. Cloud/k8s deployments keep using that Job and do not call
// this. The persisted key sits next to the bbolt file on the same disk — fine
// for single-tenant selfhosted; operators wanting separation set VAULT_UNSEAL_KEY
// and remove the file.
func EnsureReady(v *Vault, keyFilePath, envKey string) error {
	if !v.Initialized() {
		res, err := v.Init(1, 1)
		if err != nil {
			return fmt.Errorf("vault auto-init: %w", err)
		}
		if envKey == "" {
			if err := os.WriteFile(keyFilePath, []byte(res.Shares[0]), 0o600); err != nil {
				return fmt.Errorf("persist unseal key: %w", err)
			}
		}
		return nil // Init auto-unseals.
	}

	if !v.Sealed() {
		return nil
	}

	key := strings.TrimSpace(envKey)
	if key == "" {
		b, err := os.ReadFile(keyFilePath)
		if err != nil {
			return fmt.Errorf("vault is sealed and no unseal key available (set VAULT_UNSEAL_KEY or restore %s): %w", keyFilePath, err)
		}
		key = strings.TrimSpace(string(b))
	}
	if _, err := v.Unseal(key); err != nil {
		return fmt.Errorf("vault auto-unseal: %w", err)
	}
	return nil
}
