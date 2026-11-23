package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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
	// Earlier installs named the marker "demo"; .bin is this mod's own
	// folder, so that file can only be ours.
	if err := os.Remove(filepath.Join(filepath.Dir(helper), "demo")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	marker := filepath.Join(filepath.Dir(helper), demoMarkerName)
	if demo {
		if err := os.WriteFile(marker, []byte("Remove this file, or reinstall without --demo, to use your Jira.\n"), 0o644); err != nil {
			return err
		}
	} else if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(out, "Installed the Jira mod in %s (%d files) and its helper at %s.\n", dest, len(written), helper)
	if len(removed) > 0 {
		fmt.Fprintf(out, "Removed files an earlier version installed: %s.\n", strings.Join(removed, ", "))
	}
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
// have, and returns their paths. Only the kinds of file the mod ships are
// candidates (scripts and mod.json at the top, icons/*.svg), so the helper,
// the files Herdr writes into a mod folder (jsconfig.json, *.d.ts) and
// anything else a person keeps there stay.
func pruneModFiles(dest string, keep map[string]bool) ([]string, error) {
	var removed []string
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
		if keep[rel] || !shippedKind(rel) {
			return nil
		}
		removed = append(removed, rel)
		return os.Remove(path)
	})
	return removed, err
}

// shippedKind reports whether rel (a path in the mod folder) is the kind of
// file the mod ships, matching the embed pattern above.
func shippedKind(rel string) bool {
	if dir, name := filepath.Split(filepath.FromSlash(rel)); dir == "" {
		return strings.HasSuffix(name, ".js") || name == "mod.json"
	}
	ok, _ := filepath.Match("icons/*.svg", rel)
	return ok
}

// installHelper copies this running binary to path, keeping its mode.
func installHelper(path string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	src, err := os.Open(exe)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", exe, err)
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return "", err
	}
	if err := writeFileFrom(path, src, st.Mode().Perm()); err != nil {
		return "", fmt.Errorf("install the helper: %w", err)
	}
	return path, nil
}

func writeFileMode(path string, data []byte, mode os.FileMode) error {
	return writeFileFrom(path, bytes.NewReader(data), mode)
}

// writeFileFrom replaces path atomically with what r holds.
func writeFileFrom(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".install-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
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

// rewriteInPlace replaces a person's file (keys.json) with data. A symlink
// (a dotfile manager's) is followed so the link stays a link, and the file
// keeps its permissions.
func rewriteInPlace(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return writeFileMode(path, data, mode)
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
	if err := rewriteInPlace(path, edited); err != nil {
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
	edited, ok := unbindMod(data)
	if !ok {
		fmt.Fprintf(out, "Left keys.json alone: remove the %q binding yourself.\n", modAction)
		return nil
	}
	if edited == nil {
		return nil
	}
	if err := rewriteInPlace(path, edited); err != nil {
		return err
	}
	fmt.Fprintln(out, "Removed its key binding from keys.json.")
	return nil
}

var (
	modBindingLineRE = regexp.MustCompile(`(?m)^[ \t]*"[^"]+"\s*:\s*"` + regexp.QuoteMeta(modAction) + `"\s*,?[ \t]*\r?\n`)
	// danglingCommaRE is the comma left before a closing brace when the
	// binding removed was the last in its section.
	danglingCommaRE = regexp.MustCompile(`,(\s*\})`)
)

// unbindMod removes the lines binding a key to the mod from keys.json's
// text. It returns nil when there are none, and ok false when it can't
// remove them leaving every other binding exactly as it was.
func unbindMod(data []byte) (edited []byte, ok bool) {
	edited = modBindingLineRE.ReplaceAll(data, nil)
	if len(edited) == len(data) {
		return nil, true
	}
	var want map[string]map[string]any
	if json.Unmarshal(data, &want) != nil {
		return nil, false
	}
	for _, binds := range want {
		for k, action := range binds {
			if a, isStr := action.(string); isStr && a == modAction {
				delete(binds, k)
			}
		}
	}
	same := func(b []byte) bool {
		var got map[string]map[string]any
		return json.Unmarshal(b, &got) == nil && reflect.DeepEqual(got, want)
	}
	if same(edited) {
		return edited, true
	}
	if fixed := danglingCommaRE.ReplaceAll(edited, []byte("$1")); same(fixed) {
		return fixed, true
	}
	return nil, false
}
