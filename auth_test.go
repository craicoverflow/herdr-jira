package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUARoundTripperSetsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	client := newHTTPClient()
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if got != userAgent {
		t.Errorf("User-Agent = %q, want %q", got, userAgent)
	}
}

func TestRegisterClientReturnsClientID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body["token_endpoint_auth_method"] != "none" {
			t.Errorf("token_endpoint_auth_method = %v, want %q", body["token_endpoint_auth_method"], "none")
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"client_id": "cid-123"})
	}))
	defer srv.Close()

	id, err := registerClientAt(context.Background(), srv.Client(), srv.URL, "http://127.0.0.1:1/callback")
	if err != nil {
		t.Fatalf("registerClient: %v", err)
	}
	if id != "cid-123" {
		t.Errorf("client id = %q, want %q", id, "cid-123")
	}
}

func TestRegisterClientErrorsOnNon201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("error code: 1010"))
	}))
	defer srv.Close()

	_, err := registerClientAt(context.Background(), srv.Client(), srv.URL, "http://127.0.0.1:1/callback")
	if err == nil {
		t.Fatal("registerClient: expected an error for a 403 response, got nil")
	}
	if !strings.Contains(err.Error(), "1010") {
		t.Errorf("error = %v, want it to include the server's response body", err)
	}
}

func TestExchangeCodePostsExpectedForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		want := map[string]string{
			"grant_type":    "authorization_code",
			"code":          "auth-code",
			"redirect_uri":  "http://127.0.0.1:1/callback",
			"client_id":     "cid-123",
			"code_verifier": "verifier-abc",
		}
		for k, v := range want {
			if got := r.PostForm.Get(k); got != v {
				t.Errorf("form[%s] = %q, want %q", k, got, v)
			}
		}
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "access-1",
			RefreshToken: "refresh-1",
			TokenType:    "bearer",
			ExpiresIn:    3600,
			Scope:        oauthScope,
		})
	}))
	defer srv.Close()

	tok, err := exchangeCodeAt(context.Background(), srv.Client(), srv.URL, "cid-123", "http://127.0.0.1:1/callback", "auth-code", "verifier-abc")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if tok.AccessToken != "access-1" || tok.RefreshToken != "refresh-1" {
		t.Errorf("tok = %+v, want AccessToken=access-1 RefreshToken=refresh-1", tok)
	}
}

func TestRefreshAccessTokenPostsExpectedForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("grant_type"); got != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", got)
		}
		if got := r.PostForm.Get("refresh_token"); got != "old-refresh" {
			t.Errorf("refresh_token = %q, want old-refresh", got)
		}
		// Confirms the rotation behaviour found in the Task 0a spike: the
		// server returns a *new* refresh token that callers must persist.
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  "new-access",
			RefreshToken: "rotated-refresh",
			TokenType:    "bearer",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	tok, err := refreshAccessTokenAt(context.Background(), srv.Client(), srv.URL, "cid-123", "old-refresh")
	if err != nil {
		t.Fatalf("refreshAccessToken: %v", err)
	}
	if tok.RefreshToken != "rotated-refresh" {
		t.Errorf("RefreshToken = %q, want rotated-refresh (rotation must be persisted)", tok.RefreshToken)
	}
}
