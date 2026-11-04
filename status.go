package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

var (
	errNoSite   = errors.New("No Jira Cloud site is available to this account.")
	errNotCloud = errors.New("This site is not Jira Cloud.")
)

// errMultipleSites is returned for an account with several Atlassian sites,
// which is not supported yet.
type errMultipleSites struct{ names []string }

func (e errMultipleSites) Error() string {
	return fmt.Sprintf("This account has access to several Jira sites (%s). Only one is supported.", strings.Join(e.names, ", "))
}

// accessibleResource is one entry from getAccessibleAtlassianResources,
// shape confirmed live in the Task 0a/0c spikes.
type accessibleResource struct {
	ID     string   `json:"id"`
	URL    string   `json:"url"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// userInfo is atlassianUserInfo's result. Only the fields this plugin
// displays are decoded; unknown fields are ignored. account_id (snake_case,
// confirmed live) is the id "assign to me" needs for fields.assignee.accountId.
type userInfo struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	AccountID string `json:"account_id"`
}

// resolveCloudID calls getAccessibleAtlassianResources and returns the
// single accessible site's cloudId and URL. A site can appear more than
// once (once per scope bundle, seen in the spike), so the list is
// de-duplicated by id first. Zero sites, several sites and a non-Cloud URL
// each get their own error.
func resolveCloudID(ctx context.Context, client *http.Client) (id, siteURL string, err error) {
	raw, err := callTool(ctx, client, "getAccessibleAtlassianResources", map[string]any{})
	if err != nil {
		return "", "", err
	}
	var resources []accessibleResource
	if err := decodeToolResultText(raw, &resources); err != nil {
		return "", "", err
	}
	var sites []accessibleResource
	seen := map[string]bool{}
	for _, r := range resources {
		if !seen[r.ID] {
			seen[r.ID] = true
			sites = append(sites, r)
		}
	}
	switch len(sites) {
	case 0:
		return "", "", errNoSite
	case 1:
	default:
		names := make([]string, len(sites))
		for i, s := range sites {
			names[i] = s.Name
			if names[i] == "" {
				names[i] = s.URL
			}
		}
		return "", "", errMultipleSites{names: names}
	}
	u, err := url.Parse(sites[0].URL)
	if err != nil || !strings.HasSuffix(u.Hostname(), ".atlassian.net") {
		return "", "", errNotCloud
	}
	return sites[0].ID, sites[0].URL, nil
}

func fetchUserInfo(ctx context.Context, client *http.Client) (userInfo, error) {
	raw, err := callTool(ctx, client, "atlassianUserInfo", map[string]any{})
	if err != nil {
		return userInfo{}, err
	}
	var info userInfo
	if err := decodeToolResultText(raw, &info); err != nil {
		return userInfo{}, err
	}
	return info, nil
}

// ensureCloudID returns the cached cloudId if one was already resolved and
// stored, or resolves and caches it now.
func ensureCloudID(ctx context.Context, client *http.Client) (string, error) {
	creds, err := loadCredentials()
	if err == nil && creds.CloudID != "" {
		return creds.CloudID, nil
	}
	cloudID, siteURL, err := resolveCloudID(ctx, client)
	if err != nil {
		return "", err
	}
	if err := withCredentialsLock(func(creds credentials, loadErr error) (credentials, error) {
		if loadErr != nil {
			return credentials{}, loadErr
		}
		creds.CloudID = cloudID
		creds.SiteURL = siteURL
		return creds, nil
	}); err != nil {
		return "", fmt.Errorf("cache cloud id: %w", err)
	}
	return cloudID, nil
}

// ensureAccountID returns the cached accountId if one was already resolved
// and stored, or resolves and caches it now. Follows ensureCloudID's exact
// pattern.
func ensureAccountID(ctx context.Context, client *http.Client) (string, error) {
	creds, err := loadCredentials()
	if err == nil && creds.AccountID != "" {
		return creds.AccountID, nil
	}
	info, err := fetchUserInfo(ctx, client)
	if err != nil {
		return "", err
	}
	if err := withCredentialsLock(func(creds credentials, loadErr error) (credentials, error) {
		if loadErr != nil {
			return credentials{}, loadErr
		}
		creds.AccountID = info.AccountID
		return creds, nil
	}); err != nil {
		return "", fmt.Errorf("cache account id: %w", err)
	}
	return info.AccountID, nil
}

// status prints who the plugin is signed in as, their Jira site, and the
// cached cloudId. It never prints tokens.
func status(ctx context.Context) error {
	client := newHTTPClient()

	cloudID, err := ensureCloudID(ctx, client)
	if err != nil {
		return err
	}
	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	siteURL := creds.SiteURL

	info, err := fetchUserInfo(ctx, client)
	if err != nil {
		return err
	}

	fmt.Printf("Signed in as: %s", info.Name)
	if info.Email != "" {
		fmt.Printf(" <%s>", info.Email)
	}
	fmt.Println()
	fmt.Println("Site:", siteURL)
	fmt.Println("Cloud ID:", cloudID)
	cfg, _ := loadConfig()
	if cfg.KeyPattern != "" {
		fmt.Println("Branch key pattern:", cfg.KeyPattern, "(from config)")
	} else {
		fmt.Println("Branch key pattern: auto (confirmed with Jira)")
	}
	return nil
}
