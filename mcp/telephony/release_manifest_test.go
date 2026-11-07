package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestReleaseManifestSourceMatchesVersion(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	embedded := (&App{}).Manifest()
	for name, manifest := range map[string]*sdk.Manifest{"disk": disk, "embedded": &embedded} {
		t.Run(name, func(t *testing.T) {
			if manifest.Name != "telephony" || manifest.Version == "" || manifest.Runtime.Kind != "source" || manifest.Runtime.Source == nil {
				t.Fatal("release must identify Telephony and its source runtime")
			}
			source := manifest.Runtime.Source
			if want := manifest.Name + "/v" + manifest.Version; source.Ref != want {
				t.Errorf("source ref = %q, want %q; installer would build the wrong release", source.Ref, want)
			}
			if source.Repo != "github.com/apteva/apps" || source.Entry != "mcp/telephony" {
				t.Errorf("installer source points outside Telephony: %+v", source)
			}
		})
	}
	if !reflect.DeepEqual(disk, &embedded) {
		t.Error("disk and embedded release manifests differ")
	}
}

func TestReleaseFrontendMatchesManifest(t *testing.T) {
	raw, err := os.ReadFile("ui/frontend.json")
	if err != nil {
		t.Fatal(err)
	}
	var frontend struct {
		App, Version string
		Client       struct{ Path, SHA256 string }
	}
	if err := json.Unmarshal(raw, &frontend); err != nil {
		t.Fatal(err)
	}
	manifest := (&App{}).Manifest()
	if frontend.App != manifest.Name || frontend.Version != manifest.Version {
		t.Fatalf("frontend release %s/%s does not match %s/%s", frontend.App, frontend.Version, manifest.Name, manifest.Version)
	}
	if !strings.HasPrefix(frontend.Client.Path, "/ui/frontend/client-") || filepath.Clean(frontend.Client.Path) != frontend.Client.Path {
		t.Fatalf("invalid frontend asset path: %q", frontend.Client.Path)
	}
	asset, err := os.ReadFile(strings.TrimPrefix(frontend.Client.Path, "/"))
	if err != nil {
		t.Fatal(err)
	}
	if actual := fmt.Sprintf("%x", sha256.Sum256(asset)); actual != frontend.Client.SHA256 {
		t.Fatalf("published frontend hash = %s, manifest claims %s", actual, frontend.Client.SHA256)
	}
}
