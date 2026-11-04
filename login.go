package main

import (
	"context"
	"fmt"
	"time"
)

// signInTimeout bounds how long signIn waits for the user to finish the
// browser consent flow.
const signInTimeout = 3 * time.Minute

// signIn runs the full OAuth DCR + PKCE browser flow and stores the
// resulting credentials. It registers a fresh client for this sign-in's
// loopback port (the Task 0a spike found registration is cheap/synchronous
// and exact-matches whatever redirect URI is requested, so there is no need
// to pre-register or reuse a fixed port across logins).
func signIn(ctx context.Context) error {
	ln, err := listenLoopback()
	if err != nil {
		return fmt.Errorf("sign in: open loopback listener: %w", err)
	}
	defer ln.Close()
	redirectURI := redirectURIFor(ln)

	client := newHTTPClient()

	clientID, err := registerClient(ctx, client, redirectURI)
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}

	pair, err := newPKCE()
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}
	state, err := newState()
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}

	authURL := buildAuthorizeURL(clientID, redirectURI, state, pair.Challenge)
	fmt.Println("Opening your browser to sign in to Jira…")
	fmt.Println(authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Println("Couldn't open a browser automatically; open the URL above by hand.")
	}

	waitCtx, cancel := context.WithTimeout(ctx, signInTimeout)
	defer cancel()
	fmt.Println("Waiting for you to approve in the browser…")
	result, err := waitForCallback(waitCtx, ln, state)
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}

	tok, err := exchangeCode(ctx, client, clientID, redirectURI, result.Code, pair.Verifier)
	if err != nil {
		return fmt.Errorf("sign in: %w", err)
	}

	if err := saveCredentials(tokenResponseToCredentials(clientID, tok)); err != nil {
		return fmt.Errorf("sign in: store credentials: %w", err)
	}
	// Resolve the site now, so a no-site or multi-site account sees the
	// reason at sign-in rather than on the first list load.
	if _, err := ensureCloudID(ctx, client); err != nil {
		return fmt.Errorf("sign in: %w", err)
	}
	fmt.Println("Signed in.")
	return nil
}
