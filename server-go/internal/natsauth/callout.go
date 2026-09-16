package natsauth

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// CalloutSubject is where the NATS server asks whether a connection may
// proceed. Only the delegated auth account may subscribe to it.
const CalloutSubject = "$SYS.REQ.USER.AUTH"

// refusalReason is the single message returned for every refusal. It is
// echoed back to the connecting client, so it must not disclose whether the
// principal exists, and it must never carry request-derived text.
const refusalReason = "not authorized"

// Responder answers the NATS server's auth_callout requests. It is the only
// component that can mint a bus identity: it verifies the presented
// username/password against the credential store and signs a user JWT whose
// subject permissions come from PermissionsFor.
type Responder struct {
	store     CredentialStore
	issuer    nkeys.KeyPair
	account   string
	cdcStream string

	conn *nats.Conn
	sub  *nats.Subscription
}

// NewResponder validates the issuer account seed up front so a
// misconfigured platform fails at boot rather than at the first connection.
func NewResponder(store CredentialStore, issuerSeed, account, cdcStream string) (*Responder, error) {
	if store == nil {
		return nil, fmt.Errorf("nats callout: credential store required")
	}
	if account == "" {
		return nil, fmt.Errorf("nats callout: target account required")
	}
	if !safeToken.MatchString(cdcStream) {
		return nil, fmt.Errorf("nats callout: unsafe CDC stream name")
	}
	seed := strings.TrimSpace(issuerSeed)
	if !strings.HasPrefix(seed, "SA") {
		return nil, fmt.Errorf("nats callout: issuer seed must be an account seed")
	}
	issuer, err := nkeys.FromSeed([]byte(seed))
	if err != nil {
		return nil, fmt.Errorf("nats callout: invalid issuer seed")
	}
	return &Responder{store: store, issuer: issuer, account: account, cdcStream: cdcStream}, nil
}

// Start subscribes to the callout subject on an already-open connection.
// The connection must authenticate into the delegated auth account.
func (r *Responder) Start(conn *nats.Conn) error {
	if conn == nil {
		return fmt.Errorf("nats callout: nil connection")
	}
	sub, err := conn.Subscribe(CalloutSubject, func(msg *nats.Msg) {
		token := r.Handle(context.Background(), msg.Data)
		if token == "" {
			return
		}
		if err := msg.Respond([]byte(token)); err != nil {
			log.Printf("WARN: nats callout respond: %v", err)
		}
	})
	if err != nil {
		return fmt.Errorf("nats callout subscribe: %w", err)
	}
	r.conn, r.sub = conn, sub
	return nil
}

// Handle turns one signed authorization request into a signed authorization
// response. An empty return means the request could not be decoded (not
// server-signed, malformed) and must be dropped without a reply — the
// server then times the connection out, which is the fail-closed outcome.
func (r *Responder) Handle(ctx context.Context, request []byte) string {
	claims, err := jwt.DecodeAuthorizationRequestClaims(string(request))
	if err != nil {
		log.Printf("WARN: nats callout: undecodable authorization request")
		return ""
	}

	principal := claims.ConnectOptions.Username
	perms, permErr := PermissionsFor(principal, r.cdcStream)
	authErr := Authenticate(ctx, r.store, principal, claims.ConnectOptions.Password)
	if permErr != nil || authErr != nil {
		// Deliberately one message for both halves: an attacker must not be
		// able to enumerate principals. The principal is request-derived and
		// is therefore never logged.
		return r.respond(claims, "", refusalReason)
	}

	userJWT, err := r.mintUserJWT(claims.UserNkey, principal, perms)
	if err != nil {
		log.Printf("WARN: nats callout: minting user JWT failed: %v", err)
		return r.respond(claims, "", refusalReason)
	}
	return r.respond(claims, userJWT, "")
}

func (r *Responder) mintUserJWT(userNkey, principal string, perms Permissions) (string, error) {
	user := jwt.NewUserClaims(userNkey)
	user.Name = principal
	// Audience selects the account the connection is placed in; without it
	// the server would fall back to the auth account itself.
	user.Audience = r.account
	// An empty allow-list means "allow everything" to a NATS server, which
	// is the opposite of what the matrix says. Every empty list is turned
	// into an explicit deny-all.
	applyAllowList(&user.Pub, perms.Publish)
	applyAllowList(&user.Sub, perms.Subscribe)
	// Bearer: the client authenticated with a password, not an nkey, so it
	// cannot sign the server nonce.
	user.BearerToken = true
	return user.Encode(r.issuer)
}

// denyAllSubject is the wildcard that shuts a direction off entirely.
const denyAllSubject = ">"

func applyAllowList(perm *jwt.Permission, subjects []string) {
	if len(subjects) == 0 {
		perm.Deny.Add(denyAllSubject)
		return
	}
	perm.Allow.Add(subjects...)
}

func (r *Responder) respond(claims *jwt.AuthorizationRequestClaims, userJWT, refusal string) string {
	response := jwt.NewAuthorizationResponseClaims(claims.UserNkey)
	response.Audience = claims.Server.ID
	response.Jwt = userJWT
	response.Error = refusal

	token, err := response.Encode(r.issuer)
	if err != nil {
		log.Printf("WARN: nats callout: encoding authorization response failed: %v", err)
		return ""
	}
	return token
}

// Close unsubscribes from the callout subject. The connection is owned by
// the caller.
func (r *Responder) Close() {
	if r == nil || r.sub == nil {
		return
	}
	if err := r.sub.Unsubscribe(); err != nil {
		log.Printf("WARN: nats callout unsubscribe: %v", err)
	}
	r.sub = nil
}
