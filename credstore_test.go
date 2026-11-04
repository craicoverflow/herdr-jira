package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func parseCounter(s string) (int, error) { return strconv.Atoi(s) }
func formatCounter(n int) string         { return strconv.Itoa(n) }

func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HERDR_PLUGIN_CONFIG_DIR", dir)
	return dir
}

func TestLoadCredentialsMissingFileReturnsErrNoCredentials(t *testing.T) {
	withTempConfigDir(t)
	_, err := loadCredentials()
	if !errors.Is(err, errNoCredentials) {
		t.Fatalf("loadCredentials on missing file: got %v, want errNoCredentials", err)
	}
}

func TestSaveLoadCredentialsRoundTrip(t *testing.T) {
	withTempConfigDir(t)
	want := credentials{
		ClientID:     "client-123",
		AccessToken:  "access-abc",
		RefreshToken: "refresh-xyz",
		ExpiresAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Scope:        "offline_access read:jira-work",
	}
	if err := saveCredentials(want); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	got, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestSaveCredentialsFileModeIs0600(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{AccessToken: "x"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	info, err := os.Stat(credentialsPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file mode = %o, want 0600", perm)
	}
}

func TestSaveCredentialsIsAtomic(t *testing.T) {
	dir := withTempConfigDir(t)
	if err := saveCredentials(credentials{AccessToken: "first"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	if err := saveCredentials(credentials{AccessToken: "second"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}
	// No leftover temp files after a successful rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
	got, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	if got.AccessToken != "second" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "second")
	}
}

// TestWithCredentialsLockSerializesConcurrentRefreshes simulates several
// processes refreshing a rotating token at once (herdr fires tick from many
// events). Each holder increments a counter it reads under the lock; if the
// lock didn't serialize them, increments would be lost.
func TestWithCredentialsLockSerializesConcurrentRefreshes(t *testing.T) {
	withTempConfigDir(t)
	if err := saveCredentials(credentials{AccessToken: "0"}); err != nil {
		t.Fatalf("saveCredentials: %v", err)
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := withCredentialsLock(func(creds credentials, loadErr error) (credentials, error) {
				if loadErr != nil {
					return credentials{}, loadErr
				}
				n, err := parseCounter(creds.AccessToken)
				if err != nil {
					return credentials{}, err
				}
				creds.AccessToken = formatCounter(n + 1)
				return creds, nil
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("withCredentialsLock: %v", err)
		}
	}

	got, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials: %v", err)
	}
	want := formatCounter(n)
	if got.AccessToken != want {
		t.Errorf("AccessToken = %q, want %q (lost updates: lock did not serialize)", got.AccessToken, want)
	}
}
