package githubapp_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/gitprovider/githubapp"
	"github.com/excalibase/provisioning-poc/internal/testutil/githubfake"
)

const (
	installationID = int64(77)
	siteRepoID     = int64(1001)
	otherRepoID    = int64(1002)
)

func newClient(t *testing.T) (*githubapp.Client, *githubfake.GitHub) {
	t.Helper()
	fake := githubfake.New(t)
	fake.AddInstallation(installationID, "acme",
		&githubfake.Repo{ID: siteRepoID, FullName: "acme/site", DefaultBranch: "main", Private: true,
			Branches: map[string]string{"main": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "feature/x": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
		&githubfake.Repo{ID: otherRepoID, FullName: "acme/secret", DefaultBranch: "main", Private: true, Branches: map[string]string{}},
	)
	client, err := githubapp.New(githubapp.Config{
		AppID: githubfake.AppID, AppSlug: githubfake.AppSlug, ClientID: githubfake.ClientID, ClientSecret: githubfake.ClientSecret,
		PrivateKeyPEM: fake.PrivateKeyPEM, WebhookSecret: fake.WebhookSecret, APIURL: fake.URL(), WebURL: fake.URL(),
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, fake
}

func TestNew_RefusesAnUnreadableKey(t *testing.T) {
	_, err := githubapp.New(githubapp.Config{AppID: 1, PrivateKeyPEM: "not a key", APIURL: "https://api.github.com", WebURL: "https://github.com"})
	if err == nil {
		t.Fatal("a key that does not parse must refuse the client")
	}
}

func TestCloneCredential_OneRepositoryReadOnly(t *testing.T) {
	client, fake := newClient(t)
	credential, err := client.CloneCredential(context.Background(), installationID, siteRepoID)
	if err != nil {
		t.Fatalf("clone credential: %v", err)
	}
	if credential.Token == "" || credential.ExpiresAt.IsZero() {
		t.Fatalf("credential: %+v", credential)
	}
	issued := fake.Issued()
	if len(issued) != 1 {
		t.Fatalf("issued %d tokens", len(issued))
	}
	if !slices.Equal(issued[0].RepositoryIDs, []int64{siteRepoID}) {
		t.Errorf("token reaches %v, want only the site's repository", issued[0].RepositoryIDs)
	}
	if len(issued[0].Permissions) != 1 || issued[0].Permissions["contents"] != "read" {
		t.Errorf("token permissions %v, want contents:read only", issued[0].Permissions)
	}
}

func TestCloneCredential_UnknownInstallationIsNotFound(t *testing.T) {
	client, _ := newClient(t)
	_, err := client.CloneCredential(context.Background(), 999, siteRepoID)
	if !errors.Is(err, gitprovider.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRevokeCloneCredential(t *testing.T) {
	client, fake := newClient(t)
	credential, err := client.CloneCredential(context.Background(), installationID, siteRepoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RevokeCloneCredential(context.Background(), credential.Token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if !slices.Contains(fake.Revoked(), credential.Token) {
		t.Error("the token was not revoked at GitHub")
	}
}

func TestRepositoryAndBranchHead(t *testing.T) {
	client, fake := newClient(t)
	ctx := context.Background()
	repo, err := client.Repository(ctx, installationID, siteRepoID)
	if err != nil {
		t.Fatalf("repository: %v", err)
	}
	if repo.FullName != "acme/site" || repo.DefaultBranch != "main" || !repo.Private {
		t.Fatalf("repo: %+v", repo)
	}
	head, err := client.BranchHead(ctx, installationID, repo, "feature/x")
	if err != nil {
		t.Fatalf("branch head: %v", err)
	}
	if head.SHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Errorf("head = %+v", head)
	}
	if _, err := client.BranchHead(ctx, installationID, repo, "gone"); !errors.Is(err, gitprovider.ErrNotFound) {
		t.Errorf("missing branch: err = %v, want ErrNotFound", err)
	}
	if client.CloneURL(repo) != fake.URL()+"/acme/site.git" {
		t.Errorf("clone url %s", client.CloneURL(repo))
	}
}

func TestUserAccess_ListsWhatTheUserSeesAndGivesTheTokenBack(t *testing.T) {
	client, fake := newClient(t)
	fake.AddSignIn("code-1", map[int64][]int64{installationID: {siteRepoID}})
	access, err := client.UserAccess(context.Background(), "code-1", "https://api.example.com/cb")
	if err != nil {
		t.Fatalf("user access: %v", err)
	}
	if len(access) != 1 || access[0].Installation.ID != installationID || access[0].Installation.Account != "acme" {
		t.Fatalf("access: %+v", access)
	}
	if len(access[0].Repositories) != 1 || access[0].Repositories[0].ID != siteRepoID {
		t.Errorf("the user sees only acme/site, got %+v", access[0].Repositories)
	}
	if len(fake.RevokedUserTokens()) != 1 {
		t.Error("the user's token must be given back once read")
	}
}

func TestUserAccess_RefusedCode(t *testing.T) {
	client, _ := newClient(t)
	_, err := client.UserAccess(context.Background(), "never-issued", "https://api.example.com/cb")
	if !errors.Is(err, gitprovider.ErrAuthorization) {
		t.Fatalf("err = %v, want ErrAuthorization", err)
	}
}

func TestAuthorizeAndInstallURLs(t *testing.T) {
	client, fake := newClient(t)
	authorize, err := url.Parse(client.AuthorizeURL("st ate", "https://api.example.com/cb"))
	if err != nil {
		t.Fatal(err)
	}
	if authorize.Path != "/login/oauth/authorize" || authorize.Query().Get("client_id") != githubfake.ClientID ||
		authorize.Query().Get("state") != "st ate" || authorize.Query().Get("redirect_uri") != "https://api.example.com/cb" {
		t.Errorf("authorize url %s", authorize)
	}
	install := client.InstallURL("s1")
	if install != fake.URL()+"/apps/"+githubfake.AppSlug+"/installations/new?state=s1" {
		t.Errorf("install url %s", install)
	}
}

func TestParseWebhook_Push(t *testing.T) {
	client, fake := newClient(t)
	body := githubfake.PushPayload(installationID, siteRepoID, "acme/site", "main", strings.Repeat("c", 40), "fix: header\n\nbody")
	header := http.Header{"X-Github-Event": {"push"}, "X-Github-Delivery": {"d-1"}, "X-Hub-Signature-256": {fake.Sign(body)}}
	event, err := client.ParseWebhook(header, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	push := event.Push
	if event.DeliveryID != "d-1" || push == nil || push.InstallationID != installationID || push.RepositoryID != siteRepoID ||
		push.Branch != "main" || push.Commit.SHA != strings.Repeat("c", 40) || push.Commit.Message != "fix: header\n\nbody" || push.Deleted {
		t.Fatalf("event: %+v push %+v", event, push)
	}
}

func TestParseWebhook_RefusesABadOrMissingSignature(t *testing.T) {
	client, fake := newClient(t)
	body := githubfake.PushPayload(installationID, siteRepoID, "acme/site", "main", strings.Repeat("c", 40), "m")
	for name, signature := range map[string]string{
		"missing":   "",
		"tampered":  fake.Sign(append([]byte(" "), body...)),
		"wrong alg": strings.Replace(fake.Sign(body), "sha256=", "sha1=", 1),
		"not hex":   "sha256=zz",
	} {
		t.Run(name, func(t *testing.T) {
			header := http.Header{"X-Github-Event": {"push"}, "X-Hub-Signature-256": {signature}}
			if _, err := client.ParseWebhook(header, body); !errors.Is(err, gitprovider.ErrBadSignature) {
				t.Fatalf("err = %v, want ErrBadSignature", err)
			}
		})
	}
}

func TestParseWebhook_TagsAndDeletesAreNotBranchPushes(t *testing.T) {
	client, fake := newClient(t)
	tag := []byte(`{"ref":"refs/tags/v1","after":"` + strings.Repeat("d", 40) + `","installation":{"id":77},"repository":{"id":1001,"full_name":"acme/site"}}`)
	event, err := client.ParseWebhook(http.Header{"X-Github-Event": {"push"}, "X-Hub-Signature-256": {fake.Sign(tag)}}, tag)
	if err != nil || event.Push != nil {
		t.Errorf("a tag push is no branch push: %+v %v", event, err)
	}
	deleted := []byte(`{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","deleted":true,"installation":{"id":77},"repository":{"id":1001,"full_name":"acme/site"}}`)
	event, err = client.ParseWebhook(http.Header{"X-Github-Event": {"push"}, "X-Hub-Signature-256": {fake.Sign(deleted)}}, deleted)
	if err != nil || event.Push == nil || !event.Push.Deleted {
		t.Errorf("a branch deletion: %+v %v", event, err)
	}
}

func TestParseWebhook_InstallationRemovals(t *testing.T) {
	client, fake := newClient(t)
	deleted := []byte(`{"action":"deleted","installation":{"id":77}}`)
	event, err := client.ParseWebhook(http.Header{"X-Github-Event": {"installation"}, "X-Hub-Signature-256": {fake.Sign(deleted)}}, deleted)
	if err != nil || event.RemovedInstallation != installationID {
		t.Errorf("installation deleted: %+v %v", event, err)
	}
	removed := []byte(`{"action":"removed","installation":{"id":77},"repositories_removed":[{"id":1001},{"id":1002}]}`)
	event, err = client.ParseWebhook(http.Header{"X-Github-Event": {"installation_repositories"}, "X-Hub-Signature-256": {fake.Sign(removed)}}, removed)
	if err != nil || event.RemovedFrom != installationID || !slices.Equal(event.RemovedRepositories, []int64{siteRepoID, otherRepoID}) {
		t.Errorf("repositories removed: %+v %v", event, err)
	}
	ping := []byte(`{"zen":"hi"}`)
	event, err = client.ParseWebhook(http.Header{"X-Github-Event": {"ping"}, "X-Hub-Signature-256": {fake.Sign(ping)}}, ping)
	if err != nil || event.Push != nil || event.RemovedInstallation != 0 {
		t.Errorf("ping: %+v %v", event, err)
	}
}

// Only an uninstall forgets connections; a suspended App comes back with them when unsuspended.
func TestParseWebhook_SuspendKeepsConnections(t *testing.T) {
	client, fake := newClient(t)
	for action, want := range map[string]int64{"deleted": installationID, "suspend": 0, "unsuspend": 0} {
		body := []byte(`{"action":"` + action + `","installation":{"id":` + strconv.FormatInt(installationID, 10) + `}}`)
		header := http.Header{"X-Github-Event": {"installation"}, "X-Github-Delivery": {"d-" + action}, "X-Hub-Signature-256": {fake.Sign(body)}}
		event, err := client.ParseWebhook(header, body)
		if err != nil || event.RemovedInstallation != want {
			t.Errorf("%s: removed %d, want %d (%v)", action, event.RemovedInstallation, want, err)
		}
	}
}
