package main

import (
	"context"
	"fmt"
	"net/http"
)

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
// first accessible site's cloudId and URL. A site can appear more than
// once (once per scope bundle, seen in the spike); any entry's id/url pair
// is equally valid since this account only has one site.
func resolveCloudID(ctx context.Context, client *http.Client) (id, siteURL string, err error) {
	raw, err := callTool(ctx, client, "getAccessibleAtlassianResources", map[string]any{})
	if err != nil {
		return "", "", err
	}
	var resources []accessibleResource
	if err := decodeToolResultText(raw, &resources); err != nil {
		return "", "", err
	}
	if len(resources) == 0 {
		return "", "", fmt.Errorf("no accessible Atlassian resources for this account")
	}
	return resources[0].ID, resources[0].URL, nil
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
	return nil
}
