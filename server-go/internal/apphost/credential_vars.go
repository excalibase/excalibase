package apphost

import "regexp"

// credentialName matches variable names that hold a credential. A plain value
// is visible to every Developer on the project and stored in the app's
// definition, so these are always secrets (EXC-761).
var credentialName = regexp.MustCompile(`(?i)(KEY|TOKEN|SECRET|PASSWORD|PASSWD|CREDENTIAL)`)

// LooksLikeCredential reports whether a variable name holds a credential.
func LooksLikeCredential(name string) bool { return credentialName.MatchString(name) }

// SealCredentialLiterals turns every plain variable whose name looks like a
// credential into a secret variable, and returns the values to store at each
// secret's reference. The values leave the app's definition; the caller
// stores them before it saves the app. Nothing is refused: an app that holds
// such a variable already keeps saving, with the value kept.
func (a *App) SealCredentialLiterals() map[SecretRef]string {
	var sealed map[SecretRef]string
	for _, variable := range a.Env {
		if variable.Kind != KindLiteral || variable.Value == nil || !LooksLikeCredential(variable.Name) {
			continue
		}
		value := *variable.Value
		ref := a.SetSecretVar(variable.Name)
		if sealed == nil {
			sealed = map[SecretRef]string{}
		}
		sealed[ref] = value
	}
	return sealed
}
