package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultAppTmpPercent is the share of an app's memory limit its scratch /tmp may fill.
const DefaultAppTmpPercent = 25

// envTmpPercent is the variable's whole number, the default when unset, and -1 when it is
// anything else, so Validate can name it. Zero means a hand-built config that never loaded.
func envTmpPercent(key string) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return DefaultAppTmpPercent
	}
	percent, err := strconv.Atoi(raw)
	if err != nil || percent < 1 {
		return -1
	}
	return percent
}

// validateAppTmpPercent refuses a cap that is no cap or no scratch at all; the deploy renderer
// refuses a zero (a config that never loaded) too.
func (c AppConfig) validateAppTmpPercent() error {
	if c.ProvisionerMode == "docker" || c.AppTmpPercent == 0 {
		return nil
	}
	if c.AppTmpPercent < 1 || c.AppTmpPercent > 100 {
		return fmt.Errorf("APP_TMP_PERCENT must be between 1 and 100, got %d", c.AppTmpPercent)
	}
	return nil
}
