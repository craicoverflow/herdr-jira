package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCandidateKeys(t *testing.T) {
	cases := []struct {
		name   string
		branch string
		want   []string
	}{
		{"bare key", "PROJ-1339", []string{"PROJ-1339"}},
		{"key with suffix", "PROJ-1339-fixes", []string{"PROJ-1339"}},
		{"key after path segment", "alice/PROJ-2416-fix-login-bug", []string{"PROJ-2416"}},
		{"key at the end", "promote/deploy_template-v0-PROJ-2460", []string{"PROJ-2460"}},
		{"two keys keep branch order", "PROJ-1-and-OTHER-2", []string{"PROJ-1", "OTHER-2"}},
		{"duplicate key once", "PROJ-1-then-PROJ-1", []string{"PROJ-1"}},
		{"lower case is upper-cased", "proj-77-thing", []string{"PROJ-77"}},
		// Noise the generic pattern also matches; Jira drops these.
		{"pr number", "pr-4567", []string{"PR-4567"}},
		{"step counter", "step-2-of-9", []string{"STEP-2", "OF-9"}},
		{"no match", "main", nil},
		{"no digits", "set-timeout-to-60s", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := candidateKeys(c.branch)
			if len(got) != len(c.want) {
				t.Fatalf("candidateKeys(%q) = %v, want %v", c.branch, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("candidateKeys(%q) = %v, want %v", c.branch, got, c.want)
				}
			}
		})
	}
}

func TestExtractKeyCustomPattern(t *testing.T) {
	pattern := `(?i)\b(ACME-[0-9]+)\b`
	key, ok := extractKey("feature/acme-42-widget", pattern)
	if !ok || key != "ACME-42" {
		t.Errorf("extractKey with custom pattern = (%q, %v), want (ACME-42, true)", key, ok)
	}
	// Other projects don't match once the pattern is set.
	if _, ok := extractKey("PROJ-1339", pattern); ok {
		t.Error("extractKey: PROJ branch matched an ACME-only pattern")
	}
	// Multiple keys: first match wins.
	if key, _ := extractKey("ACME-1-and-ACME-2", pattern); key != "ACME-1" {
		t.Errorf("key = %q, want ACME-1", key)
	}
}

func TestExtractKeyInvalidPatternReturnsNoKey(t *testing.T) {
	if _, ok := extractKey("PROJ-1339", `(unclosed`); ok {
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
	dir := gitRepo(t, "PROJ-9999-test")
	branch, ok := currentBranch(dir)
	if !ok {
		t.Fatal("currentBranch: expected ok=true in a real repo")
	}
	if branch != "PROJ-9999-test" {
		t.Errorf("branch = %q, want PROJ-9999-test", branch)
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

func TestResolveTicketKeysAutoReturnsCandidates(t *testing.T) {
	dir := gitRepo(t, "PROJ-4242-widgets")
	keys, userPattern := resolveTicketKeys(dir, withConfigDefaults(config{}))
	if userPattern || len(keys) != 1 || keys[0] != "PROJ-4242" {
		t.Errorf("resolveTicketKeys = (%v, %v), want ([PROJ-4242], false)", keys, userPattern)
	}
}

func TestResolveTicketKeysUserPatternIsUsedAsGiven(t *testing.T) {
	dir := gitRepo(t, "acme-7-and-PROJ-4242")
	keys, userPattern := resolveTicketKeys(dir, config{KeyPattern: `(?i)\b(ACME-[0-9]+)\b`})
	if !userPattern || len(keys) != 1 || keys[0] != "ACME-7" {
		t.Errorf("resolveTicketKeys = (%v, %v), want ([ACME-7], true)", keys, userPattern)
	}
	keys, userPattern = resolveTicketKeys(dir, config{KeyPattern: `(?i)\b(NOPE-[0-9]+)\b`})
	if !userPattern || len(keys) != 0 {
		t.Errorf("resolveTicketKeys = (%v, %v), want (none, true)", keys, userPattern)
	}
}

func TestResolveTicketKeysNoBranch(t *testing.T) {
	keys, _ := resolveTicketKeys(t.TempDir(), withConfigDefaults(config{}))
	if len(keys) != 0 {
		t.Errorf("keys = %v, want none outside a git repo", keys)
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
	if cfg.KeyPattern != "" {
		t.Errorf("KeyPattern = %q, want empty (auto)", cfg.KeyPattern)
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
	name, err := createBranch(dir, "PROJ-2815", "Start Spec")
	if err != nil {
		t.Fatalf("createBranch: %v", err)
	}
	if name != "PROJ-2815-start-spec" {
		t.Errorf("name = %q, want PROJ-2815-start-spec", name)
	}
	branch, ok := currentBranch(dir)
	if !ok || branch != name {
		t.Errorf("currentBranch = (%q, %v), want (%q, true) — checkout -b should switch to it", branch, ok, name)
	}
}

func TestCreateBranchEmptyCwd(t *testing.T) {
	if _, err := createBranch("", "PROJ-1", "suffix"); err == nil {
		t.Error("createBranch with no cwd: expected an error")
	}
}

func TestCreateBranchEmptySuffix(t *testing.T) {
	dir := gitRepo(t, "main")
	if _, err := createBranch(dir, "PROJ-1", "   "); err == nil {
		t.Error("createBranch with a blank suffix: expected an error")
	}
}

func TestCreateBranchAlreadyExists(t *testing.T) {
	dir := gitRepo(t, "main")
	if _, err := createBranch(dir, "PROJ-1", "dup"); err != nil {
		t.Fatalf("first createBranch: %v", err)
	}
	if err := exec.Command("git", "-C", dir, "checkout", "-q", "main").Run(); err != nil {
		t.Fatalf("checkout back to main: %v", err)
	}
	if _, err := createBranch(dir, "PROJ-1", "dup"); err == nil {
		t.Error("createBranch: expected an error creating a branch that already exists")
	}
}
