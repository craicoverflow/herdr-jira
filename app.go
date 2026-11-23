package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// app.go installs the Herdr app mod (app/mods/jira) into the app's folder:
//
//	herdr-jira app install [--home DIR] [--no-key]
//	herdr-jira app uninstall [--home DIR]
//
// The mod's files are built into this binary, so a downloaded release
// installs it with nothing else at hand. The binary copies itself into the
// mod's hidden .bin folder as the helper the mod runs, and binds a key to
// open it when that key is free.

// Only the mod's own files: `herdr-app check` writes jsconfig.json and the
// type files into a mod folder, and those stay out of the binary.
//
//go:embed app/mods/jira/*.js app/mods/jira/mod.json app/mods/jira/icons/*.svg
var embeddedMod embed.FS

const (
	modName      = "jira"
	modEmbedRoot = "app/mods/jira"
	modAction    = "mod.open:" + modName
)

// appHomeDir is the Herdr app's folder: $HERDR_APP_HOME, else ~/.config/herdr-app.
func appHomeDir() string {
	if h := os.Getenv("HERDR_APP_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "herdr-app")
}

// openKey is the key the installer binds to open the mod.
func openKey() string {
	if runtime.GOOS == "darwin" {
		return "cmd-j"
	}
	return "ctrl-shift-j"
}

func runApp(args []string) error {
	return runAppTo(args, os.Stdout)
}

func runAppTo(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: herdr-jira app install|uninstall [--home DIR] [--no-key]")
	}
	fset := flag.NewFlagSet("app "+args[0], flag.ContinueOnError)
	fset.SetOutput(io.Discard)
	home := fset.String("home", appHomeDir(), "the Herdr app's folder")
	noKey := fset.Bool("no-key", false, "don't add a key binding")
	from := fset.String("from", "", "install the mod from this folder instead of the built-in copy (development)")
	demo := fset.Bool("demo", false, "answer from a pretend Jira site, to try the mod without an account")
	if err := fset.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "install":
		return installMod(*home, *from, !*noKey, *demo, out)
	case "uninstall":
		return uninstallMod(*home, out)
	}
	return fmt.Errorf("unknown app command %q", args[0])
}

func installMod(home, from string, bindKey, demo bool, out io.Writer) error {
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return fmt.Errorf("no Herdr app folder at %s (run the Herdr app once, or pass --home)", home)
	}
	var src fs.FS
	if from != "" {
		src = os.DirFS(from)
	} else {
		sub, err := fs.Sub(embeddedMod, modEmbedRoot)
		if err != nil {
			return err
		}
		src = sub
	}
	dest := filepath.Join(home, "mods", modName)
	written, err := copyModFiles(src, dest)
	if err != nil {
		return err
	}
	removed, err := pruneModFiles(dest, written)
	if err != nil {
		return err
	}
	helper, err := installHelper(filepath.Join(dest, ".bin", "herdr-jira"))
	if err != nil {
		return err
	}
	marker := filepath.Join(filepath.Dir(helper), "demo")
	if demo {
		if err := os.WriteFile(marker, []byte("Remove this file, or reinstall without --demo, to use your Jira.\n"), 0o644); err != nil {
			return err
		}
	} else if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(out, "Installed the Jira mod in %s (%d files", dest, len(written))
	if removed > 0 {
		fmt.Fprintf(out, ", %d old ones removed", removed)
	}
	fmt.Fprintf(out, ") and its helper at %s.\n", helper)
	if demo {
		fmt.Fprintln(out, "Demo mode: it shows a pretend Jira site. Reinstall without --demo to use yours.")
	}

	if bindKey {
		msg, err := bindOpenKey(filepath.Join(home, "keys.json"), openKey())
		if err != nil {
			fmt.Fprintf(out, "Didn't add a key: %v. Bind one yourself in keys.json: \"%s\": \"%s\".\n", err, openKey(), modAction)
		} else {
			fmt.Fprintln(out, msg)
		}
	}
	fmt.Fprintf(out, "Open it with the key, or from a shell: herdr-app do mod.open --name %s\n", modName)
	return nil
}

