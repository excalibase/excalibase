package natsauth

import (
	"testing"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
)

const testDispatchPrincipal = PrincipalGraphQL

// TestRefuseDropsAnUndecodableRequest: a request that is not server-signed
// cannot be answered at all — there is no server to address the response to
// — so it must be dropped, never guessed at.
func TestRefuseDropsAnUndecodableRequest(t *testing.T) {
	responder, _ := newTestResponder(t, testDispatchPrincipal, testPassword)

	// No panic, no reply: the message carries no reply subject either, so a
	// responder that tried to answer would fail loudly here.
	responder.refuse(&nats.Msg{Subject: CalloutSubject, Data: []byte("not-a-jwt")})
}

// TestRefuseAnswersADecodableRequestWithADenial pins the fail-closed shape
// of backpressure: a request the pool could not serve is answered with a
// denial that carries no user JWT, never left to expire.
func TestRefuseAnswersADecodableRequestWithADenial(t *testing.T) {
	responder, _ := newTestResponder(t, testDispatchPrincipal, testPassword)
	request, _ := signedRequest(t, testDispatchPrincipal, testPassword)

	denial := responder.respond(decodedRequest(t, request), "", refusalReason)
	claims, err := jwt.DecodeAuthorizationResponseClaims(denial)
	if err != nil {
		t.Fatalf("DecodeAuthorizationResponseClaims: %v", err)
	}
	if claims.Error != refusalReason {
		t.Errorf("refusal reason = %q, want %q", claims.Error, refusalReason)
	}
	if claims.Jwt != "" {
		t.Error("a refusal carried a user JWT")
	}

	// The same request through refuse: it decodes, is answered, and the
	// missing reply subject is the only thing that fails.
	responder.refuse(&nats.Msg{Subject: CalloutSubject, Data: []byte(request)})
}

// decodedRequest is the server-signed request as the responder sees it.
func decodedRequest(t *testing.T, request string) *jwt.AuthorizationRequestClaims {
	t.Helper()
	claims, err := jwt.DecodeAuthorizationRequestClaims(request)
	if err != nil {
		t.Fatalf("DecodeAuthorizationRequestClaims: %v", err)
	}
	return claims
}

// TestReplyIgnoresAnEmptyTokenAndSurvivesAFailedRespond: an undecodable
// request yields no token, and a request whose connection has gone away
// cannot be answered. Neither may take the responder down.
func TestReplyIgnoresAnEmptyTokenAndSurvivesAFailedRespond(t *testing.T) {
	responder, _ := newTestResponder(t, testDispatchPrincipal, testPassword)

	responder.reply(&nats.Msg{Subject: CalloutSubject}, "")
	// No reply subject and no connection: Respond fails, and the responder
	// logs it instead of propagating a panic into the worker pool.
	responder.reply(&nats.Msg{Subject: CalloutSubject}, "a-token")
}

// TestInFlightIsZeroWithoutARunningPool keeps the health signal honest
// before Start and after a nil responder, which is what a shutdown path and
// a health endpoint both call.
func TestInFlightIsZeroWithoutARunningPool(t *testing.T) {
	var absent *Responder
	if absent.InFlight() != 0 {
		t.Error("a nil responder reports work in flight")
	}

	responder, _ := newTestResponder(t, testDispatchPrincipal, testPassword)
	if got := responder.InFlight(); got != 0 {
		t.Errorf("InFlight() = %d before Start, want 0", got)
	}
}

// TestCloseIsSafeWithoutStartAndAfterTheConnectionDied covers shutdown
// ordering: the owner closes the connection first, so unsubscribing then
// fails — and Close must still drain and stay callable.
func TestCloseIsSafeWithoutStartAndAfterTheConnectionDied(t *testing.T) {
	var absent *Responder
	absent.Close()

	responder, _ := newTestResponder(t, testDispatchPrincipal, testPassword)
	responder.Close()

	// A subscription whose connection is gone: Unsubscribe returns an error
	// that Close logs rather than propagates.
	responder.sub = &nats.Subscription{Subject: CalloutSubject}
	responder.Close()
	if responder.sub != nil {
		t.Error("Close left the subscription in place")
	}
	// Idempotent: a second Close after a drain must not block or panic.
	responder.Close()
}
