package domain

// ProjectInfo is a flat combined view of a project + its owning org.
// Returned by GET /api/projects/{projectId}/info so callers (notably
// the auth service at JWT-mint time) can embed display names for support
// dashboards/log search without round-tripping multiple endpoints.
type ProjectInfo struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	OrgID       string `json:"orgId"`
	OrgSlug     string `json:"orgSlug"`
	OrgName     string `json:"orgName"`
	// RealtimeAutoEnable controls whether NoSQL auto-create adds new
	// collection tables to the cdc_watcher publication. v1 defaults to
	// true; future: per-project override stored on instances row.
	RealtimeAutoEnable bool `json:"realtimeAutoEnable"`
	// CorsAllowedOrigins is the project's browser-origin allowlist (EXC-23):
	// canonical absolute origins, or the single "*" entry. Empty means the
	// data plane sends no CORS headers for the project. Never null.
	CorsAllowedOrigins []string `json:"corsAllowedOrigins"`
	// RequireEmailVerification and SiteURL are the project's auth settings
	// (EXC-367): whether the auth service must verify a new signup's email
	// before it can log in, and the site URL used to build redirect/callback
	// links. No row = the zero value (verification off, no site URL). Never
	// null.
	RequireEmailVerification bool   `json:"requireEmailVerification"`
	SiteURL                  string `json:"siteUrl"`
	// ProjectOAuthInfo is present only while the authoauth or authotp flag
	// is on (EXC-662); nil keeps its keys off the wire entirely.
	*ProjectOAuthInfo
	// UserAPIEnabled is sent, as true, only while the authuser flag is on;
	// nil keeps the key off the wire.
	UserAPIEnabled *bool `json:"userApiEnabled,omitempty"`
	// StableSubject is sent, as true, only while the authguest flag is on:
	// the auth service then signs sub as the account id, not its address.
	StableSubject *bool `json:"stableSubject,omitempty"`
	// AuthHooks is sent only while the authhooks flag is on, then always
	// (possibly {}); nil keeps the key off the wire.
	AuthHooks *AuthHooksInfo `json:"authHooks,omitempty"`
}

// AuthHooksInfo is the URL of each enabled auth hook (EXC-662); a hook that
// is off has no key.
type AuthHooksInfo struct {
	CustomAccessToken string `json:"customAccessToken,omitempty"`
	BeforeUserCreated string `json:"beforeUserCreated,omitempty"`
	SendEmail         string `json:"sendEmail,omitempty"`
}

// Set records the URL of one enabled hook; an unknown hook is ignored.
func (i *AuthHooksInfo) Set(hook, url string) {
	switch hook {
	case AuthHookCustomAccessToken:
		i.CustomAccessToken = url
	case AuthHookBeforeUserCreated:
		i.BeforeUserCreated = url
	case AuthHookSendEmail:
		i.SendEmail = url
	}
}

// ProjectOAuthInfo is the end-user OAuth policy the auth service enforces:
// where it may redirect, whether sign-up is open, and the providers it may
// offer. Never null inside.
type ProjectOAuthInfo struct {
	RedirectURLs   []string            `json:"redirectUrls"`
	AllowNewUsers  bool                `json:"allowNewUsers"`
	OAuthProviders []OAuthProviderInfo `json:"oauthProviders"`
	// EmailOTPEnabled is sent only while authotp is on.
	EmailOTPEnabled *bool `json:"emailOtpEnabled,omitempty"`
	// GuestSignInEnabled is sent only while authguest is on; the captcha key
	// only when the project also has a Turnstile secret.
	GuestSignInEnabled  *bool `json:"guestSignInEnabled,omitempty"`
	GuestCaptchaEnabled bool  `json:"guestCaptchaEnabled,omitempty"`
}
