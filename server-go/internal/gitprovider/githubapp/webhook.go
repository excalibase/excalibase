package githubapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

const branchRefPrefix = "refs/heads/"

// ParseWebhook checks X-Hub-Signature-256 over the raw body before reading anything in it.
func (c *Client) ParseWebhook(header http.Header, body []byte) (gitprovider.Event, error) {
	if !validSignature(c.config.WebhookSecret, header.Get("X-Hub-Signature-256"), body) {
		return gitprovider.Event{}, gitprovider.ErrBadSignature
	}
	event := gitprovider.Event{DeliveryID: header.Get("X-GitHub-Delivery")}
	var err error
	switch header.Get("X-GitHub-Event") {
	case "push":
		event.Push, err = parsePush(body)
	case "pull_request":
		event.PullRequest, err = parsePullRequest(body)
	case "installation":
		event.RemovedInstallation, err = parseInstallation(body)
	case "installation_repositories":
		event.RemovedFrom, event.RemovedRepositories, err = parseRepositoriesRemoved(body)
	}
	if err != nil {
		return gitprovider.Event{}, fmt.Errorf("read the %s event: %w", header.Get("X-GitHub-Event"), err)
	}
	return event, nil
}

func validSignature(secret, header string, body []byte) bool {
	hexMAC, ok := strings.CutPrefix(header, "sha256=")
	if secret == "" || !ok {
		return false
	}
	given, err := hex.DecodeString(hexMAC)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(given, mac.Sum(nil))
}

type installationRef struct {
	ID int64 `json:"id"`
}

func parsePush(body []byte) (*gitprovider.Push, error) {
	var payload struct {
		Ref          string          `json:"ref"`
		After        string          `json:"after"`
		Deleted      bool            `json:"deleted"`
		Installation installationRef `json:"installation"`
		Repository   struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		} `json:"repository"`
		HeadCommit *struct {
			Message string `json:"message"`
		} `json:"head_commit"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	branch, isBranch := strings.CutPrefix(payload.Ref, branchRefPrefix)
	if !isBranch {
		return nil, nil
	}
	push := &gitprovider.Push{
		InstallationID: payload.Installation.ID, RepositoryID: payload.Repository.ID, Repository: payload.Repository.FullName,
		Branch: branch, Commit: gitprovider.Commit{SHA: payload.After},
		Deleted: payload.Deleted || strings.Trim(payload.After, "0") == "",
	}
	if payload.HeadCommit != nil {
		push.Commit.Message = payload.HeadCommit.Message
	}
	return push, nil
}

// parseInstallation answers an uninstalled installation; a suspended one keeps its connections and simply fails to clone.
func parseInstallation(body []byte) (int64, error) {
	var payload struct {
		Action       string          `json:"action"`
		Installation installationRef `json:"installation"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, err
	}
	if payload.Action == "deleted" {
		return payload.Installation.ID, nil
	}
	return 0, nil
}

func parseRepositoriesRemoved(body []byte) (int64, []int64, error) {
	var payload struct {
		Action       string            `json:"action"`
		Installation installationRef   `json:"installation"`
		Removed      []installationRef `json:"repositories_removed"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, nil, err
	}
	if payload.Action != "removed" || len(payload.Removed) == 0 {
		return 0, nil, nil
	}
	ids := make([]int64, 0, len(payload.Removed))
	for _, repository := range payload.Removed {
		ids = append(ids, repository.ID)
	}
	return payload.Installation.ID, ids, nil
}