// copyModFiles writes every file of src under dest, each replaced
// atomically, and returns the relative paths written.
func copyModFiles(src fs.FS, dest string) (map[string]bool, error) {
	written := map[string]bool{}
	err := fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") || d.Name() == "jsconfig.json" || strings.HasSuffix(d.Name(), ".d.ts") {
			return nil
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(path))
		if err := writeFileMode(target, data, 0o644); err != nil {
			return err
		}
		written[filepath.ToSlash(path)] = true
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("copy the mod: %w", err)
	}
	return written, nil
}

// pruneModFiles removes files a previous install left that this one doesn't
// have. Hidden files (the helper) and the files Herdr writes into a mod
// folder (jsconfig.json, *.d.ts) stay.
func pruneModFiles(dest string, keep map[string]bool) (int, error) {
	removed := 0
	err := filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && path != dest {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dest, path)
		rel = filepath.ToSlash(rel)
		if keep[rel] || rel == "jsconfig.json" || strings.HasSuffix(rel, ".d.ts") {
			return nil
		}
		removed++
		return os.Remove(path)
	})
	return removed, err
}

// installHelper copies this running binary to path.
func installHelper(path string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", exe, err)
	}
	if err := writeFileMode(path, data, 0o755); err != nil {
		return "", fmt.Errorf("install the helper: %w", err)
	}
	return path, nil
}

func writeFileMode(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".install-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var globalSectionRE = regexp.MustCompile(`"global"\s*:\s*\{`)

// bindOpenKey adds key -> mod.open:jira to keys.json's global section, unless
// something already opens the mod or the key is taken. It edits the text in
// place, so the person's file keeps its order and layout, and checks the
// result is still valid JSON before writing it.
func bindOpenKey(path, key string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("there's no keys.json yet (open Herdr once)")
	}
	if err != nil {
		return "", err
	}
	var keys map[string]map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		return "", fmt.Errorf("keys.json isn't valid JSON: %w", err)
	}
	var already []string
	for section, binds := range keys {
		for k, action := range binds {
			if a, ok := action.(string); ok && a == modAction {
				already = append(already, section+" "+k)
			}
		}
	}
	if len(already) > 0 {
		sort.Strings(already)
		return fmt.Sprintf("keys.json already opens the mod (%s).", strings.Join(already, ", ")), nil
	}
	if action, taken := keys["global"][key]; taken {
		return "", fmt.Errorf("%s is already bound to %v", key, action)
	}
	loc := globalSectionRE.FindIndex(data)
	if loc == nil {
		return "", errors.New("keys.json has no global section")
	}
	entry := fmt.Sprintf("\n    %q: %q", key, modAction)
	if len(keys["global"]) > 0 {
		entry += ","
	}
	edited := append(append(append([]byte{}, data[:loc[1]]...), entry...), data[loc[1]:]...)
	var check map[string]map[string]any
	if err := json.Unmarshal(edited, &check); err != nil || check["global"][key] != modAction {
		return "", errors.New("couldn't edit keys.json safely")
	}
	if err := writeFileMode(path, edited, 0o600); err != nil {
		return "", err
	}
	return fmt.Sprintf("Bound %s to open it (keys.json, global).", key), nil
}

func uninstallMod(home string, out io.Writer) error {
	dest := filepath.Join(home, "mods", modName)
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %s.\n", dest)
	path := filepath.Join(home, "keys.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lineRE := regexp.MustCompile(`(?m)^[ \t]*"[^"]+"\s*:\s*"` + regexp.QuoteMeta(modAction) + `"\s*,?[ \t]*\r?\n`)
	edited := lineRE.ReplaceAll(data, nil)
	if len(edited) == len(data) {
		return nil
	}
	var check map[string]any
	if json.Unmarshal(edited, &check) != nil {
		fmt.Fprintf(out, "Left keys.json alone: remove the %q binding yourself.\n", modAction)
		return nil
	}
	if err := writeFileMode(path, edited, 0o600); err != nil {
		return err
	}
	fmt.Fprintln(out, "Removed its key binding from keys.json.")
	return nil
}
