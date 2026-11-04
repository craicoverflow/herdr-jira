package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestWaitForCallbackCapturesCode(t *testing.T) {
	ln, err := listenLoopback()
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resCh := make(chan callbackResult, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := waitForCallback(ctx, ln, "expected-state")
		resCh <- res
		errCh <- err
	}()

	url := fmt.Sprintf("http://%s/callback?code=abc123&state=expected-state", ln.Addr().String())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	resp.Body.Close()

	res := <-resCh
	if err := <-errCh; err != nil {
		t.Fatalf("waitForCallback: %v", err)
	}
	if res.Code != "abc123" {
		t.Errorf("Code = %q, want %q", res.Code, "abc123")
	}
	if res.State != "expected-state" {
		t.Errorf("State = %q, want %q", res.State, "expected-state")
	}
}

func TestWaitForCallbackRejectsStateMismatch(t *testing.T) {
	ln, err := listenLoopback()
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := waitForCallback(ctx, ln, "expected-state")
		errCh <- err
	}()

	url := fmt.Sprintf("http://%s/callback?code=abc123&state=wrong-state", ln.Addr().String())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	resp.Body.Close()

	if err := <-errCh; err == nil {
		t.Fatal("waitForCallback: expected an error for mismatched state, got nil")
	}
}

func TestWaitForCallbackCapturesProviderError(t *testing.T) {
	ln, err := listenLoopback()
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := waitForCallback(ctx, ln, "expected-state")
		errCh <- err
	}()

	url := fmt.Sprintf("http://%s/callback?error=access_denied&state=expected-state", ln.Addr().String())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	resp.Body.Close()

	err = <-errCh
	if !errors.Is(err, errConsentBlocked) {
		t.Fatalf("waitForCallback err = %v, want errConsentBlocked for access_denied", err)
	}
}

func TestWaitForCallbackContextTimeout(t *testing.T) {
	ln, err := listenLoopback()
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err = waitForCallback(ctx, ln, "expected-state")
	if err == nil {
		t.Fatal("waitForCallback: expected a timeout error, got nil")
	}
}

func TestRedirectURIForUsesLoopbackAddress(t *testing.T) {
	ln, err := listenLoopback()
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer ln.Close()

	uri := redirectURIFor(ln)
	want := fmt.Sprintf("http://%s/callback", ln.Addr().String())
	if uri != want {
		t.Errorf("redirectURIFor = %q, want %q", uri, want)
	}
}
