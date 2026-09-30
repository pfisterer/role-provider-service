package keycloak

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client reads users from the Keycloak admin API with a service account. It
// needs nothing beyond realm-management/view-users: it only ever reads.
type Client struct {
	tokenURL     string
	adminURL     string
	clientID     string
	clientSecret string
	http         *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// User is what this package needs of a Keycloak user.
type User struct {
	Email      string              `json:"email"`
	Enabled    bool                `json:"enabled"`
	Attributes map[string][]string `json:"attributes"`
}

// NewClient builds a client for a realm, given as its issuer URL
// ("https://sso.example/realms/main"). The admin API of the same realm lives
// under /admin/realms/<name> on the same host.
func NewClient(realmURL, clientID, clientSecret string) (*Client, error) {
	realmURL = strings.TrimRight(strings.TrimSpace(realmURL), "/")
	base, realm, ok := strings.Cut(realmURL, "/realms/")
	if !ok || realm == "" || strings.Contains(realm, "/") {
		return nil, fmt.Errorf("keycloak realm URL %q must end in /realms/<name>", realmURL)
	}
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("keycloak client id and secret must be set")
	}
	return &Client{
		tokenURL:     realmURL + "/protocol/openid-connect/token",
		adminURL:     base + "/admin/realms/" + realm,
		clientID:     clientID,
		clientSecret: clientSecret,
		http:         &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// pageSize is how many users one admin API call returns.
const pageSize = 100

// ListUsers returns every user in the realm, with attributes.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	var all []User
	for first := 0; ; first += pageSize {
		q := url.Values{
			"first":               {strconv.Itoa(first)},
			"max":                 {strconv.Itoa(pageSize)},
			"briefRepresentation": {"false"},
		}
		var page []User
		if err := c.get(ctx, "/users?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
	}
}

// FindUserByEmail returns the user with exactly this address, or nil.
func (c *Client) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	q := url.Values{"email": {email}, "exact": {"true"}, "briefRepresentation": {"false"}}
	var users []User
	if err := c.get(ctx, "/users?"+q.Encode(), &users); err != nil {
		return nil, err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return &u, nil
		}
	}
	return nil, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.adminURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak admin API: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("keycloak admin API: %s", res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// accessToken returns a cached service-account token, fetching a new one
// shortly before the old one expires.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak token: %w", err)
	}
	defer res.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("keycloak token: %s: %w", res.Status, err)
	}
	if res.StatusCode != http.StatusOK || body.AccessToken == "" {
		return "", fmt.Errorf("keycloak token: %s: %s %s", res.Status, body.Error, body.Description)
	}
	c.token = body.AccessToken
	c.expires = time.Now().Add(time.Duration(max(body.ExpiresIn-30, 10)) * time.Second)
	return c.token, nil
}
