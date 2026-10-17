package githubapp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

// UserAccess exchanges the sign-in code for a user token, lists the App's
// installations and the repositories in them that user can read, then gives
// the token back. Only what the user may see is ever offered for connecting.
func (c *Client) UserAccess(ctx context.Context, code, redirectURL string) ([]gitprovider.Access, error) {
	token, err := c.exchangeCode(ctx, code, redirectURL)
	if err != nil {
		return nil, err
	}
	defer c.revokeUserToken(ctx, token)
	var listed struct {
		Installations []struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
			} `json:"account"`
		} `json:"installations"`
	}
	installationsURL := fmt.Sprintf("%s/user/installations?per_page=%d", c.config.APIURL, maxInstallations)
	if err := c.do(ctx, call{method: http.MethodGet, url: installationsURL, bearer: token}, &listed); err != nil {
		return nil, fmt.Errorf("list the user's installations: %w", err)
	}
	access := make([]gitprovider.Access, 0, len(listed.Installations))
	for _, installation := range listed.Installations {
		repositories, err := c.userRepositories(ctx, token, installation.ID)
		if err != nil {
			return nil, err
		}
		access = append(access, gitprovider.Access{
			Installation: gitprovider.Installation{ID: installation.ID, Account: installation.Account.Login},
			Repositories: repositories,
		})
	}
	return access, nil
}

func (c *Client) exchangeCode(ctx context.Context, code, redirectURL string) (string, error) {
	var exchanged struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	form := url.Values{"client_id": {c.config.ClientID}, "client_secret": {c.config.ClientSecret}, "code": {code}, "redirect_uri": {redirectURL}}
	if err := c.do(ctx, call{method: http.MethodPost, url: c.config.WebURL + "/login/oauth/access_token", form: form}, &exchanged); err != nil {
		return "", fmt.Errorf("exchange the GitHub sign-in code: %w", err)
	}
	if exchanged.AccessToken == "" {
		return "", fmt.Errorf("%w (%s)", gitprovider.ErrAuthorization, exchanged.Error)
	}
	return exchanged.AccessToken, nil
}

func (c *Client) userRepositories(ctx context.Context, token string, installationID int64) ([]gitprovider.Repository, error) {
	var repositories []gitprovider.Repository
	for page := 1; page <= maxRepoPages; page++ {
		var listed struct {
			Repositories []repositoryJSON `json:"repositories"`
		}
		pageURL := fmt.Sprintf("%s/user/installations/%d/repositories?per_page=%d&page=%d", c.config.APIURL, installationID, pageSize, page)
		if err := c.do(ctx, call{method: http.MethodGet, url: pageURL, bearer: token}, &listed); err != nil {
			return nil, fmt.Errorf("list the repositories of installation %d: %w", installationID, err)
		}
		for _, repository := range listed.Repositories {
			repositories = append(repositories, repository.repository())
		}
		if len(listed.Repositories) < pageSize {
			break
		}
	}
	return repositories, nil
}

// revokeUserToken gives the user's token back; it was needed for one listing only.
func (c *Client) revokeUserToken(ctx context.Context, token string) {
	err := c.do(context.WithoutCancel(ctx), call{
		method: http.MethodDelete, url: c.config.APIURL + "/applications/" + url.PathEscape(c.config.ClientID) + "/token",
		body: map[string]string{"access_token": token}, basicUser: c.config.ClientID, basicSecret: c.config.ClientSecret,
	}, nil)
	if err != nil {
		log.Printf("github: give back a user token after listing its installations: %v", err)
	}
}
