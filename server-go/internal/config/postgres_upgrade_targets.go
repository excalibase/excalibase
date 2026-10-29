package config

import "strconv"

// MinPostgresUpgradeTarget is the lowest major an upgrade may move to:
// CloudNativePG refuses lower targets (collation data it does not carry), and
// a single host offers the same set so both deployments behave alike.
const MinPostgresUpgradeTarget = 16

// PostgresUpgradeTargets are the published catalogue majors newer than
// current and at least MinPostgresUpgradeTarget, in catalogue order (EXC-668). A major the catalogue does not list
// has none: nothing is known about what its data could move to.
func PostgresUpgradeTargets(current string) []string {
	targets := []string{}
	from, ok := catalogueMajorNumber(current)
	if !ok {
		return targets
	}
	for _, entry := range postgresCatalog.Majors {
		to, err := strconv.Atoi(entry.Major)
		if err == nil && to > from && to >= MinPostgresUpgradeTarget && entry.Image != "" {
			targets = append(targets, entry.Major)
		}
	}
	return targets
}

// IsPostgresUpgradeTarget reports whether target is offered from current.
func IsPostgresUpgradeTarget(current, target string) bool {
	for _, candidate := range PostgresUpgradeTargets(current) {
		if candidate == target {
			return true
		}
	}
	return false
}

func catalogueMajorNumber(major string) (int, bool) {
	entry, ok := LookupPostgresMajor(major)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(entry.Major)
	return n, err == nil
}
