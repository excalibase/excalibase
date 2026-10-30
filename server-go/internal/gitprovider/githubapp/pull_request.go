package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

var (
	pullRequestsRead = map[string]string{"pull_requests": "read"}
	statusesWrite    = map[string]string{"statuses": "write"}
)

// previewActions are the pull request actions a preview acts on; the rest (labels, reviews) are ignored.
var previewActions = map[string]bool{
	gitprovider.PullRequestOpened: true, gitprovider.PullRequestReopened: true,
	gitprovider.PullRequestSynchronize: true, gitprovider.PullRequestClosed: true,
}

type pullRequestJSON struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Head   struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		Repo *struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Repo struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"base"`
}

// fromFork: a head repository other than the base, or none (the fork was deleted).
func (p pullRequestJSON) fromFork() bool {
	return p.Head.Repo == nil || p.Head.Repo.ID != p.Base.Repo.ID
}

func parsePullRequest(body []byte) (*gitprovider.PullRequest, error) {
	var payload struct {
		Action       string          `json:"action"`
		Installation installationRef `json:"installation"`
		Repository   struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest pullRequestJSON `json:"pull_request"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if !previewActions[payload.Action] {
		return nil, nil
	}
	pr := payload.PullRequest
	return &gitprovider.PullRequest{
		Action: payload.Action, InstallationID: payload.Installation.ID, RepositoryID: payload.Repository.ID,
		Repository: payload.Repository.FullName, Number: pr.Number, Commit: gitprovider.Commit{SHA: pr.Head.SHA, Message: pr.Title},
		HeadBranch: pr.Head.Ref, FromFork: pr.fromFork(),
	}, nil
}

// PullRequest reads a pull request with a token that can read pull requests and nothing else.
func (c *Client) PullRequest(ctx context.Context, installationID int64, repository gitprovider.Repository, number int) (gitprovider.PullRequestState, error) {
	credential, err := c.installationToken(ctx, installationID, repository.ID, pullRequestsRead)
	if err != nil {
		return gitprovider.PullRequestState{}, err
	}
	defer c.revokeQuietly(ctx, credential.Token)
	var found pullRequestJSON
	pullURL := fmt.Sprintf("%s/repos/%s/pulls/%d", c.config.APIURL, repository.FullName, number)
	if err := c.do(ctx, call{method: http.MethodGet, url: pullURL, bearer: credential.Token}, &found); err != nil {
		return gitprovider.PullRequestState{}, err
	}
	return gitprovider.PullRequestState{Open: found.State == "open", HeadSHA: found.Head.SHA, FromFork: found.fromFork()}, nil
}

// SetCommitStatus posts one status with a token that can write statuses and nothing else.
func (c *Client) SetCommitStatus(ctx context.Context, installationID int64, repository gitprovider.Repository, sha string, status gitprovider.CommitStatus) error {
	credential, err := c.installationToken(ctx, installationID, repository.ID, statusesWrite)
	if err != nil {
		return err
	}
	defer c.revokeQuietly(ctx, credential.Token)
	body := map[string]string{"state": status.State, "description": status.Description, "context": status.Context}
	if status.TargetURL != "" {
		body["target_url"] = status.TargetURL
	}
	statusURL := fmt.Sprintf("%s/repos/%s/statuses/%s", c.config.APIURL, repository.FullName, url.PathEscape(sha))
	return c.do(ctx, call{method: http.MethodPost, url: statusURL, bearer: credential.Token, body: body}, nil)
}
