package apphost

import "fmt"

// AppSecretValueKey is the field each app secret entry keeps its value under.
const AppSecretValueKey = "value"

// AppSecretRef is where the platform keeps the value of one of an app's secret
// variables: a vault entry of its own under the app's prefix, so writing one
// never has to read and rewrite the others. It is generated here, never taken
// from a caller.
func AppSecretRef(projectID, appID, name string) SecretRef {
	return SecretRef{Path: AppSecretPrefix(projectID, appID) + "env/" + name, Key: AppSecretValueKey}
}

// AppSecretPrefix holds every secret value one app owns.
func AppSecretPrefix(projectID, appID string) string {
	return "projects/" + projectID + "/apps/" + appID + "/"
}

// ValidateEnvName checks one variable name.
func ValidateEnvName(name string) error {
	if len(name) > MaxEnvNameLength || !validEnvName.MatchString(name) {
		return fmt.Errorf("invalid environment variable name: %q", name)
	}
	return nil
}

// SetSecretVar points the named variable at the app's own vault entry,
// replacing whatever it carried, or appends it when the app has none by
// that name. The env slice is rebuilt so a caller's copy is never mutated.
func (a *App) SetSecretVar(name string) SecretRef {
	ref := AppSecretRef(a.ProjectID, a.ID, name)
	secret := EnvVar{Name: name, Kind: KindSecret, Secret: &ref}
	env := make([]EnvVar, 0, len(a.Env)+1)
	replaced := false
	for _, v := range a.Env {
		if v.Name == name {
			env = append(env, secret)
			replaced = true
			continue
		}
		env = append(env, v)
	}
	if !replaced {
		env = append(env, secret)
	}
	a.Env = env
	return ref
}
