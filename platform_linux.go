package main

import (
	"fmt"
	"os/exec"
	"time"
)

// browserGrace is how long openBrowser waits for xdg-open to fail fast
// (e.g. "no browser configured"). Some desktops keep xdg-open running as
// long as the browser does, so a failure after this is not reported.
var browserGrace = 2 * time.Second

func openBrowser(u string) error {
	cmd := exec.Command("xdg-open", u)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("xdg-open: %w", err)
		}
		return nil
	case <-time.After(browserGrace):
		return nil
	}
}
