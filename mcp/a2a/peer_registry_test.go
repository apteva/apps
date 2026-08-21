package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func appCallerCtx(installID int64, name string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		AppInstallID: installID,
		AppName:      name,
		ProjectID:    testProject,
	})
}

func TestConfiguredPeersReconcileIntoEncryptedRegistry(t *testing.T) {
	const token = "operator-peer-token-that-must-not-be-plaintext"
	raw, _ := json.Marshal([]peerConfig{{
		ID: "main", Name: "Main", BaseURL: "https://agents.example.com/api/apps/a2a", Token: token,
		DiscoverAgents: []string{" Support ", "Support"}, InvokeAgents: []string{"Support"},
	}})
	ctx, _ := newTestEnvWithConfig(t, map[string]string{"peers_json": string(raw)})
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}

	var encrypted, hash []byte
	var owner sql.NullInt64
	if err := ctx.AppDB().QueryRow(`SELECT encrypted_token, token_hash, owner_install_id FROM a2a_peers WHERE id = 'main'`).
		Scan(&encrypted, &hash, &owner); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte(token)) || string(encrypted) == token {
		t.Fatal("peer token was stored in plaintext")
	}
	if len(hash) == 0 || owner.Valid {
		t.Fatalf("registry metadata hash=%d owner=%+v", len(hash), owner)
	}
	peers, err := peerConfigs(ctx)
	if err != nil || len(peers) != 1 {
		t.Fatalf("peerConfigs() = %+v, %v", peers, err)
	}
	if peers[0].Token != token || len(peers[0].DiscoverAgents) != 1 || peers[0].DiscoverAgents[0] != "Support" {
		t.Fatalf("reloaded peer = %+v", peers[0])
	}

	ctx.Config()["peers_json"] = "[]"
	if err := syncConfiguredPeers(ctx); err != nil {
		t.Fatal(err)
	}
	peers, err = peerConfigs(ctx)
	if err != nil || len(peers) != 0 {
		t.Fatalf("removed configured peer still present: %+v, %v", peers, err)
	}
}

func TestPeerKeyPersistsBesideDatabase(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "a2a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, nil, nil)
	first, err := resolvePeerMasterKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolvePeerMasterKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || !bytes.Equal(first, second) {
		t.Fatal("peer master key did not persist")
	}
	info, err := os.Stat(filepath.Join(dir, peerKeyFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("peer key permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestAppManagedPeerOwnershipAndRemoval(t *testing.T) {
	ctx, _ := newTestEnv(t)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	fleet := appCallerCtx(71, "fleet")
	other := appCallerCtx(72, "other-controller")
	args := map[string]any{
		"id": "tenant-123", "name": "Tenant 123",
		"base_url":        "https://tenant.example.com/api/apps/a2a",
		"token":           "managed-relationship-token",
		"discover_agents": []any{"*"},
		"invoke_agents":   []any{"Support"},
	}
	got := resultMap(t)(app.toolPeerUpsert(fleet, ctx, args))
	if got["owner_install_id"] != int64(71) {
		t.Fatalf("upsert result = %+v", got)
	}

	// Reconciliation is idempotent and app-managed rows survive an empty
	// operator configuration.
	if _, err := app.toolPeerUpsert(fleet, ctx, args); err != nil {
		t.Fatal(err)
	}
	peers, err := peerConfigs(ctx)
	if err != nil || len(peers) != 1 || peers[0].ID != "tenant-123" {
		t.Fatalf("managed peers = %+v, %v", peers, err)
	}
	if _, err := app.toolPeerUpsert(other, ctx, args); err == nil || !strings.Contains(err.Error(), "another app install") {
		t.Fatalf("other owner upsert error = %v", err)
	}
	if _, err := app.toolPeerRemove(other, ctx, map[string]any{"id": "tenant-123"}); err == nil || !strings.Contains(err.Error(), "another app install") {
		t.Fatalf("other owner removal error = %v", err)
	}

	removed := resultMap(t)(app.toolPeerRemove(fleet, ctx, map[string]any{"id": "tenant-123"}))
	if removed["removed"] != true {
		t.Fatalf("remove result = %+v", removed)
	}
	removed = resultMap(t)(app.toolPeerRemove(fleet, ctx, map[string]any{"id": "tenant-123"}))
	if removed["removed"] != false {
		t.Fatalf("idempotent remove result = %+v", removed)
	}
}

func TestOperatorAndAppManagedPeersCannotClobberEachOther(t *testing.T) {
	ctx, _ := newTestEnv(t)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"id": "shared-id", "base_url": "https://tenant.example.com/api/apps/a2a",
		"token": "owned-token", "discover_agents": []any{"*"}, "invoke_agents": []any{"*"},
	}
	if _, err := app.toolPeerUpsert(appCallerCtx(71, "fleet"), ctx, args); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]peerConfig{{
		ID: "shared-id", BaseURL: "https://manual.example.com/api/apps/a2a", Token: "manual-token",
	}})
	ctx.Config()["peers_json"] = string(raw)
	if err := syncConfiguredPeers(ctx); err == nil || !strings.Contains(err.Error(), "managed by app install 71") {
		t.Fatalf("configured collision error = %v", err)
	}
}

func TestPeerRegistryToolsArePrivateAndRequireAppCaller(t *testing.T) {
	app := &App{}
	want := map[string]bool{"node_info": true, "peer_upsert": true, "peer_remove": true}
	for _, tool := range app.MCPTools() {
		if want[tool.Name] {
			if tool.Exposure != sdk.ToolExposureAppOnly {
				t.Errorf("tool %s exposure=%q, want app_only", tool.Name, tool.Exposure)
			}
			delete(want, tool.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing private tools: %+v", want)
	}

	ctx, _ := newTestEnv(t)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolNodeInfo(context.Background(), ctx, nil); err == nil || !strings.Contains(err.Error(), "app caller") {
		t.Fatalf("node_info without app caller error = %v", err)
	}
	first := resultMap(t)(app.toolNodeInfo(appCallerCtx(71, "fleet"), ctx, nil))
	second := resultMap(t)(app.toolNodeInfo(appCallerCtx(71, "fleet"), ctx, nil))
	if first["node_id"] == "" || first["node_id"] != second["node_id"] {
		t.Fatalf("node identity is not stable: first=%+v second=%+v", first, second)
	}
	var nodes int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM a2a_node`).Scan(&nodes); err != nil || nodes != 1 {
		t.Fatalf("a2a_node count=%d err=%v", nodes, err)
	}
}
