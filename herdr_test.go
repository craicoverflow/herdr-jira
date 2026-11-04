package main

import (
	"encoding/json"
	"testing"
)

// TestOpenPopupNeverSendsNullEnv guards against a real bug found running
// the "open" action against a live herdr server: encoding/json marshals a
// nil map[string]string as `null`, and herdr's socket API rejected that
// with "invalid type: null, expected a map" for plugin.pane.open's env
// field.
func TestOpenPopupNeverSendsNullEnv(t *testing.T) {
	var gotParams map[string]any
	orig := herdrCallFn
	herdrCallFn = func(method string, params any, out any) error {
		data, _ := json.Marshal(params)
		json.Unmarshal(data, &gotParams)
		return nil
	}
	defer func() { herdrCallFn = orig }()

	if err := openPopup(nil); err != nil {
		t.Fatalf("openPopup: %v", err)
	}
	if gotParams["env"] == nil {
		t.Error("env marshaled as null; herdr's socket API rejects this")
	}
	if _, ok := gotParams["env"].(map[string]any); !ok {
		t.Errorf("env = %#v (%T), want a map", gotParams["env"], gotParams["env"])
	}
}
