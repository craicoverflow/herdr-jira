package main

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// extractKey finds a Jira key in branch using pattern, matching anywhere in
// the name (keys appear as a bare prefix, with a trailing suffix, or
// nested after a path segment — confirmed in the Task 0b branch audit) and
// case-insensitively, uppercasing the result since real branches use both
// (`TICKET-1339`, `TICKET-553`).
func extractKey(branch, pattern string) (string, bool) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}
	var match string
	if n := re.SubexpNames(); len(n) > 1 {
		m := re.FindStringSubmatch(branch)
		if m == nil {
			return "", false
		}
		match = m[1]
	} else {
		match = re.FindString(branch)
		if match == "" {
			return "", false
		}
	}
	return strings.ToUpper(match), true
}

// currentBranch returns the checked-out branch name for the git worktree
// containing cwd. It returns ("", false) for a detached HEAD, a non-git
// directory, or any other git failure — never an error, since the caller
// (tick, running on every herdr event) should stay quiet rather than fail.
func currentBranch(cwd string) (string, bool) {
	out, err := exec.Command("git", "-C", cwd, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", false
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", false
	}
	return branch, true
}

// resolveTicketKey is the full branch->key resolution for cwd using cfg's
// key pattern.
func resolveTicketKey(cwd string, cfg config) (string, bool) {
	branch, ok := currentBranch(cwd)
	if !ok {
		return "", false
	}
	return extractKey(branch, cfg.KeyPattern)
}

// branchSuffixUnsafeRE matches runs of characters a git branch name
// shouldn't carry (spaces, punctuation, anything non-ASCII-alnum), so a
// suffix typed as free text becomes a single run of hyphens instead.
var branchSuffixUnsafeRE = regexp.MustCompile(`[^a-z0-9]+`)

// slugifyBranchSuffix lowercases suffix and collapses anything that isn't
// [a-z0-9] into a single hyphen, trimming leading/trailing hyphens.
func slugifyBranchSuffix(suffix string) string {
	s := strings.ToLower(strings.TrimSpace(suffix))
	s = branchSuffixUnsafeRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// createBranchFn is a seam over the actual `git checkout -b`, so TUI tests
// can exercise the branch-creation flow without shelling out.
var createBranchFn = runCreateBranch

// runCreateBranch creates and switches to a new branch named name in cwd.
func runCreateBranch(cwd, name string) error {
	out, err := exec.Command("git", "-C", cwd, "checkout", "-b", name).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git checkout -b %s: %s", name, msg)
	}
	return nil
}

// createBranch builds "<key>-<slug(suffix)>" and creates/switches to it in
// cwd, validating the name with git itself (git check-ref-format) rather
// than hand-rolling the ref-name rules.
func createBranch(cwd, key, suffix string) (string, error) {
	if cwd == "" {
		return "", fmt.Errorf("no workspace directory known for this popup")
	}
	slug := slugifyBranchSuffix(suffix)
	if slug == "" {
		return "", fmt.Errorf("branch suffix %q has no usable characters", suffix)
	}
	name := key + "-" + slug
	if out, err := exec.Command("git", "check-ref-format", "--branch", name).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("invalid branch name %q: %s", name, msg)
	}
	if err := createBranchFn(cwd, name); err != nil {
		return "", err
	}
	return name, nil
}
