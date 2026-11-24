package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/install-app.sh, run against a pretend release in a local folder.

// fakeRelease writes bin as this platform's release binary, and a
// checksums.txt naming sum (the binary's own when sum is empty).
func fakeRelease(t *testing.T, bin []byte, sum string) string {
	t.Helper()
	dir := t.TempDir()
	name := fmt.Sprintf("herdr-jira-%s-%s", runtime.GOOS, runtime.GOARCH)
	if err := os.WriteFile(filepath.Join(dir, name), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if sum == "" {
		sum = fmt.Sprintf("%x", sha256.Sum256(bin))
	}
	checksums := fmt.Sprintf("%s  %s\n%s  herdr-jira-plan9-mips\n", sum, name, strings.Repeat("0", 64))
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runInstallScript runs the script with the Herdr app folder at home and
// checks it leaves no temporary files behind.
func runInstallScript(t *testing.T, release, home string, args ...string) (string, error) {
	t.Helper()
	tmp := t.TempDir()
	cmd := exec.Command("sh", append([]string{"scripts/install-app.sh"}, args...)...)
	cmd.Env = append(os.Environ(),
		"HERDR_JIRA_RELEASE_BASE=file://"+release,
		"HERDR_APP_HOME="+home,
		"TMPDIR="+tmp,
	)
	out, err := cmd.CombinedOutput()
	if left, _ := os.ReadDir(tmp); len(left) > 0 {
		t.Errorf("the script left %s in TMPDIR", left[0].Name())
	}
	return string(out), err
}

func TestInstallAppScriptIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "herdr-jira")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	release := fakeRelease(t, data, "")
	home := t.TempDir()
	keysPath := filepath.Join(home, "keys.json")
	os.WriteFile(keysPath, []byte(sampleKeys), 0o600)

	out, err := runInstallScript(t, release, home)
	if err != nil {
		t.Fatalf("first run: %v\n%s", err, out)
	}
	helper := filepath.Join(home, "mods", "jira", ".bin", "herdr-jira")
	if got, _ := os.ReadFile(helper); string(got) != string(data) {
		t.Errorf("the helper should be the release binary")
	}
	if _, err := os.Stat(filepath.Join(home, "mods", "jira", "main.js")); err != nil {
		t.Errorf("the mod wasn't installed: %v", err)
	}
	keys, _ := os.ReadFile(keysPath)
	if strings.Count(string(keys), modAction) != 1 {
		t.Fatalf("want one binding, got:\n%s", keys)
	}

	out, err = runInstallScript(t, release, home)
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if again, _ := os.ReadFile(keysPath); string(again) != string(keys) {
		t.Errorf("a second run changed keys.json:\n%s", again)
	}
	if !strings.Contains(out, "already opens the mod") {
		t.Errorf("a second run should leave the binding be:\n%s", out)
	}

	// Arguments reach `app install`.
	out, err = runInstallScript(t, release, home, "--demo")
	if err != nil {
		t.Fatalf("--demo: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(helper), demoMarkerName)); err != nil {
		t.Errorf("--demo wasn't passed on: %v\n%s", err, out)
	}
}

func TestInstallAppScriptRefuses(t *testing.T) {
	// Stands in for the binary; it records being asked to install.
	fake := func(usage string) []byte {
		return []byte("#!/bin/sh\nif [ $# -eq 0 ]; then printf '%s\\n' '" + usage + "'; exit 0; fi\n" +
			"mkdir -p \"$HERDR_APP_HOME/mods\"\n")
	}
	for _, tc := range []struct {
		name, usage, sum, want string
	}{
		{"checksum mismatch", "app install", strings.Repeat("a", 64), "checksum mismatch"},
		{"a release without the app mod", "login status tick", "", "predates the Herdr app mod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			out, err := runInstallScript(t, fakeRelease(t, fake(tc.usage), tc.sum), home)
			if err == nil || !strings.Contains(out, tc.want) {
				t.Fatalf("want a failure naming %q, got %v:\n%s", tc.want, err, out)
			}
			if _, err := os.Stat(filepath.Join(home, "mods")); !os.IsNotExist(err) {
				t.Errorf("it installed anyway")
			}
		})
	}

	t.Run("a binary missing from the release", func(t *testing.T) {
		release := fakeRelease(t, fake("app install"), "")
		os.Remove(filepath.Join(release, fmt.Sprintf("herdr-jira-%s-%s", runtime.GOOS, runtime.GOARCH)))
		out, err := runInstallScript(t, release, t.TempDir())
		if err == nil || !strings.Contains(out, "couldn't download") {
			t.Fatalf("got %v:\n%s", err, out)
		}
	})
}
