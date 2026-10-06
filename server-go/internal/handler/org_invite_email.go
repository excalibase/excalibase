package handler

// isEmailAddress reports whether an invite names an address a sign-up would
// accept. It is the registration rule itself, so an invite is never sent to
// an address that could not register to claim it.
func isEmailAddress(address string) bool {
	return isValidEmail(address)
}
