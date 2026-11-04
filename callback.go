package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// callbackResult is what the loopback redirect handler captured from the
// browser.
type callbackResult struct {
	Code  string
	State string
	Err   error // set if the provider redirected back with an OAuth error
}

// listenLoopback opens an ephemeral-port TCP listener on 127.0.0.1. The
// Task 0a spike found DCR registers whatever redirect URI is requested
// (including an arbitrary port), so the plugin picks a fresh port per
// sign-in and registers a client for it, rather than needing a fixed port.
func listenLoopback() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func redirectURIFor(ln net.Listener) string {
	return fmt.Sprintf("http://%s/callback", ln.Addr().String())
}

// waitForCallback serves exactly one request on ln (the OAuth redirect) and
// returns what it captured. It stops serving as soon as that request lands,
// or when ctx is done.
func waitForCallback(ctx context.Context, ln net.Listener, expectedState string) (callbackResult, error) {
	resultCh := make(chan callbackResult, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			res := callbackResult{Code: q.Get("code"), State: q.Get("state")}
			if errStr := q.Get("error"); errStr == "access_denied" {
				res.Err = errConsentBlocked
			} else if errStr != "" {
				res.Err = fmt.Errorf("authorization server returned error: %s", errStr)
			} else if res.State != expectedState {
				res.Err = fmt.Errorf("state mismatch: got %q", res.State)
			} else if res.Code == "" {
				res.Err = fmt.Errorf("no code in callback")
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if res.Err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintln(w, "Sign-in failed, check herdr-jira. You can close this tab.")
			} else {
				fmt.Fprintln(w, "Signed in. You can close this tab.")
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			select {
			case resultCh <- res:
			default:
			}
		}),
	}
	go srv.Serve(ln)
	// Close asynchronously, after giving the handler's response time to
	// reach the browser: an immediate srv.Close() can cut the TCP
	// connection before the client finishes reading the response body.
	defer time.AfterFunc(200*time.Millisecond, func() { srv.Close() })

	select {
	case res := <-resultCh:
		return res, res.Err
	case <-ctx.Done():
		return callbackResult{}, ctx.Err()
	}
}
