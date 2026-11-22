package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
)

const (
	// confirmTTL is how long a confirmation token stays good.
	confirmTTL = 5 * time.Minute
	// maxPendingConfirmations bounds the store; expired tokens are given up first.
	maxPendingConfirmations = 10000
	// maxPendingPerUser keeps one account from using up the store for everyone.
	maxPendingPerUser = 30
	confirmPrefix     = "mcpc_"
	confirmBytes      = 32
)

// confirmTarget is exactly what a token destroys: the project and the one
// object in it. A token never reaches another project's object of the same id.
type confirmTarget struct {
	project string
	id      string
}

// confirmation is what one issued token is good for.
type confirmation struct {
	user    string
	session string
	tool    string
	target  confirmTarget
	expires time.Time
}

// confirmations holds the tokens of the destructive tools. They live in this
// process, like the probe limits: the control plane runs one replica, and a
// restart only asks the caller to start again.
type confirmations struct {
	mu      sync.Mutex
	now     func() time.Time
	pending map[[sha256.Size]byte]confirmation
}

func newConfirmations() *confirmations {
	return &confirmations{now: time.Now, pending: map[[sha256.Size]byte]confirmation{}}
}

func digest(token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(token))
}

// issue mints a token for this caller, tool and target. Only the token's hash
// is kept.
func (s *confirmations) issue(caller Caller, tool string, target confirmTarget) (string, time.Time, error) {
	raw := make([]byte, confirmBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("make a confirmation token: %w", err)
	}
	token := confirmPrefix + base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	held := s.dropExpired(now, caller.User.ID)
	if held >= maxPendingPerUser {
		return "", time.Time{}, errors.New("you have too many confirmations waiting: use one or wait a few minutes")
	}
	if len(s.pending) >= maxPendingConfirmations {
		return "", time.Time{}, errors.New("too many confirmations are waiting: try again in a few minutes")
	}
	expires := now.Add(confirmTTL)
	s.pending[digest(token)] = confirmation{
		user: caller.User.ID, session: caller.Token.TokenHash, tool: tool, target: target, expires: expires,
	}
	return token, expires, nil
}

// dropExpired gives up every expired token and says how many live ones the user holds.
func (s *confirmations) dropExpired(now time.Time, user string) int {
	held := 0
	for key, token := range s.pending {
		switch {
		case !now.Before(token.expires):
			delete(s.pending, key)
		case token.user == user:
			held++
		}
	}
	return held
}

// redeem spends a token. A token that is found is spent whatever follows, so a
// wrong guess at its target does not leave it usable; only a different caller
// leaves it alone, so a stranger cannot spend someone else's.
func (s *confirmations) redeem(caller Caller, token, tool string, target confirmTarget) error {
	if !strings.HasPrefix(token, confirmPrefix) {
		return errConfirmationUnknown
	}
	key := digest(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.pending[key]
	if !ok || held.user != caller.User.ID || held.session != caller.Token.TokenHash {
		return errConfirmationUnknown
	}
	delete(s.pending, key)
	if !s.now().Before(held.expires) {
		return errConfirmationExpired
	}
	if held.tool != tool || held.target != target {
		return errConfirmationElsewhere
	}
	return nil
}

var (
	errConfirmationUnknown = errors.New("confirmation_token is not valid: it was already used, was issued to another connection, or was never issued. " +
		"Call again without it to get a new one")
	errConfirmationExpired   = errors.New("confirmation_token has expired. Call again without it to get a new one")
	errConfirmationElsewhere = errors.New("confirmation_token was issued for a different tool or target, and is now spent. " +
		"Call again without it to get one for this target")
)

// destruction is one irreversible action a tool is about to take.
type destruction struct {
	tool   string
	target confirmTarget
	token  string
	// describe says what would be destroyed; it is read only while the guard is on.
	describe func(ctx context.Context) (string, error)
}

// confirmArg is the argument every guarded tool takes.
type confirmArg struct {
	ConfirmationToken string `json:"confirmation_token,omitempty" jsonschema:"leave out on the first call, which deletes nothing and returns what would be destroyed plus a token; pass that token, with the same other arguments, to go ahead. Good for five minutes, once, for this exact target"`
}

// guard decides whether a destructive tool may act now. It returns the answer
// to hand back instead (nil when the action may go ahead), or an error when
// the token is not good. With the guard off, the action goes ahead.
func (c *call) guard(ctx context.Context, d destruction) (map[string]any, error) {
	if !features.Enabled(ctx, c.settings.Features, features.MCPConfirm) {
		return nil, nil
	}
	description, err := d.describe(ctx)
	if err != nil {
		return nil, err
	}
	if d.token != "" {
		if err := c.confirms.redeem(c.caller, d.token, d.tool, d.target); err != nil {
			return nil, err
		}
		c.stage = stageConfirmed
		return nil, nil
	}
	token, _, err := c.confirms.issue(c.caller, d.tool, d.target)
	if err != nil {
		return nil, err
	}
	c.stage = stageRequested
	return map[string]any{
		"confirmationRequired": true,
		"nothingChanged":       true,
		"wouldDelete":          description,
		"confirmationToken":    token,
		"expiresInSeconds":     int(confirmTTL.Seconds()),
		"next": "Nothing was deleted. Tell the user what would be deleted and ask whether to go ahead. Only if they say yes, call " + d.tool +
			" again with the same arguments and confirmation_token set to this token. It works once, for this target only. " +
			"The call that deletes is still checked against the caller's project role (the one this tool's description names), so it is refused if that role may not delete this.",
	}, nil
}

// What a call did about confirmation, for the audit entry.
const (
	stageRequested = "requested"
	stageConfirmed = "confirmed"
)

func orNone(text string) string {
	if text == "" {
		return "(none)"
	}
	return text
}

// confirmNote ends the description of every tool that asks twice.
const confirmNote = " Irreversible, so it asks twice: the first call deletes nothing and returns what would be destroyed with a confirmation_token; " +
	"ask the user, then call again with the same arguments and that token."
