package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// pkcePair is a PKCE code verifier and its S256 challenge, per RFC 7636.
type pkcePair struct {
	Verifier  string
	Challenge string
}

// newPKCE generates a fresh verifier/challenge pair.
func newPKCE() (pkcePair, error) {
	raw := make([]byte, 40)
	if _, err := rand.Read(raw); err != nil {
		return pkcePair{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return pkcePair{Verifier: verifier, Challenge: challenge}, nil
}

// newState returns a fresh random OAuth state value.
func newState() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
