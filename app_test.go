package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleKeys = `{
  "global": {
    "cmd-q": "app.quit",
    "cmd-p": "mod.open:picker"
  },
  "terminal": {
    "cmd-c": "terminal.copy"
  }
}
`

func TestBindOpenKeyAddsKeepingTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	os.WriteFile(path, []byte(sampleKeys), 0o600)
	msg, err := bindOpenKey(path, "cmd-j")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "cmd-j") {
		t.Errorf("message: %s", msg)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "{\n  \"global\": {\n    \"cmd-j\": \"mod.open:jira\",\n    \"cmd-q\"") {
		t.Errorf("the binding should go first in global, the rest untouched:\n%s", data)
	}
	var keys map[string]map[string]string
	if err := json.Unmarshal(data, &keys); err != nil || keys["global"]["cmd-p"] != "mod.open:picker" {
		t.Fatalf("result: %v %v", keys, err)
	}

	// Again: nothing changes.
	if msg, err := bindOpenKey(path, "cmd-j"); err != nil || !strings.Contains(msg, "already") {
		t.Errorf("second bind: %s %v", msg, err)
	}
	again, _ := os.ReadFile(path)
	if !bytes.Equal(again, data) {
		t.Errorf("a second install rewrote keys.json")
	}
}

func TestBindOpenKeyRefusesATakenKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	os.WriteFile(path, []byte(sampleKeys), 0o600)
	if _, err := bindOpenKey(path, "cmd-q"); err == nil || !strings.Contains(err.Error(), "already bound") {
		t.Errorf("want a refusal, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != sampleKeys {
		t.Errorf("a refused bind must leave keys.json alone")
	}
}

func TestBindOpenKeyEmptyGlobal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	os.WriteFile(path, []byte(`{"global": {}}`), 0o600)
	if _, err := bindOpenKey(path, "cmd-j"); err != nil {
		t.Fatal(err)
	}
	var keys map[string]map[string]string
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &keys); err != nil || keys["global"]["cmd-j"] != modAction {
		t.Fatalf("%s: %v", data, err)
	}
}

func TestInstallAndUninstall(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "keys.json"), []byte(sampleKeys), 0o600)
	dest := filepath.Join(home, "mods", "jira")
	os.MkdirAll(dest, 0o755)
	os.WriteFile(filepath.Join(dest, "old.js"), []byte("stale"), 0o644)
	os.WriteFile(filepath.Join(dest, "jsconfig.json"), []byte("{}"), 0o644) // the app's

	var out bytes.Buffer
	if err := runAppTo([]string{"install", "--home", home, "--demo"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"main.js", "model.js", "mod.json", "icons/ticket.svg", ".bin/herdr-jira", ".bin/demo", "jsconfig.json"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "old.js")); !os.IsNotExist(err) {
		t.Errorf("a file the mod no longer has should be removed")
	}
	if st, _ := os.Stat(filepath.Join(dest, ".bin", "herdr-jira")); st == nil || st.Mode()&0o111 == 0 {
		t.Errorf("the helper should be executable")
	}
	keys, _ := os.ReadFile(filepath.Join(home, "keys.json"))
	if !strings.Contains(string(keys), modAction) {
		t.Errorf("no key bound:\n%s", keys)
	}

	out.Reset()
	if err := runAppTo([]string{"install", "--home", home}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".bin", "demo")); !os.IsNotExist(err) {
		t.Errorf("installing without --demo should leave demo mode")
	}

	out.Reset()
	if err := runAppTo([]string{"uninstall", "--home", home}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("uninstall left %s", dest)
	}
	keys, _ = os.ReadFile(filepath.Join(home, "keys.json"))
	if string(keys) != sampleKeys {
		t.Errorf("uninstall should restore keys.json exactly:\n%s", keys)
	}
}

func TestInstallNeedsTheAppFolder(t *testing.T) {
	var out bytes.Buffer
	err := runAppTo([]string{"install", "--home", filepath.Join(t.TempDir(), "missing")}, &out)
	if err == nil || !strings.Contains(err.Error(), "no Herdr app folder") {
		t.Fatalf("got %v", err)
	}
}

func TestEmbeddedModHasNoGeneratedFiles(t *testing.T) {
	entries, err := embeddedMod.ReadDir(modEmbedRoot)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, want := range []string{"main.js", "api.js", "model.js", "ui.js", "start.js", "mod.json", "icons"} {
		if !names[want] {
			t.Errorf("embedded mod lacks %s", want)
		}
	}
	for n := range names {
		if n == "jsconfig.json" || strings.HasSuffix(n, ".d.ts") {
			t.Errorf("embedded mod carries a generated file %s", n)
		}
	}
}
