package githubfake

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

// PullRequestEvent is what PullRequestPayload writes; HeadRepoID 0 is a deleted fork.
type PullRequestEvent struct {
	Action       string
	Installation int64
	RepoID       int64
	FullName     string
	Number       int
	HeadSHA      string
	HeadBranch   string
	HeadRepoID   int64
	Title        string
}

// PullRequestPayload is a pull_request event body as GitHub sends it.
func PullRequestPayload(event PullRequestEvent) []byte {
	head := map[string]any{"sha": event.HeadSHA, "ref": event.HeadBranch, "repo": nil}
	if event.HeadRepoID != 0 {
		head["repo"] = map[string]any{"id": event.HeadRepoID}
	}
	body, _ := json.Marshal(map[string]any{
		"action": event.Action, "number": event.Number,
		"installation": map[string]any{"id": event.Installation},
		"repository":   map[string]any{"id": event.RepoID, "full_name": event.FullName},
		"pull_request": map[string]any{"number": event.Number, "state": "open", "title": event.Title, "head": head,
			"base": map[string]any{"repo": map[string]any{"id": event.RepoID}}},
	})
	return body
}

// PullRequest is one pull request the fake serves.
type PullRequest struct {
	Number     int
	Open       bool
	HeadSHA    string
	HeadRepoID int64
}

// SetPullRequest adds or replaces a pull request of a repo.
func (g *GitHub) SetPullRequest(repoID int64, pr PullRequest) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pulls == nil {
		g.pulls = map[int64]map[int]PullRequest{}
	}
	if g.pulls[repoID] == nil {
		g.pulls[repoID] = map[int]PullRequest{}
	}
	g.pulls[repoID][pr.Number] = pr
}

// Statuses answers every status posted on a commit, oldest first.
func (g *GitHub) Statuses(sha string) []gitprovider.CommitStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gitprovider.CommitStatus(nil), g.statuses[sha]...)
}

func (g *GitHub) pullRequest(w http.ResponseWriter, r *http.Request) {
	fullName := r.PathValue("owner") + "/" + r.PathValue("repo")
	repo, ok := g.scopedRepo(r, func(repo *Repo) bool { return repo.FullName == fullName })
	if !ok {
		notFound(w)
		return
	}
	number, _ := strconv.Atoi(r.PathValue("number"))
	g.mu.Lock()
	pr, held := g.pulls[repo.ID][number]
	g.mu.Unlock()
	if !held {
		notFound(w)
		return
	}
	state := "closed"
	if pr.Open {
		state = "open"
	}
	head := map[string]any{"sha": pr.HeadSHA, "repo": nil}
	if pr.HeadRepoID != 0 {
		head["repo"] = map[string]any{"id": pr.HeadRepoID}
	}
	writeJSON(w, http.StatusOK, map[string]any{"number": number, "state": state, "head": head,
		"base": map[string]any{"repo": map[string]any{"id": repo.ID}}})
}

func (g *GitHub) createStatus(w http.ResponseWriter, r *http.Request) {
	fullName := r.PathValue("owner") + "/" + r.PathValue("repo")
	if _, ok := g.scopedRepo(r, func(repo *Repo) bool { return repo.FullName == fullName }); !ok {
		notFound(w)
		return
	}
	var body struct {
		State       string `json:"state"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "bad body"})
		return
	}
	g.mu.Lock()
	if g.statuses == nil {
		g.statuses = map[string][]gitprovider.CommitStatus{}
	}
	sha := r.PathValue("sha")
	g.statuses[sha] = append(g.statuses[sha], gitprovider.CommitStatus{State: body.State, TargetURL: body.TargetURL,
		Description: body.Description, Context: body.Context})
	g.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"state": body.State})
}

// DeletePayload is a push event body for a deleted branch.
func DeletePayload(installationID, repoID int64, fullName, branch string) []byte {
	body, _ := json.Marshal(map[string]any{
		"ref": "refs/heads/" + branch, "after": "0000000000000000000000000000000000000000", "deleted": true,
		"installation": map[string]any{"id": installationID},
		"repository":   map[string]any{"id": repoID, "full_name": fullName},
	})
	return body
}

// DeleteBranch removes a branch of a repo.
func (g *GitHub) DeleteBranch(repoID int64, branch string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, inst := range g.installations {
		if repo, ok := inst.repos[repoID]; ok {
			delete(repo.Branches, branch)
		}
	}
}
