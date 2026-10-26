package main

import (
	"github.com/excalibase/provisioning-poc/internal/handler"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// The relay finds project names and the sign-in mail budget by asserting the
// platform store; a renamed method would leave every sign-in mail refused.
var _ handler.SignInMailSource = (*pgstore.Store)(nil)
