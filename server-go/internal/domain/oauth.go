package domain

// OAuthState is what a Studio OAuth sign-in remembers between sending the
// browser to the provider and the provider sending it back.
type OAuthState struct {
	Provider     string
	CodeVerifier string
	// InviteHash is the hash of the org invite the sign-in started from, if any.
	InviteHash string
	// Purpose is empty for a sign-in and "verify" for a GitHub verification (EXC-899).
	Purpose string
	// UserID is the signed-in account a verification started from.
	UserID string
}
