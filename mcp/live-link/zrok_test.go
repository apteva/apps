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

func TestZrokPublicURLUsesNamespaceReturnedByAPI(t *testing.T) {
	got, err := zrokPublicURL("apteva", "shares.zrok.io")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://apteva.shares.zrok.io" {
		t.Fatalf("url=%q", got)
	}
	for _, invalid := range []string{"", "https://shares.zrok.io", "shares.zrok.io/path", "-bad.example", "bad_.example"} {
		if _, err := zrokPublicURL("apteva", invalid); err == nil {
			t.Errorf("invalid namespace hostname %q was accepted", invalid)
		}
	}
}

func TestZrokEndpointHostRejectsUnexpectedURLParts(t *testing.T) {
	for _, valid := range []string{"apteva.shares.zrok.io", "https://apteva.shares.zrok.io/"} {
		if host, err := zrokEndpointHost(valid); err != nil || host != "apteva.shares.zrok.io" {
			t.Errorf("valid endpoint %q: host=%q err=%v", valid, host, err)
		}
	}
	for _, invalid := range []string{
		"http://apteva.shares.zrok.io",
		"https://user@apteva.shares.zrok.io",
		"https://apteva.shares.zrok.io:443",
		"https://apteva.shares.zrok.io/path",
		"https://apteva.shares.zrok.io/?query=1",
	} {
		if _, err := zrokEndpointHost(invalid); err == nil {
			t.Errorf("invalid endpoint %q was accepted", invalid)
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
	loaded, err := readZrokEnvironment(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ZitiIdentity != "identity-id" {
		t.Fatalf("loaded environment=%#v", loaded)
	}
}

func TestReconcileZrokSharesDeletesOnlyOwnedReservedEndpoint(t *testing.T) {
	oldList, oldDelete := zrokListShares, zrokDeleteShare
	t.Cleanup(func() {
		zrokListShares, zrokDeleteShare = oldList, oldDelete
	})
	zrokListShares = func(_ context.Context, token, envZID string) ([]zrokShareSummary, error) {
		if token != "enable-token" || envZID != "current-environment" {
			t.Fatalf("list token/environment mismatch")
		}
		return []zrokShareSummary{
			{
				BackendMode: "proxy", EnvZID: "current-environment",
				FrontendEndpoints: []string{"apteva.shares.zrok.io"},
				ShareMode:         "public", ShareToken: "owned-stale-share",
			},
			{
				BackendMode: "proxy", EnvZID: "another-environment",
				FrontendEndpoints: []string{"apteva.shares.zrok.io"},
				ShareMode:         "public", ShareToken: "other-environment",
			},
			{
				BackendMode: "proxy", EnvZID: "current-environment",
				FrontendEndpoints: []string{"different.shares.zrok.io"},
				ShareMode:         "public", ShareToken: "different-endpoint",
			},
			{
				BackendMode: "web", EnvZID: "current-environment",
				FrontendEndpoints: []string{"apteva.shares.zrok.io"},
				ShareMode:         "public", ShareToken: "different-backend",
			},
		}, nil
	}
	var deleted []string
	zrokDeleteShare = func(_ context.Context, token, envZID, shareToken string) error {
		if token != "enable-token" || envZID != "current-environment" {
			t.Fatalf("delete token/environment mismatch")
		}
		deleted = append(deleted, shareToken)
		return nil
	}

	removed, err := reconcileZrokShares(
		context.Background(), "enable-token", "current-environment",
		"https://apteva.shares.zrok.io",
	)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || len(deleted) != 1 || deleted[0] != "owned-stale-share" {
		t.Fatalf("removed=%d deleted=%v", removed, deleted)
	}
}

func TestReconcileZrokSharesRefusesMatchingShareWithoutToken(t *testing.T) {
	oldList := zrokListShares
	t.Cleanup(func() { zrokListShares = oldList })
	zrokListShares = func(_ context.Context, _, _ string) ([]zrokShareSummary, error) {
		return []zrokShareSummary{{
			BackendMode: "proxy", EnvZID: "current-environment",
			FrontendEndpoints: []string{"https://apteva.shares.zrok.io/"},
			ShareMode:         "public",
		}}, nil
	}
	if _, err := reconcileZrokShares(
		context.Background(), "enable-token", "current-environment",
		"https://apteva.shares.zrok.io",
	); err == nil || !strings.Contains(err.Error(), "without a share token") {
		t.Fatalf("error=%v", err)
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
	oldEnable, oldCreate, oldDelete, oldResolve := zrokEnableEnvironment, zrokCreateName, zrokDeleteName, zrokResolveName
	t.Cleanup(func() {
		zrokEnableEnvironment, zrokCreateName, zrokDeleteName, zrokResolveName = oldEnable, oldCreate, oldDelete, oldResolve
	})
	zrokEnableEnvironment = func(_ context.Context, _ string) (*zrokEnableResponse, error) {
		return &zrokEnableResponse{Identity: "identity-id", Config: `{}`}, nil
	}
	var created, deleted string
	createCalls := 0
	zrokCreateName = func(_ context.Context, token, namespace, name string) error {
		if token != "zrok-test-token" || namespace != "public" {
			t.Fatalf("create token/namespace mismatch")
		}
		createCalls++
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
	zrokResolveName = func(_ context.Context, token, namespace, name string) (string, error) {
		if token != "zrok-test-token" || namespace != "public" {
			t.Fatalf("resolve token/namespace mismatch")
		}
		return "https://" + name + ".shares.zrok.io", nil
	}

	app := &App{mgr: NewManager(nil, nil)}
	app.providers = []Provider{&zrokProvider{app: app}, &cloudflareQuickProvider{app: app}}
	raw := json.RawMessage(`{"name":"stable-link"}`)
	got, err := app.configureProvider(ctx, providerNameZrok, raw)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := got.(*ZrokState)
	if !ok || state.PublicURL != "https://stable-link.shares.zrok.io" || created != "stable-link" {
		t.Fatalf("configuration=%#v created=%q", got, created)
	}
	state.PublicURL = "https://stable-link.share.zrok.io"
	if err := dbPutZrokState(ctx.AppDB(), state); err != nil {
		t.Fatal(err)
	}
	got, err = app.configureProvider(ctx, providerNameZrok, raw)
	if err != nil {
		t.Fatal(err)
	}
	state = got.(*ZrokState)
	if state.PublicURL != "https://stable-link.shares.zrok.io" || createCalls != 1 {
		t.Fatalf("refreshed configuration=%#v create calls=%d", state, createCalls)
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
