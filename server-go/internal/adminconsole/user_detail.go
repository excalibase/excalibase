package adminconsole

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// UserProfile is who the account is and how far it is trusted.
type UserProfile struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	// TempMail is nil when no disposable-address list is wired: unknown, not "no".
	TempMail        *bool      `json:"tempMail,omitempty"`
	EmailVerified   bool       `json:"emailVerified"`
	EmailVerifiedAt *time.Time `json:"emailVerifiedAt,omitempty"`
	SignedUpAt      *time.Time `json:"signedUpAt,omitempty"`
	LastLoginAt     *time.Time `json:"lastLoginAt,omitempty"`
	LastLoginIP     string     `json:"lastLoginIp,omitempty"`
	// Trust is the account's limited-trial state; nil when none was ever decided.
	Trust *TrustState `json:"trust,omitempty"`
}

// TrustState is the current trust decision (limited or full) and why.
type TrustState struct {
	State     string     `json:"state"`
	Method    string     `json:"method"`
	Reason    string     `json:"reason"`
	DecidedAt *time.Time `json:"decidedAt,omitempty"`
}

// TrustChange is one step of the trust trail.
type TrustChange struct {
	From   string     `json:"from,omitempty"`
	To     string     `json:"to"`
	Method string     `json:"method"`
	Reason string     `json:"reason"`
	Actor  string     `json:"actor"`
	At     *time.Time `json:"at,omitempty"`
}

// UserOrg is an org the account belongs to and its role there.
type UserOrg struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Tier  string `json:"tier"`
	Role  string `json:"role"`
	Owner bool   `json:"owner"`
}

// UserProject is a project in one of the account's orgs.
type UserProject struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	OrgID     string `json:"orgId"`
	OrgName   string `json:"orgName"`
	Status    string `json:"status"`
}

// SuspensionEvent is one suspend or unsuspend, read from the audit trail.
type SuspensionEvent struct {
	Action     string     `json:"action"`
	ActorID    string     `json:"actorId,omitempty"`
	ActorEmail string     `json:"actorEmail,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	At         *time.Time `json:"at,omitempty"`
}

// SuspensionState is whether the account is suspended now and the rule it is for.
type SuspensionState struct {
	Suspended   bool       `json:"suspended"`
	Reason      string     `json:"reason,omitempty"`
	ReasonCode  string     `json:"reasonCode,omitempty"`
	SuspendedAt *time.Time `json:"suspendedAt,omitempty"`
}

// SignInMethod is a way the account can sign in: a password, or a linked provider.
type SignInMethod struct {
	Kind     string     `json:"kind"`
	Provider string     `json:"provider,omitempty"`
	Email    string     `json:"email,omitempty"`
	LinkedAt *time.Time `json:"linkedAt,omitempty"`
}

// UserDetail is everything the admin's account page shows. It carries no
// credential, hash or token.
type UserDetail struct {
	Profile      UserProfile       `json:"profile"`
	Suspension   SuspensionState   `json:"suspension"`
	Suspensions  []SuspensionEvent `json:"suspensions"`
	TrustHistory []TrustChange     `json:"trustHistory"`
	Orgs         []UserOrg         `json:"orgs"`
	Projects     []UserProject     `json:"projects"`
	SignIn       []SignInMethod    `json:"signIn"`
	Audit        []AuditRow        `json:"audit"`
}

// UserDetailReader reads one account's page; ErrUserNotFound when there is none.
type UserDetailReader interface {
	ReadUserDetail(ctx context.Context, userID string) (*UserDetail, error)
}

// UserDetails serves the account page.
type UserDetails struct {
	reader   UserDetailReader
	tempMail func(email string) bool
}

// NewUserDetails reads accounts from reader; tempMail may be nil (the flag then stays unknown).
func NewUserDetails(reader UserDetailReader, tempMail func(email string) bool) *UserDetails {
	return &UserDetails{reader: reader, tempMail: tempMail}
}

// Detail reads one account.
func (u *UserDetails) Detail(ctx context.Context, userID string) (*UserDetail, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, ErrUserNotFound
	}
	detail, err := u.reader.ReadUserDetail(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("user detail: %w", err)
	}
	if u.tempMail != nil {
		flagged := u.tempMail(detail.Profile.Email)
		detail.Profile.TempMail = &flagged
	}
	detail.fillEmptyLists()
	return detail, nil
}

// fillEmptyLists turns missing lists into empty ones, so the page never meets null.
func (d *UserDetail) fillEmptyLists() {
	if d.Suspensions == nil {
		d.Suspensions = []SuspensionEvent{}
	}
	if d.TrustHistory == nil {
		d.TrustHistory = []TrustChange{}
	}
	if d.Orgs == nil {
		d.Orgs = []UserOrg{}
	}
	if d.Projects == nil {
		d.Projects = []UserProject{}
	}
	if d.SignIn == nil {
		d.SignIn = []SignInMethod{}
	}
	if d.Audit == nil {
		d.Audit = []AuditRow{}
	}
	for i := range d.Audit {
		d.Audit[i].Details = ShortDetails(d.Audit[i].Details)
	}
}
