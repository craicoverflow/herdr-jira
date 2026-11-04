package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	oauthIssuer       = "https://mcp.atlassian.com"
	registerEndpoint  = oauthIssuer + "/v1/register"
	authorizeEndpoint = oauthIssuer + "/v1/authorize"

	// oauthScope covers everything the plugin's tool surface needs:
	// offline_access for a refresh token, read/write Jira work, and read:me
	// for the signed-in user's identity.
	oauthScope = "offline_access read:jira-work write:jira-work read:me"

	// userAgent is required: mcp.atlassian.com sits behind a Cloudflare WAF
	// that blocks default Go/Python HTTP client User-Agents with a 403
	// (confirmed in the Task 0a spike). Every request to the endpoint must
	// set a browser-like UA.
	userAgent = "Mozilla/5.0 (compatible; herdr-jira/" + pluginVersion + "; +https://github.com/craicoverflow/herdr-jira)"
)

// tokenEndpoint and mcpEndpoint are vars, not consts, so tests can point
// them at an httptest server for the duration of a test.
var tokenEndpoint = oauthIssuer + "/v1/token"

// uaRoundTripper sets a browser-like User-Agent on every request, working
// around the Cloudflare WAF block confirmed in the Task 0a spike.
type uaRoundTripper struct{ next http.RoundTripper }

func (t uaRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("User-Agent", userAgent)
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}

func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: uaRoundTripper{},
		Timeout:   30 * time.Second,
	}
}

// tokenResponse is the OAuth token endpoint's response shape, confirmed
// against the live endpoint in the Task 0a spike.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// registerClient performs OAuth dynamic client registration for redirectURI
// and returns the issued client_id. Confirmed live in the Task 0a spike:
// mcp.atlassian.com accepts token_endpoint_auth_method "none" (no secret)
// and registers the exact loopback redirect URI requested.
func registerClient(ctx context.Context, client *http.Client, redirectURI string) (string, error) {
	return registerClientAt(ctx, client, registerEndpoint, redirectURI)
}

// registerClientAt is registerClient against an explicit endpoint, for testing.
func registerClientAt(ctx context.Context, client *http.Client, endpoint, redirectURI string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"client_name":                "herdr-jira",
		"redirect_uris":              []string{redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("register client: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusForbidden && !strings.Contains(string(data), "error code:") {
		// A bare 403 is the server refusing this client. A Cloudflare WAF
		// block ("error code: 1010") is a different failure and keeps its
		// body in the generic error below.
		return "", fmt.Errorf("register client: %w", errConsentBlocked)
	}
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("register client: %s: %s", resp.Status, string(data))
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("register client: decode response: %w", err)
	}
	if out.ClientID == "" {
		return "", fmt.Errorf("register client: no client_id in response")
	}
	return out.ClientID, nil
}

// buildAuthorizeURL builds the browser-facing consent URL for a PKCE
// authorization-code request.
func buildAuthorizeURL(clientID, redirectURI, state, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {oauthScope},
	}
	return authorizeEndpoint + "?" + q.Encode()
}

// exchangeCode trades an authorization code for tokens.
func exchangeCode(ctx context.Context, client *http.Client, clientID, redirectURI, code, verifier string) (tokenResponse, error) {
	return exchangeCodeAt(ctx, client, tokenEndpoint, clientID, redirectURI, code, verifier)
}

// exchangeCodeAt is exchangeCode against an explicit endpoint, for testing.
func exchangeCodeAt(ctx context.Context, client *http.Client, endpoint, clientID, redirectURI, code, verifier string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	return postTokenForm(ctx, client, endpoint, form)
}

// refreshAccessToken trades a refresh token for a new access token.
//
// mcp.atlassian.com rotates the refresh token on every use (confirmed in
// the Task 0a spike), so callers must persist the returned RefreshToken,
// not just the AccessToken, and should do so under withCredentialsLock to
// avoid losing a rotation to a concurrent refresh.
func refreshAccessToken(ctx context.Context, client *http.Client, clientID, refreshToken string) (tokenResponse, error) {
	return refreshAccessTokenAt(ctx, client, tokenEndpoint, clientID, refreshToken)
}

// refreshAccessTokenAt is refreshAccessToken against an explicit endpoint,
// for testing.
func refreshAccessTokenAt(ctx context.Context, client *http.Client, endpoint, clientID, refreshToken string) (tokenResponse, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
	}
	return postTokenForm(ctx, client, endpoint, form)
}

func postTokenForm(ctx context.Context, client *http.Client, endpoint string, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("token request: %s: %s", resp.Status, string(data))
	}
	var out tokenResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return tokenResponse{}, fmt.Errorf("token request: decode response: %w", err)
	}
	return out, nil
}

func tokenResponseToCredentials(clientID string, tok tokenResponse) credentials {
	return credentials{
		ClientID:     clientID,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second),
		Scope:        tok.Scope,
	}
}
