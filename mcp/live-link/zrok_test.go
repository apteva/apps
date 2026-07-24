package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestNormalizeZrokName(t *testing.T) {
	for _, valid := range []string{"abc", "my-apteva", strings.Repeat("a", 63)} {
		if got, err := normalizeZrokName(valid); err != nil || got != valid {
			t.Errorf("valid name %q: got=%q err=%v", valid, got, err)
		}
	}
	for _, invalid := range []string{"ab", "UPPER", "has.dot", "under_score", "-leading", "trailing-", strings.Repeat("a", 64)} {
		if _, err := normalizeZrokName(invalid); err == nil {
			t.Errorf("invalid name %q was accepted", invalid)
		}
	}
}

func TestZrokArtifactsArePinned(t *testing.T) {
	for _, platform := range [][2]string{
		{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"},
	} {
		artifact, err := zrokArtifact(platform[0], platform[1])
		if err != nil {
			t.Fatalf("%s/%s: %v", platform[0], platform[1], err)
		}
		if artifact.Version != zrokPinnedVersion || !strings.Contains(artifact.URL, "/v"+zrokPinnedVersion+"/") {
			t.Errorf("%s/%s: artifact is not pinned: %#v", platform[0], platform[1], artifact)
		}
		if err := validateArtifactURL(artifact); err != nil {
			t.Errorf("%s/%s: invalid artifact: %v", platform[0], platform[1], err)
		}
		if artifact.MaxExtracted < 100<<20 {
			t.Errorf("%s/%s: extraction limit %d is below the pinned zrok2 binary size", platform[0], platform[1], artifact.MaxExtracted)
		}
	}
	if _, err := zrokArtifact("windows", "amd64"); err == nil || !strings.Contains(err.Error(), "zrok2_path") {
		t.Fatalf("unsupported platform error=%v", err)
	}
}

func TestEnsureZrokEnvironmentUsesPrivateNativeFiles(t *testing.T) {
	oldEnable := zrokEnableEnvironment
	t.Cleanup(func() { zrokEnableEnvironment = oldEnable })
	calls := 0
	zrokEnableEnvironment = func(_ context.Context, token string) (*zrokEnableResponse, error) {
		calls++
		if token != "secret-enable-token" {
			t.Fatalf("token=%q", token)
		}
		return &zrokEnableResponse{Identity: "identity-id", Config: `{"ztAPI":"https://example.invalid"}`}, nil
	}

	dataDir := t.TempDir()
	if err := ensureZrokEnvironment(dataDir, "secret-enable-token"); err != nil {
		t.Fatal(err)
	}
	// A complete matching environment is reused without consuming another
	// enable request.
	if err := ensureZrokEnvironment(dataDir, "secret-enable-token"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("enable calls=%d, want 1", calls)
	}

	root := filepath.Join(zrokHome(dataDir), ".zrok2")
	for _, path := range []string{
		filepath.Join(root, "metadata.json"),
		filepath.Join(root, "environment.json"),
		filepath.Join(root, "identities", "environment.json"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode=%o, want 600", path, info.Mode().Perm())
		}
	}
	var native zrokNativeEnvironment
	data, err := os.ReadFile(filepath.Join(root, "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &native); err != nil {
		t.Fatal(err)
	}
	if native.AccountToken != "secret-enable-token" || native.ZitiIdentity != "identity-id" {
		t.Fatalf("native environment=%#v", native)
	}
}

func TestZrokStateRoundTripContainsNoCredential(t *testing.T) {
	ctx := newTestCtx(t)
	state := &ZrokState{
		ConnectionID: 42, Namespace: "public", Name: "apteva-test",
		PublicURL: "https://apteva-test.share.zrok.io",
	}
	if err := dbPutZrokState(ctx.AppDB(), state); err != nil {
		t.Fatal(err)
	}
	got, err := dbZrokState(ctx.AppDB())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ConnectionID != 42 || got.Name != state.Name || got.PublicURL != state.PublicURL {
		t.Fatalf("got=%#v", got)
	}
	var schema string
	if err := ctx.AppDB().QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'zrok_state'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"enable_token", "account_token", "credential", "secret"} {
		if strings.Contains(strings.ToLower(schema), forbidden) {
			t.Fatalf("zrok_state schema persists %q: %s", forbidden, schema)
		}
	}
}

func TestConfigureAndDestroyZrokLifecycle(t *testing.T) {
	t.Setenv("APTEVA_DATA_DIR", t.TempDir())
	ctx, platform := newTestCtxWithCF(t)
	platform.bindings["zrok"] = float64(fakeZrokConnID)
	platform.credentials[fakeZrokConnID] = &sdk.ConnectionCredentials{
		Fields: map[string]string{"enable_token": "zrok-test-token"},
	}
	oldEnable, oldCreate, oldDelete := zrokEnableEnvironment, zrokCreateName, zrokDeleteName
	t.Cleanup(func() {
		zrokEnableEnvironment, zrokCreateName, zrokDeleteName = oldEnable, oldCreate, oldDelete
	})
	zrokEnableEnvironment = func(_ context.Context, _ string) (*zrokEnableResponse, error) {
		return &zrokEnableResponse{Identity: "identity-id", Config: `{}`}, nil
	}
	var created, deleted string
	zrokCreateName = func(_ context.Context, token, namespace, name string) error {
		if token != "zrok-test-token" || namespace != "public" {
			t.Fatalf("create token/namespace mismatch")
		}
		created = name
		return nil
	}
	zrokDeleteName = func(_ context.Context, token, namespace, name string) error {
		if token != "zrok-test-token" || namespace != "public" {
			t.Fatalf("delete token/namespace mismatch")
		}
		deleted = name
		return nil
	}

	app := &App{mgr: NewManager(nil, nil)}
	app.providers = []Provider{&zrokProvider{app: app}, &cloudflareQuickProvider{app: app}}
	raw := json.RawMessage(`{"name":"stable-link"}`)
	got, err := app.configureProvider(ctx, providerNameZrok, raw)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := got.(*ZrokState)
	if !ok || state.PublicURL != "https://stable-link.share.zrok.io" || created != "stable-link" {
		t.Fatalf("configuration=%#v created=%q", got, created)
	}
	if active := app.activeProviderName(ctx); active != providerNameZrok {
		t.Fatalf("active provider=%q", active)
	}
	destroyed, err := app.destroyProviderSafe(ctx, providerNameZrok)
	if err != nil || !destroyed {
		t.Fatalf("destroyed=%v err=%v", destroyed, err)
	}
	if deleted != "stable-link" {
		t.Fatalf("deleted=%q", deleted)
	}
	if active := app.activeProviderName(ctx); active != providerNameQuick {
		t.Fatalf("active provider after destroy=%q", active)
	}
}
