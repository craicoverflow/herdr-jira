package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// credentials is everything needed to call the MCP endpoint as a signed-in
// user, plus the DCR client registration (so sign-in never re-registers).
type credentials struct {
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope"`
	// CloudID and SiteURL cache getAccessibleAtlassianResources's answer,
	// so every call doesn't need to re-resolve it.
	CloudID string `json:"cloud_id,omitempty"`
	SiteURL string `json:"site_url,omitempty"`
	// AccountID caches atlassianUserInfo's answer, so "assign to me" doesn't
	// need to look it up fresh on every assign.
	AccountID string `json:"account_id,omitempty"`
}

func credentialsPath() string {
	return filepath.Join(configDir(), "credentials.json")
}

func lockPath() string {
	return filepath.Join(configDir(), "credentials.lock")
}

// errNoCredentials means the credentials file doesn't exist yet: not signed in.
var errNoCredentials = errors.New("not signed in")

// loadCredentials reads the stored credentials without locking. Use
// withCredentialsLock for a read-modify-write.
func loadCredentials() (credentials, error) {
	data, err := os.ReadFile(credentialsPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return credentials{}, errNoCredentials
		}
		return credentials{}, err
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return credentials{}, err
	}
	return creds, nil
}

// saveCredentials writes creds atomically (temp file + rename) with mode
// 0600. The directory is created if needed.
func saveCredentials(creds credentials) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, credentialsPath())
}

// withCredentialsLock runs fn holding an exclusive cross-process file lock,
// so two processes (e.g. two concurrent `tick`s) refreshing the same rotating
// refresh token can't race and lose the winner's new token. fn receives the
// freshly loaded credentials (or the zero value + errNoCredentials) and
// returns the value to save; if fn returns an error, nothing is saved.
func withCredentialsLock(fn func(credentials, error) (credentials, error)) error {
	dir := configDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	creds, loadErr := loadCredentials()
	next, err := fn(creds, loadErr)
	if err != nil {
		return err
	}
	return saveCredentials(next)
}
