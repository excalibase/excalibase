package handler

import (
	"log"
	"net/http"
)

// resetFailure answers a reset that failed on the platform's side: the cause
// is logged and the user is told to retry or ask for a new link.
func resetFailure(w http.ResponseWriter, step string, err error) {
	log.Printf("ERROR: password reset: %s: %v", step, err)
	httpError(w, "your password was not reset; try again in a moment, or request a new link", http.StatusInternalServerError)
}
