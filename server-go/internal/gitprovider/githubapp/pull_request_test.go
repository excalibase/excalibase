package githubapp_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/gitprovider/githubapp"
	"github.com/excalibase/provisioning-poc/internal/testutil/githubfake"
)

func deliverPR(t *testing.T, client *githubapp.Client, fake *githubfake.GitHub, body []byte) (gitprovider.Event, error) {
	t.Helper()
	header := http.Header{"X-Github-Event": {"pull_request"}, "X-Github-Delivery": {"d-pr"}, "X-Hub-Signature-256": {fake.Sign(body)}}
	return client.ParseWebhook(header, body)
}

func TestParseWebhook_PullRequestFromTheSameRepository(t *testing.T) {
	client, fake := newClient(t)
	sha := strings.Repeat("e", 40)
	body := githubfake.PullRequestPayload(githubfake.PullRequestEvent{Action: "synchronize", Installation: installationID, RepoID: siteRepoID,
		FullName: "acme/site", Number: 12, HeadSHA: sha, HeadBranch: "feat/x", HeadRepoID: siteRepoID, Title: "Add header"})
	event, err := deliverPR(t, client, fake, body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pr := event.PullRequest
	if pr == nil || pr.Action != "synchronize" || pr.InstallationID != installationID || pr.RepositoryID != siteRepoID || pr.Repository != "acme/site" ||
		pr.Number != 12 || pr.Commit.SHA != sha || pr.Commit.Message != "Add header" || pr.HeadBranch != "feat/x" || pr.FromFork {
		t.Fatalf("pull request = %+v", pr)
	}
}

func TestParseWebhook_PullRequestFromAForkOrADeletedForkIsAFork(t *testing.T) {
	client, fake := newClient(t)
	for name, headRepo := range map[string]int64{"fork": 999, "deleted fork": 0} {
		body := githubfake.PullRequestPayload(githubfake.PullRequestEvent{Action: "opened", Installation: installationID, RepoID: siteRepoID,
			FullName: "acme/site", Number: 3, HeadSHA: strings.Repeat("f", 40), HeadBranch: "main", HeadRepoID: headRepo})
		event, err := deliverPR(t, client, fake, body)
		if err != nil || event.PullRequest == nil || !event.PullRequest.FromFork {
			t.Errorf("%s: %+v %v", name, event.PullRequest, err)
		}
	}
}

func TestParseWebhook_PullRequestActionsWeDoNotUseAreIgnored(t *testing.T) {
	client, fake := newClient(t)
	body := githubfake.PullRequestPayload(githubfake.PullRequestEvent{Action: "labeled", Installation: installationID, RepoID: siteRepoID,
		FullName: "acme/site", Number: 3, HeadSHA: strings.Repeat("f", 40), HeadRepoID: siteRepoID})
	event, err := deliverPR(t, client, fake, body)
	if err != nil || event.PullRequest != nil {
		t.Fatalf("labeled: %+v %v", event.PullRequest, err)
	}
}

func TestPullRequest_ReadsStateAndHeadWithAReadOnlyToken(t *testing.T) {
	client, fake := newClient(t)
	sha := strings.Repeat("a", 40)
	fake.SetPullRequest(siteRepoID, githubfake.PullRequest{Number: 12, Open: true, HeadSHA: sha, HeadRepoID: siteRepoID})
	state, err := client.PullRequest(context.Background(), installationID, gitprovider.Repository{ID: siteRepoID, FullName: "acme/site"}, 12)
	if err != nil || !state.Open || state.HeadSHA != sha || state.FromFork {
		t.Fatalf("state = %+v, %v", state, err)
	}
	issued := fake.Issued()
	last := issued[len(issued)-1]
	if len(last.Permissions) != 1 || last.Permissions["pull_requests"] != "read" {
		t.Fatalf("token permissions = %v, want pull_requests:read only", last.Permissions)
	}
	if revoked := fake.Revoked(); len(revoked) == 0 || revoked[len(revoked)-1] != last.Token {
		t.Fatal("the read token was not given back")
	}
	if _, err := client.PullRequest(context.Background(), installationID, gitprovider.Repository{ID: siteRepoID, FullName: "acme/site"}, 99); !errors.Is(err, gitprovider.ErrNotFound) {
		t.Fatalf("unknown PR err = %v", err)
	}
}

func TestSetCommitStatus_WritesOneStatusWithAStatusOnlyToken(t *testing.T) {
	client, fake := newClient(t)
	sha := strings.Repeat("b", 40)
	status := gitprovider.CommitStatus{State: gitprovider.StatusSuccess, TargetURL: "https://shop--abc.excalibase.app",
		Description: "Preview ready", Context: "excalibase/preview shop"}
	if err := client.SetCommitStatus(context.Background(), installationID, gitprovider.Repository{ID: siteRepoID, FullName: "acme/site"}, sha, status); err != nil {
		t.Fatal(err)
	}
	statuses := fake.Statuses(sha)
	if len(statuses) != 1 || statuses[0] != status {
		t.Fatalf("statuses = %+v", statuses)
	}
	issued := fake.Issued()
	if permissions := issued[len(issued)-1].Permissions; len(permissions) != 1 || permissions["statuses"] != "write" {
		t.Fatalf("token permissions = %v, want statuses:write only", permissions)
	}
}
