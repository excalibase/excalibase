package domain

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestAuthHookNames_AreTheThreeHooksInOrder(t *testing.T) {
	got := AuthHookNames()
	want := []string{"custom_access_token", "before_user_created", "send_email"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v", got)
	}
	got[0] = "changed"
	if AuthHookNames()[0] != "custom_access_token" {
		t.Fatal("the caller can change the list")
	}
	for _, name := range want {
		if !IsAuthHook(name) {
			t.Errorf("%s is not a hook", name)
		}
	}
	for _, name := range []string{"", "Send_Email", "after_sign_in"} {
		if IsAuthHook(name) {
			t.Errorf("%q is a hook", name)
		}
	}
}

func TestAllAuthHooks_FillsTheMissingHooksInOrder(t *testing.T) {
	stored := []ProjectAuthHook{
		{Hook: AuthHookSendEmail, Enabled: true, URL: "https://mail.example.com/hook"},
		{Hook: "retired_hook", Enabled: true, URL: "https://old.example.com"},
	}
	got := AllAuthHooks(stored)
	if len(got) != 3 || got[0].Hook != AuthHookCustomAccessToken || got[1].Hook != AuthHookBeforeUserCreated || got[2].Hook != AuthHookSendEmail {
		t.Fatalf("hooks = %+v", got)
	}
	if got[0].Enabled || got[0].URL != "" || !got[2].Enabled || got[2].URL != "https://mail.example.com/hook" {
		t.Fatalf("hooks = %+v", got)
	}
}

func TestAuthHooksSecretPath_IsUnderTheProject(t *testing.T) {
	if got := AuthHooksSecretPath("proj-1"); got != "projects/proj-1/auth/hooks" {
		t.Fatalf("path = %q", got)
	}
}

func TestValidateAuthHookURL_AcceptsOnePublicHTTPSHost(t *testing.T) {
	for raw, want := range map[string]string{
		"https://hooks.example.com/claims":                       "https://hooks.example.com/claims",
		"  https://Hooks.Example.COM/claims?v=1  ":               "https://hooks.example.com/claims?v=1",
		"https://hooks.example.com:443/claims":                   "https://hooks.example.com:443/claims",
		"https://[2001:4860:4860::8888]/hook":                    "https://[2001:4860:4860::8888]/hook",
		"https://203.0.113.9/hook":                               "https://203.0.113.9/hook",
		"https://hooks.example.com":                              "https://hooks.example.com",
		"https://hooks.example.com/" + strings.Repeat("a", 2000): "https://hooks.example.com/" + strings.Repeat("a", 2000),
	} {
		got, err := ValidateAuthHookURL(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v", raw, got, err)
		}
	}
}

func TestValidateAuthHookURL_RefusesWhatCouldReachInside(t *testing.T) {
	for _, raw := range []string{
		"",
		"http://hooks.example.com/claims",
		"ftp://hooks.example.com",
		"https://",
		"https:///path",
		"https://user:pass@hooks.example.com/",
		"https://user@hooks.example.com/",
		"https://hooks.example.com/#frag",
		"https://localhost/hook",
		"https://hooks.internal/hook",
		"https://service.default.svc.cluster.local/hook",
		"https://vault/hook",
		"https://127.0.0.1/hook",
		"https://10.0.0.5/hook",
		"https://169.254.169.254/latest",
		"https://[::1]/hook",
		"https://[fe80::1%25eth0]/hook",
		"https://100.64.0.1/hook",
		"https://hooks.example.com/" + strings.Repeat("a", 2048),
		"https://hooks.example.com/a\nb",
		"https:hooks.example.com",
		"https://hooks.example.com:notaport/",
		"https://hooks.example.com:8443/claims",
		"https://hooks.example.com:80/",
		"https://[2001:4860:4860::8888]:8443/hook",
		"https://127.1/hook",
		"https://0x7f.1/hook",
		"https://hooks.0x7f/hook",
		"https://10.1.2/hook",
		"https://hooks.123/hook",
	} {
		if got, err := ValidateAuthHookURL(raw); err == nil {
			t.Errorf("%q accepted as %q", raw, got)
		} else if !errors.Is(err, ErrInvalidAuthHookURL) {
			t.Errorf("%q: %v is not ErrInvalidAuthHookURL", raw, err)
		}
	}
}

func TestAuthHookURLHost_IsTheHostOnly(t *testing.T) {
	for raw, want := range map[string]string{
		"https://hooks.example.com:8443/claims?token=abc": "hooks.example.com:8443",
		"https://hooks.example.com/claims?token=abc":      "hooks.example.com",
		"":       "",
		"%zz://": "",
	} {
		if got := AuthHookURLHost(raw); got != want {
			t.Errorf("%q: host %q, want %q", raw, got, want)
		}
	}
}

func TestGenerateAuthHookSecret_IsAFreshStandardWebhooksSecret(t *testing.T) {
	first, err := GenerateAuthHookSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := GenerateAuthHookSecret()
	if first == second || !strings.HasPrefix(first, "whsec_") {
		t.Fatalf("secrets %q %q", first, second)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(first, "whsec_"))
	if err != nil || len(key) != 32 {
		t.Fatalf("key = %d bytes, %v", len(key), err)
	}
}

func TestGenerateAuthHookSecret_ReportsARandomFailure(t *testing.T) {
	failing := func([]byte) (int, error) { return 0, errors.New("no entropy") }
	if _, err := generateAuthHookSecret(failing); err == nil {
		t.Fatal("a random failure was not reported")
	}
}

func TestAuthHooksInfo_SetNamesEachHookByItsKey(t *testing.T) {
	info := &AuthHooksInfo{}
	for i, name := range AuthHookNames() {
		info.Set(name, "https://h"+string(rune('a'+i))+".example.com")
	}
	info.Set("retired_hook", "https://old.example.com")
	want := AuthHooksInfo{CustomAccessToken: "https://ha.example.com", BeforeUserCreated: "https://hb.example.com", SendEmail: "https://hc.example.com"}
	if *info != want {
		t.Fatalf("info = %+v", *info)
	}
}

func TestValidateAuthHookURL_NamesThePortRule(t *testing.T) {
	if _, err := ValidateAuthHookURL("https://hooks.example.com:8443/"); !errors.Is(err, ErrAuthHookURLPort) || !strings.Contains(err.Error(), "port 443") {
		t.Fatalf("err = %v", err)
	}
}
