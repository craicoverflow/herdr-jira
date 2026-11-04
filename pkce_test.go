package main

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewPKCEChallengeMatchesVerifier(t *testing.T) {
	pair, err := newPKCE()
	if err != nil {
		t.Fatalf("newPKCE: %v", err)
	}
	if pair.Verifier == "" || pair.Challenge == "" {
		t.Fatalf("expected non-empty verifier and challenge, got %+v", pair)
	}
	sum := sha256.Sum256([]byte(pair.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if pair.Challenge != want {
		t.Errorf("challenge = %q, want S256(verifier) = %q", pair.Challenge, want)
	}
	if strings.ContainsAny(pair.Verifier, "+/=") {
		t.Errorf("verifier %q contains non-URL-safe base64 characters", pair.Verifier)
	}
	if strings.ContainsAny(pair.Challenge, "+/=") {
		t.Errorf("challenge %q contains non-URL-safe base64 characters", pair.Challenge)
	}
}

func TestNewPKCEIsRandom(t *testing.T) {
	a, err := newPKCE()
	if err != nil {
		t.Fatalf("newPKCE: %v", err)
	}
	b, err := newPKCE()
	if err != nil {
		t.Fatalf("newPKCE: %v", err)
	}
	if a.Verifier == b.Verifier {
		t.Error("two calls to newPKCE produced the same verifier")
	}
}

func TestNewStateIsNonEmptyAndRandom(t *testing.T) {
	a, err := newState()
	if err != nil {
		t.Fatalf("newState: %v", err)
	}
	b, err := newState()
	if err != nil {
		t.Fatalf("newState: %v", err)
	}
	if a == "" {
		t.Fatal("expected non-empty state")
	}
	if a == b {
		t.Error("two calls to newState produced the same value")
	}
}
