package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Keycloak reads groups through Keycloak's admin REST API.
//
// It authenticates with the client credentials grant, so the service account it
// uses needs only view-groups on the realms being checked — no write scope of
// any kind. See docs/security.md.
type Keycloak struct {
	baseURL      string
	clientID     string
	clientSecret string
	// authRealm holds the client's own credentials, which is often master
	// while the groups being checked live in a tenant realm.
	authRealm string
	client    *http.Client

	mu    sync.Mutex
	token string
	// expires is deliberately pessimistic: the token is refreshed early rather
	// than retried on a 401 in the middle of a run.
	expires time.Time
}

// KeycloakConfig configures a Keycloak provider.
type KeycloakConfig struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	AuthRealm    string
	Client       *http.Client
}

// NewKeycloak returns a Keycloak provider.
func NewKeycloak(cfg KeycloakConfig) (*Keycloak, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("keycloak base url is required")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, fmt.Errorf("keycloak client id and secret are required")
	}
	if cfg.AuthRealm == "" {
		cfg.AuthRealm = "master"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Keycloak{
		baseURL:      strings.TrimSuffix(cfg.BaseURL, "/"),
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		authRealm:    cfg.AuthRealm,
		client:       cfg.Client,
	}, nil
}

// group is the slice of Keycloak's group representation this package reads.
type group struct {
	Name      string  `json:"name"`
	Path      string  `json:"path"`
	SubGroups []group `json:"subGroups"`
}

// GroupExists implements Provider.
func (k *Keycloak) GroupExists(ctx context.Context, realm, name string) (bool, error) {
	token, err := k.accessToken(ctx)
	if err != nil {
		return false, err
	}

	endpoint := fmt.Sprintf("%s/admin/realms/%s/groups?%s", k.baseURL, url.PathEscape(realm),
		url.Values{"search": {strings.TrimPrefix(name, "/")}, "exact": {"true"}}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := k.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("keycloak groups: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		// A missing realm is a broken check configuration, not a missing
		// group, and must not read as "the group is gone".
		return false, fmt.Errorf("keycloak realm %q not found", realm)
	default:
		return false, fmt.Errorf("keycloak groups: %s", resp.Status)
	}

	var groups []group
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		return false, fmt.Errorf("decode keycloak groups: %w", err)
	}
	return matches(groups, name), nil
}

// matches walks the group tree Keycloak returns. A runbook may name a group by
// its bare name or by its full path, and subgroups are returned nested.
func matches(groups []group, want string) bool {
	wantPath := "/" + strings.Trim(want, "/")
	for _, g := range groups {
		if g.Name == want || g.Path == wantPath {
			return true
		}
		if matches(g.SubGroups, want) {
			return true
		}
	}
	return false
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func (k *Keycloak) accessToken(ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.token != "" && time.Now().Before(k.expires) {
		return k.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {k.clientID},
		"client_secret": {k.clientSecret},
	}
	endpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", k.baseURL, url.PathEscape(k.authRealm))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		// The response body of a failed token request can echo credentials
		// back, so only the status is reported.
		return "", fmt.Errorf("keycloak token: %s", resp.Status)
	}

	var token tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("decode keycloak token: %w", err)
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("keycloak token: response carried no access token")
	}
	lifetime := time.Duration(token.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Minute
	}
	k.token = token.AccessToken
	k.expires = time.Now().Add(lifetime - 30*time.Second)
	return k.token, nil
}
