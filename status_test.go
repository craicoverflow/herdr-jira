package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// resolveWith runs resolveCloudID against a fake endpoint that returns
// resources as getAccessibleAtlassianResources' text payload.
func resolveWith(t *testing.T, resources []accessibleResource) (string, string, error) {
	t.Helper()
	withSignedInCredentials(t)
	payload, _ := json.Marshal(resources)
	srv := newFakeMCPServer(t, map[string]string{"getAccessibleAtlassianResources": string(payload)})
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()
	return resolveCloudID(context.Background(), srv.Client())
}

func TestResolveCloudIDDeduplicatesSameSite(t *testing.T) {
	site := accessibleResource{ID: "c1", URL: "https://example.atlassian.net", Name: "example"}
	id, url, err := resolveWith(t, []accessibleResource{site, site})
	if err != nil || id != "c1" || url != "https://example.atlassian.net" {
		t.Errorf("got (%q, %q, %v), want the one site", id, url, err)
	}
}

func TestResolveCloudIDStage0Fixture(t *testing.T) {
	data, err := os.ReadFile("testdata/stage0/accessible_resources.json")
	if err != nil {
		t.Fatal(err)
	}
	var rs []accessibleResource
	if err := json.Unmarshal(data, &rs); err != nil {
		t.Fatal(err)
	}
	if _, _, err := resolveWith(t, rs); err != nil {
		t.Errorf("fixture site rejected: %v", err)
	}
}

func TestResolveCloudIDNoSite(t *testing.T) {
	_, _, err := resolveWith(t, nil)
	if !errors.Is(err, errNoSite) {
		t.Errorf("err = %v, want errNoSite", err)
	}
}

func TestResolveCloudIDSeveralSitesNamesThem(t *testing.T) {
	_, _, err := resolveWith(t, []accessibleResource{
		{ID: "c1", URL: "https://a.atlassian.net", Name: "alpha"},
		{ID: "c2", URL: "https://b.atlassian.net", Name: "beta"},
	})
	var multi errMultipleSites
	if !errors.As(err, &multi) {
		t.Fatalf("err = %v, want errMultipleSites", err)
	}
	if !strings.Contains(err.Error(), "alpha, beta") {
		t.Errorf("err = %q, want both site names", err)
	}
}

func TestResolveCloudIDRejectsNonCloudURL(t *testing.T) {
	_, _, err := resolveWith(t, []accessibleResource{{ID: "c1", URL: "https://jira.example.com", Name: "dc"}})
	if !errors.Is(err, errNotCloud) {
		t.Errorf("err = %v, want errNotCloud", err)
	}
	// A look-alike host must not pass a suffix check on the whole URL.
	_, _, err = resolveWith(t, []accessibleResource{{ID: "c1", URL: "https://evil.example/x.atlassian.net", Name: "x"}})
	if !errors.Is(err, errNotCloud) {
		t.Errorf("err = %v, want errNotCloud for a path that merely ends in .atlassian.net", err)
	}
}

func TestResolveCloudIDAcceptsTrailingSlash(t *testing.T) {
	_, _, err := resolveWith(t, []accessibleResource{{ID: "c1", URL: "https://example.atlassian.net/", Name: "example"}})
	if err != nil {
		t.Errorf("err = %v, want a trailing slash accepted", err)
	}
}

func TestEnsureCloudIDSurfacesSiteErrorAndStoresNothing(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{AccessToken: "tok", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	srv := newFakeMCPServer(t, map[string]string{"getAccessibleAtlassianResources": "[]"})
	defer srv.Close()
	orig := mcpEndpoint
	mcpEndpoint = srv.URL
	defer func() { mcpEndpoint = orig }()

	// signIn calls ensureCloudID, so this is the error it would surface.
	_, err := ensureCloudID(context.Background(), srv.Client())
	if !errors.Is(err, errNoSite) {
		t.Fatalf("err = %v, want errNoSite", err)
	}
	if creds, _ := loadCredentials(); creds.CloudID != "" {
		t.Errorf("CloudID = %q, want nothing stored on failure", creds.CloudID)
	}
}
