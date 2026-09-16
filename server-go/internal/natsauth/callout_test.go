package natsauth

import (
	"context"
	"slices"
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

const (
	testAccount   = "APP"
	testPassword  = "correct-horse"
	testServerKey = "NATS-TEST-SERVER"
)

// newTestResponder builds a responder whose store holds one credential.
func newTestResponder(t *testing.T, principal, password string) (*Responder, nkeys.KeyPair) {
	t.Helper()
	accountKP, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	seed, err := accountKP.Seed()
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	responder, err := NewResponder(newStoreWith(t, principal, password), string(seed), testAccount, testStream)
	if err != nil {
		t.Fatalf("NewResponder: %v", err)
	}
	return responder, accountKP
}

// signedRequest produces the auth request JWT a NATS server would send.
func signedRequest(t *testing.T, username, password string) (string, string) {
	t.Helper()
	serverKP, err := nkeys.CreateServer()
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	userKP, err := nkeys.CreateUser()
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	userPub, _ := userKP.PublicKey()

	claims := jwt.NewAuthorizationRequestClaims(userPub)
	claims.UserNkey = userPub
	claims.Server.ID = testServerKey
	claims.ConnectOptions.Username = username
	claims.ConnectOptions.Password = password

	token, err := claims.Encode(serverKP)
	if err != nil {
		t.Fatalf("Encode request: %v", err)
	}
	return token, userPub
}

// grantedUser runs a request through the responder and returns the issued
// user claims. It fails the test if the connection was refused.
func grantedUser(t *testing.T, responder *Responder, username, password string) *jwt.UserClaims {
	t.Helper()
	request, userPub := signedRequest(t, username, password)
	response := decodeResponse(t, responder, request)
	if response.Error != "" {
		t.Fatalf("connection refused: %s", response.Error)
	}
	user, err := jwt.DecodeUserClaims(response.Jwt)
	if err != nil {
		t.Fatalf("DecodeUserClaims: %v", err)
	}
	if user.Subject != userPub {
		t.Errorf("user JWT subject = %q, want the client nkey %q", user.Subject, userPub)
	}
	if user.Audience != testAccount {
		t.Errorf("user JWT audience = %q, want %q", user.Audience, testAccount)
	}
	return user
}

func decodeResponse(t *testing.T, responder *Responder, request string) *jwt.AuthorizationResponse {
	t.Helper()
	token := responder.Handle(context.Background(), []byte(request))
	claims, err := jwt.DecodeAuthorizationResponseClaims(token)
	if err != nil {
		t.Fatalf("DecodeAuthorizationResponseClaims: %v", err)
	}
	if claims.Audience != testServerKey {
		t.Errorf("response audience = %q, want the requesting server id", claims.Audience)
	}
	return &claims.AuthorizationResponse
}

func TestHandleIssuesScopedTenantWatcherJWT(t *testing.T) {
	principal := TenantWatcherPrincipal("proj-a")
	responder, _ := newTestResponder(t, principal, testPassword)

	user := grantedUser(t, responder, principal, testPassword)

	if !slices.Contains(user.Pub.Allow, "cdc.proj-a.>") {
		t.Errorf("publish allow = %v, missing own project prefix", user.Pub.Allow)
	}
	if slices.Contains(user.Pub.Allow, "cdc.>") {
		t.Errorf("publish allow = %v, must not include the CDC wildcard", user.Pub.Allow)
	}
	for _, subject := range user.Sub.Allow {
		if subject == "cdc.>" || subject == "cdc.proj-a.>" || subject == "policies.>" {
			t.Errorf("tenant watcher granted subscribe on %q", subject)
		}
	}
}

func TestHandleIssuesReadScopeForGraphQL(t *testing.T) {
	responder, _ := newTestResponder(t, PrincipalGraphQL, testPassword)

	user := grantedUser(t, responder, PrincipalGraphQL, testPassword)

	if !slices.Contains(user.Sub.Allow, "cdc.>") {
		t.Errorf("subscribe allow = %v, missing cdc.>", user.Sub.Allow)
	}
	if !slices.Contains(user.Sub.Allow, "policies.>") {
		t.Errorf("subscribe allow = %v, missing policies.>", user.Sub.Allow)
	}
	if slices.Contains(user.Pub.Allow, "cdc.>") {
		t.Errorf("graphql granted publish on cdc.>: %v", user.Pub.Allow)
	}
}

// An empty allow-list is "allow everything" to a NATS server, so a
// principal that publishes nothing must carry an explicit deny instead.
func TestHandleDeniesEmptyDirectionsExplicitly(t *testing.T) {
	responder, _ := newTestResponder(t, PrincipalPgDog, testPassword)

	user := grantedUser(t, responder, PrincipalPgDog, testPassword)

	if len(user.Pub.Allow) != 0 {
		t.Fatalf("pgdog publish allow = %v, want empty", user.Pub.Allow)
	}
	if !slices.Contains(user.Pub.Deny, ">") {
		t.Errorf("pgdog publish deny = %v, want a deny-all wildcard", user.Pub.Deny)
	}
}

func TestHandleRefusesBadCredentials(t *testing.T) {
	principal := TenantWatcherPrincipal("proj-a")
	responder, _ := newTestResponder(t, principal, testPassword)

	cases := []struct {
		name     string
		username string
		password string
	}{
		{"wrong password", principal, "guess"},
		{"anonymous", "", ""},
		{"no password", principal, ""},
		{"unknown principal", "svc-attacker", testPassword},
		{"other project", TenantWatcherPrincipal("proj-b"), testPassword},
		{"wildcard project", tenantWatcherPrefix + ">", testPassword},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request, _ := signedRequest(t, tc.username, tc.password)
			response := decodeResponse(t, responder, request)
			if response.Error == "" {
				t.Errorf("connection accepted for %s, want refusal", tc.name)
			}
			if response.Jwt != "" {
				t.Errorf("refused connection still carried a user JWT")
			}
		})
	}
}

func TestHandleRefusesUnsignedOrGarbageRequests(t *testing.T) {
	responder, _ := newTestResponder(t, PrincipalGraphQL, testPassword)

	for _, payload := range []string{"", "not-a-jwt", "eyJhbGciOiJub25lIn0.e30."} {
		token := responder.Handle(context.Background(), []byte(payload))
		if token != "" {
			// A reply is only possible when the request decoded; anything
			// else must be dropped rather than answered.
			claims, err := jwt.DecodeAuthorizationResponseClaims(token)
			if err == nil && claims.Error == "" {
				t.Errorf("garbage request %q was granted a JWT", payload)
			}
		}
	}
}

func TestNewResponderRejectsBadIssuerSeed(t *testing.T) {
	store := newStoreWith(t, PrincipalGraphQL, testPassword)
	userKP, _ := nkeys.CreateUser()
	userSeed, _ := userKP.Seed()

	cases := []struct {
		name string
		seed string
	}{
		{"empty", ""},
		{"garbage", "not-a-seed"},
		{"user seed not account seed", string(userSeed)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewResponder(store, tc.seed, testAccount, testStream); err == nil {
				t.Error("NewResponder accepted an invalid issuer seed")
			}
		})
	}
}
