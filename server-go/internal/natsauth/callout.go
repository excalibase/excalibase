package natsauth

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// CalloutSubject is where the NATS server asks whether a connection may
// proceed. Only the delegated auth account may subscribe to it.
const CalloutSubject = "$SYS.REQ.USER.AUTH"

// ServerAuthTimeout is the `authorization { timeout }` the NATS server must
// be configured with for this responder. The server's 2s default is not
// enough headroom for a bcrypt verification on a CPU-limited pod, and it
// fails a connection with an authorization violation when the callout is
// late. The value is mirrored in the chart's server config; see
// docs/nats-auth-callout.md.
const ServerAuthTimeout = 5 * time.Second

// refusalReason is the single message returned for every refusal. It is
// echoed back to the connecting client, so it must not disclose whether the
// principal exists, and it must never carry request-derived text.
const refusalReason = "not authorized"

// Verifier decides whether a principal's presented password is acceptable.
// It is a seam: production uses the credential store, tests substitute a
// verifier with controlled latency so concurrency can be asserted without
// depending on how busy the machine happens to be.
type Verifier interface {
	Verify(ctx context.Context, principal, password string) error
}

// storeVerifier is the production verifier: a bcrypt comparison against the
// hash held in the credential store.
type storeVerifier struct{ store CredentialStore }

func (s storeVerifier) Verify(ctx context.Context, principal, password string) error {
	return Authenticate(ctx, s.store, principal, password)
}

// ResponderOption tunes a Responder at construction.
type ResponderOption func(*Responder)

// WithVerifier replaces the credential check.
func WithVerifier(verifier Verifier) ResponderOption {
	return func(r *Responder) {
		if verifier != nil {
			r.verifier = verifier
		}
	}
}

// WithWorkers caps how many credential verifications run at once.
func WithWorkers(workers int) ResponderOption {
	return func(r *Responder) {
		if workers > 0 {
			r.workers = workers
		}
	}
}

// WithQueueCapacity caps how many requests may be admitted (running or
// waiting for a worker) before further requests are refused.
func WithQueueCapacity(capacity int) ResponderOption {
	return func(r *Responder) {
		if capacity > 0 {
			r.queueCapacity = capacity
		}
	}
}

// Responder answers the NATS server's auth_callout requests. It is the only
// component that can mint a bus identity: it verifies the presented
// username/password against the credential store and signs a user JWT whose
// subject permissions come from PermissionsFor.
type Responder struct {
	verifier  Verifier
	issuer    nkeys.KeyPair
	account   string
	cdcStream string

	workers       int
	queueCapacity int

	conn *nats.Conn
	sub  *nats.Subscription
	pool *dispatcher
}

// InFlight reports how many verifications are running right now. It is the
// signal a shutdown test and an operator dashboard both need.
func (r *Responder) InFlight() int64 {
	if r == nil || r.pool == nil {
		return 0
	}
	return r.pool.inFlightCount()
}

// NewResponder validates the issuer account seed up front so a
// misconfigured platform fails at boot rather than at the first connection.
func NewResponder(store CredentialStore, issuerSeed, account, cdcStream string, opts ...ResponderOption) (*Responder, error) {
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
	responder := &Responder{
		verifier:  storeVerifier{store: store},
		issuer:    issuer,
		account:   account,
		cdcStream: cdcStream,
		workers:   defaultWorkers(),
	}
	for _, opt := range opts {
		opt(responder)
	}
	if responder.queueCapacity == 0 {
		responder.queueCapacity = responder.workers * queueDepthPerWorker
	}
	responder.pool = newDispatcher(responder.workers, responder.queueCapacity, admissionWaitBudget)
	return responder, nil
}

// admissionWaitBudget is how long a request may wait for a free worker
// before it is refused. It leaves the connecting client a margin inside
// ServerAuthTimeout to receive the refusal, so a saturated responder denies
// promptly instead of letting the server time the connection out.
const admissionWaitBudget = ServerAuthTimeout * 3 / 4

// Start subscribes to the callout subject on an already-open connection.
// The connection must authenticate into the delegated auth account.
func (r *Responder) Start(conn *nats.Conn) error {
	if conn == nil {
		return fmt.Errorf("nats callout: nil connection")
	}
	sub, err := conn.Subscribe(CalloutSubject, r.dispatch)
	if err != nil {
		return fmt.Errorf("nats callout subscribe: %w", err)
	}
	// Subscribe only buffers the SUB frame. Until the server has registered it,
	// a connecting client's authorization request reaches no one and the server
	// times it out as an authorization violation, so Start must not report
	// readiness before the subscription is live.
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("nats callout subscribe flush: %w", err)
	}
	r.conn, r.sub = conn, sub
	return nil
}

// dispatch hands one callout request to the verification pool. The NATS
// client delivers async messages on a single goroutine per subscription, so
// verifying inline would serialise every connection behind one bcrypt: that
// is what made a reconnect storm overrun the server's authorization timeout.
func (r *Responder) dispatch(msg *nats.Msg) {
	r.pool.run(
		func() { r.serve(msg) },
		func() { r.refuse(msg) },
	)
}

// serve answers one request. A panic anywhere in the verification path is
// contained here and turned into a refusal: one malformed request must not
// take the responder's subscription goroutine — and with it every other
// connection — down with it.
func (r *Responder) serve(msg *nats.Msg) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("ERROR: nats callout: authorization request panicked: %v", recovered)
			r.refuse(msg)
		}
	}()
	// Bound the credential lookup: a worker held by a stalled database query
	// is a worker the next connection cannot have, and the answer would be
	// useless by the time it arrived anyway.
	ctx, cancel := context.WithTimeout(context.Background(), ServerAuthTimeout)
	defer cancel()
	r.reply(msg, r.Handle(ctx, msg.Data))
}

// refuse denies a request the responder could not serve — a saturated pool,
// a panic — explicitly, so the client learns immediately instead of waiting
// out the server's authorization timeout. Failing closed is the point: the
// alternative of admitting an unverified connection is never taken.
func (r *Responder) refuse(msg *nats.Msg) {
	claims, err := jwt.DecodeAuthorizationRequestClaims(string(msg.Data))
	if err != nil {
		log.Printf("WARN: nats callout: undecodable authorization request")
		return
	}
	r.reply(msg, r.respond(claims, "", refusalReason))
}

func (r *Responder) reply(msg *nats.Msg, token string) {
	if token == "" {
		return
	}
	if err := msg.Respond([]byte(token)); err != nil {
		log.Printf("WARN: nats callout respond: %v", err)
	}
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
	authErr := r.verifier.Verify(ctx, principal, claims.ConnectOptions.Password)
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

// Close unsubscribes from the callout subject and drains the verifications
// already in flight, so a shutting-down responder still answers the
// connections it accepted. The connection is owned by the caller.
func (r *Responder) Close() {
	if r == nil {
		return
	}
	if r.sub != nil {
		if err := r.sub.Unsubscribe(); err != nil {
			log.Printf("WARN: nats callout unsubscribe: %v", err)
		}
		r.sub = nil
	}
	if r.pool != nil {
		r.pool.close()
	}
}
