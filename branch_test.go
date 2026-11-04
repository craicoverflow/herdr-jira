package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestExtractKeyDefaultPattern(t *testing.T) {
	cases := []struct {
		branch  string
		wantKey string
		wantOK  bool
	}{
		// Real patterns sampled in the Task 0b branch audit.
		{"TICKET-1339", "TICKET-1339", true},
		{"TICKET-1339-fixes", "TICKET-1339", true},
		{"TICKET-1339-fixes2", "TICKET-1339", true},
		{"alice/TICKET-2416-fix-login-bug", "TICKET-2416", true},
		{"promote/deploy_template-v0-TICKET-2460", "TICKET-2460", true},
		{"TICKET-553", "TICKET-553", true},
		{"TICKET-2481-ha-lab-dev-canary", "TICKET-2481", true},
		// No key.
		{"fix-tc-template-2", "", false},
		{"set-timeout-to-60s", "", false},
		{"main", "", false},
		{"register-k8lab-2", "", false},
		// False positives a bare generic regex would catch, that the
		// TICKET-scoped default must not.
		{"pr-28069", "", false},
		{"main-1743", "", false},
		{"template-2", "", false},
		// Multiple keys: first match wins.
		{"TICKET-1-and-TICKET-2", "TICKET-1", true},
	}
	for _, c := range cases {
		t.Run(c.branch, func(t *testing.T) {
			key, ok := extractKey(c.branch, defaultKeyPattern)
			if ok != c.wantOK || key != c.wantKey {
				t.Errorf("extractKey(%q) = (%q, %v), want (%q, %v)", c.branch, key, ok, c.wantKey, c.wantOK)
			}
		})
	}
}

func TestExtractKeyCustomPattern(t *testing.T) {
	// A config override for a different/second project prefix.
	pattern := `(?i)\b(PROJ-[0-9]+)\b`
	key, ok := extractKey("feature/PROJ-42-widget", pattern)
	if !ok || key != "PROJ-42" {
		t.Errorf("extractKey with custom pattern = (%q, %v), want (PROJ-42, true)", key, ok)
	}
	// TICKET no longer matches once the pattern is overridden.
	if _, ok := extractKey("TICKET-1339", pattern); ok {
		t.Error("extractKey: TICKET branch matched a PROJ-only pattern")
	}
}

func TestExtractKeyInvalidPatternReturnsNoKey(t *testing.T) {
	if _, ok := extractKey("TICKET-1339", `(unclosed`); ok {
		t.Error("extractKey: expected no match for an invalid regex, got a match")
	}
}

// gitRepo creates a throwaway repo at a branch and returns its path.
func gitRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", branch)
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	run("add", "f.txt")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestCurrentBranchInRealRepo(t *testing.T) {
	dir := gitRepo(t, "TICKET-9999-test")
	branch, ok := currentBranch(dir)
	if !ok {
		t.Fatal("currentBranch: expected ok=true in a real repo")
	}
	if branch != "TICKET-9999-test" {
		t.Errorf("branch = %q, want TICKET-9999-test", branch)
	}
}

func TestCurrentBranchDetachedHEAD(t *testing.T) {
	dir := gitRepo(t, "main")
	sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "checkout", "-q", string(sha[:len(sha)-1])).CombinedOutput(); err != nil {
		t.Fatalf("checkout detached: %v\n%s", err, out)
	}
	if _, ok := currentBranch(dir); ok {
		t.Error("currentBranch: expected ok=false on a detached HEAD")
	}
}

func TestCurrentBranchNonGitDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, ok := currentBranch(dir); ok {
		t.Error("currentBranch: expected ok=false outside a git repo")
	}
}

func TestResolveTicketKeyEndToEnd(t *testing.T) {
	dir := gitRepo(t, "TICKET-4242-widgets")
	cfg := withConfigDefaults(config{})
	key, ok := resolveTicketKey(dir, cfg)
	if !ok || key != "TICKET-4242" {
		t.Errorf("resolveTicketKey = (%q, %v), want (TICKET-4242, true)", key, ok)
	}
}

func TestLoadConfigKeyPatternOverride(t *testing.T) {
	dir := withTempConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"key_pattern": "(?i)\\b(PROJ-[0-9]+)\\b"}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.KeyPattern != `(?i)\b(PROJ-[0-9]+)\b` {
		t.Errorf("KeyPattern = %q, want override from config.json", cfg.KeyPattern)
	}
}

func TestLoadConfigDefaultsWhenFileMissing(t *testing.T) {
	withTempConfigDir(t)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.KeyPattern != defaultKeyPattern {
		t.Errorf("KeyPattern = %q, want default %q", cfg.KeyPattern, defaultKeyPattern)
	}
}

func TestSlugifyBranchSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"start-spec", "start-spec"},
		{"Start Spec", "start-spec"},
		{"  fix the thing!  ", "fix-the-thing"},
		{"a_b/c", "a-b-c"},
		{"---", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := slugifyBranchSuffix(c.in); got != c.want {
			t.Errorf("slugifyBranchSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCreateBranchInRealRepo(t *testing.T) {
	dir := gitRepo(t, "main")
	name, err := createBranch(dir, "TICKET-2815", "Start Spec")
	if err != nil {
		t.Fatalf("createBranch: %v", err)
	}
	if name != "TICKET-2815-start-spec" {
		t.Errorf("name = %q, want TICKET-2815-start-spec", name)
	}
	branch, ok := currentBranch(dir)
	if !ok || branch != name {
		t.Errorf("currentBranch = (%q, %v), want (%q, true) — checkout -b should switch to it", branch, ok, name)
	}
}

func TestCreateBranchEmptyCwd(t *testing.T) {
	if _, err := createBranch("", "TICKET-1", "suffix"); err == nil {
		t.Error("createBranch with no cwd: expected an error")
	}
}

func TestCreateBranchEmptySuffix(t *testing.T) {
	dir := gitRepo(t, "main")
	if _, err := createBranch(dir, "TICKET-1", "   "); err == nil {
		t.Error("createBranch with a blank suffix: expected an error")
	}
}

func TestCreateBranchAlreadyExists(t *testing.T) {
	dir := gitRepo(t, "main")
	if _, err := createBranch(dir, "TICKET-1", "dup"); err != nil {
		t.Fatalf("first createBranch: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "checkout", "-q", "main").Run(); err != nil {
		t.Fatalf("checkout back to main: %v", err)
	}
	if _, err := createBranch(dir, "TICKET-1", "dup"); err == nil {
		t.Error("createBranch: expected an error creating a branch that already exists")
	}
}
