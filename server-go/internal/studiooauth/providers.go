package studiooauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Endpoints locates a provider; tests point it at a fake.
type Endpoints struct {
	AuthURL  string
	TokenURL string
	APIBase  string
}

var (
	GoogleEndpoints = Endpoints{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		APIBase:  "https://openidconnect.googleapis.com/v1",
	}
	GitHubEndpoints = Endpoints{
		AuthURL:  "https://github.com/login/oauth/authorize",
		TokenURL: "https://github.com/login/oauth/access_token",
		APIBase:  "https://api.github.com",
	}
)

// Provider is one sign-in provider Studio offers.
type Provider struct {
	Name      string
	Endpoints Endpoints
	Scopes    []string
	identify  func(ctx context.Context, client *http.Client, apiBase, accessToken string) (Identity, error)
}

func Google(endpoints Endpoints) Provider {
	return Provider{Name: "google", Endpoints: endpoints, Scopes: []string{"openid", "email"}, identify: identifyGoogle}
}

func GitHub(endpoints Endpoints) Provider {
	return Provider{Name: "github", Endpoints: endpoints, Scopes: []string{"read:user", "user:email"}, identify: identifyGitHub}
}

func identifyGoogle(ctx context.Context, client *http.Client, apiBase, accessToken string) (Identity, error) {
	var user struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := getJSON(ctx, client, apiBase+"/userinfo", accessToken, &user); err != nil {
		return Identity{}, err
	}
	if !user.EmailVerified || user.Email == "" {
		return Identity{}, ErrEmailNotVerified
	}
	return Identity{Provider: "google", Subject: user.Sub, Email: user.Email}, nil
}

func identifyGitHub(ctx context.Context, client *http.Client, apiBase, accessToken string) (Identity, error) {
	var user struct {
		ID int64 `json:"id"`
	}
	if err := getJSON(ctx, client, apiBase+"/user", accessToken, &user); err != nil {
		return Identity{}, err
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, apiBase+"/user/emails", accessToken, &emails); err != nil {
		return Identity{}, err
	}
	for _, candidate := range emails {
		if candidate.Primary && candidate.Verified && candidate.Email != "" {
			return Identity{Provider: "github", Subject: strconv.FormatInt(user.ID, 10), Email: candidate.Email}, nil
		}
	}
	return Identity{}, ErrEmailNotVerified
}

func getJSON(ctx context.Context, client *http.Client, url, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("provider identity: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("provider identity: status %d from %s", resp.StatusCode, strings.SplitN(url, "?", 2)[0])
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
