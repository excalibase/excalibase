package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxProjectNameLength bounds a project's display name, in characters.
const MaxProjectNameLength = 100

// NormalizeProjectName trims the name and reports whether it is 1 to
// MaxProjectNameLength characters long.
func NormalizeProjectName(name string) (string, bool) {
	trimmed := strings.TrimSpace(name)
	length := utf8.RuneCountInString(trimmed)
	if length == 0 || length > MaxProjectNameLength {
		return "", false
	}
	return trimmed, true
}

// masterUsernamePattern is a plain lowercase Postgres identifier. The name is
// written into pg_hba lines, so nothing that could add a token there passes.
var masterUsernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// reservedRoleNames are pg_hba keywords and roles Postgres or CNPG own.
var reservedRoleNames = map[string]bool{
	"postgres": true, "all": true, "replication": true, "sameuser": true,
	"samerole": true, "streaming_replica": true, "cnpg_pooler_pgbouncer": true,
	"public": true, "none": true,
}

// ErrMasterUsername refuses a database owner name the platform will not use.
var ErrMasterUsername = errors.New("masterUsername must be 1-63 characters of lowercase letters, digits and underscore, not starting with a digit, and not a reserved role name")

// ValidateMasterUsername refuses an owner role name that is not a plain
// identifier or that names a reserved role.
func ValidateMasterUsername(name string) error {
	if !masterUsernamePattern.MatchString(name) || reservedRoleNames[name] || strings.HasPrefix(name, "pg_") {
		return ErrMasterUsername
	}
	return nil
}

// Recovery targets are written into postgresql.auto.conf inside quotes, so
// each is held to the shape Postgres itself accepts for it.
var (
	recoveryXIDPattern  = regexp.MustCompile(`^[0-9]+$`)
	recoveryLSNPattern  = regexp.MustCompile(`^[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}$`)
	recoveryNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,63}$`)
)

func validateRecoveryTarget(r RestoreRequest) error {
	switch {
	case r.TargetXID != "" && !recoveryXIDPattern.MatchString(r.TargetXID):
		return fmt.Errorf("restore request: targetXid must be a transaction id (digits only)")
	case r.TargetLSN != "" && !recoveryLSNPattern.MatchString(r.TargetLSN):
		return fmt.Errorf("restore request: targetLsn must look like 0/16B3748")
	case r.TargetName != "" && !recoveryNamePattern.MatchString(r.TargetName):
		return fmt.Errorf("restore request: targetName must be 1-63 letters, digits, '_', '.' or '-'")
	}
	return nil
}
