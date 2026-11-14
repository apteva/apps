package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestInstagramPlatformExamplePreservesExistingWorkflows(t *testing.T) {
	load := func(path string) actorDefinition {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var raw any
		if err = json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		d, _, err := decodeActorDefinition(raw)
		if err != nil {
			t.Fatal(path, err)
		}
		return d
	}
	legacy := load("examples/instagram-iphone.json")
	shared := load("examples/instagram.json")
	for name, operation := range legacy.Operations {
		platform := "ios"
		if operation.Browser != nil && operation.Browser.Backend == "local" {
			platform = "web"
		}
		selected, err := selectOperation(shared, name)
		if err != nil {
			t.Fatal(name, err)
		}
		selected, err = selectActorVariant(selected, platform)
		if err != nil {
			t.Fatal(name, err)
		}
		if !reflect.DeepEqual(selected.Steps, operation.Steps) || selected.PrivateScreen != operation.PrivateScreen || selected.ReadOnly != operation.ReadOnly {
			t.Errorf("%s: %s workflow or privacy contract changed", name, platform)
		}
	}
	for _, name := range []string{"switch_account", "read_profile_bio", "set_profile_bio", "clear_profile_bio", "set_profile_picture"} {
		selected, err := selectOperation(shared, name)
		if err != nil {
			t.Fatal(name, err)
		}
		selected, err = selectActorVariant(selected, "android")
		if err != nil {
			t.Fatal(name, err)
		}
		if selected.Browser.ContextID != "{{android_context_id}}" {
			t.Fatal(name, "unbound Android context")
		}
	}
}
