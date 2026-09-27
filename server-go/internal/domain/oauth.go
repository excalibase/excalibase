package domain

// OAuthState is what a Studio OAuth sign-in remembers between sending the
// browser to the provider and the provider sending it back.
type OAuthState struct {
	Provider     string
	CodeVerifier string
	// InviteHash is the hash of the org invite the sign-in started from, if any.
	InviteHash string
}
