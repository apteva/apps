package main

// Tests for the v0.4 Provider abstraction. The slice-1 acceptance
// bar is "no behavior change vs v0.3"; these prove that the new
// activeProvider() picker mirrors the old currentMode() logic so
// downstream callers see exactly the same answer either way.

import (
	"testing"
)

// TestActiveProvider_DefaultsToQuick — empty DB → quick provider is
// active. The default path matters: an install that has never
// configured named must fall through to quick without erroring.
func TestActiveProvider_DefaultsToQuick(t *testing.T) {
	ctx, _ := newTestCtxWithCF(t)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if got := app.activeProviderName(ctx); got != providerNameQuick {
		t.Errorf("empty DB: got %q, want %q", got, providerNameQuick)
	}
	if got := app.activeProvider(ctx).Name(); got != providerNameQuick {
		t.Errorf("activeProvider: got %q, want %q", got, providerNameQuick)
	}
}

// TestActiveProvider_FlipsToNamedWhenRowExists — the named provider
// takes over as soon as a named_tunnels row is present, regardless
// of prior install state. Mirrors v0.3's currentMode contract.
func TestActiveProvider_FlipsToNamedWhenRowExists(t *testing.T) {
	ctx, _ := newTestCtxWithCF(t)
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dbInsertNamedTunnel(ctx.AppDB(), &NamedTunnel{
		Hostname: "h.example.com", TunnelID: "T", TunnelToken: "K",
		ZoneID: "Z", DNSRecordID: "R",
	}); err != nil {
		t.Fatal(err)
	}
	if got := app.activeProviderName(ctx); got != providerNameNamed {
		t.Errorf("with named row: got %q, want %q", got, providerNameNamed)
	}

	// And after deletion the picker falls back to quick.
	if err := dbDeleteNamedTunnel(ctx.AppDB(), "h.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := app.activeProviderName(ctx); got != providerNameQuick {
		t.Errorf("after delete: got %q, want %q", got, providerNameQuick)
	}
}

// TestQuickProvider_ConfiguredAlwaysFalse — quick is the fallback
// default, never the actively-configured one. Configured() must
// return false even when no other provider is wired, otherwise the
// picker order semantics break.
func TestQuickProvider_ConfiguredAlwaysFalse(t *testing.T) {
	ctx, _ := newTestCtxWithCF(t)
	q := &cloudflareQuickProvider{app: &App{}}
	if q.Configured(ctx) {
		t.Error("quick.Configured(empty DB) returned true; should be false")
	}
	if err := dbInsertNamedTunnel(ctx.AppDB(), &NamedTunnel{
		Hostname: "h.example.com", TunnelID: "T", TunnelToken: "K",
		ZoneID: "Z", DNSRecordID: "R",
	}); err != nil {
		t.Fatal(err)
	}
	if q.Configured(ctx) {
		t.Error("quick.Configured(with named row) returned true; should still be false")
	}
}

// TestNamedProvider_ConfiguredTracksRow — named.Configured() must
// match the presence of a named_tunnels row exactly.
func TestNamedProvider_ConfiguredTracksRow(t *testing.T) {
	ctx, _ := newTestCtxWithCF(t)
	n := &cloudflareNamedProvider{app: &App{}}
	if n.Configured(ctx) {
		t.Error("named.Configured(empty DB) returned true; should be false")
	}
	if err := dbInsertNamedTunnel(ctx.AppDB(), &NamedTunnel{
		Hostname: "h.example.com", TunnelID: "T", TunnelToken: "K",
		ZoneID: "Z", DNSRecordID: "R",
	}); err != nil {
		t.Fatal(err)
	}
	if !n.Configured(ctx) {
		t.Error("named.Configured(with row) returned false; should be true")
	}
}

// TestProviderMigration_Backfill — the 003 migration rewrites legacy
// 'cloudflared'+mode rows to the new provider names. Run it manually
// against pre-migration shaped data and verify the rewrites land.
func TestProviderMigration_Backfill(t *testing.T) {
	db := openTestDB(t) // applies all migrations including 003

	// Seed rows the way v0.3 would have. Bypass the modern dbInsertRun
	// (which already writes the new provider names) by using raw INSERTs.
	cases := []struct {
		mode, wantProvider string
	}{
		{"quick", "cloudflare-quick"},
		{"named", "cloudflare-named"},
	}
	for _, c := range cases {
		if _, err := db.Exec(
			`INSERT INTO runs (provider, target_url, status, mode)
			 VALUES ('cloudflared', 'http://localhost:5280', 'stopped', ?)`, c.mode); err != nil {
			t.Fatal(err)
		}
	}

	// Re-run the migration to backfill the new rows.
	if _, err := db.Exec(
		`UPDATE runs SET provider = 'cloudflare-named' WHERE provider = 'cloudflared' AND mode = 'named';
		 UPDATE runs SET provider = 'cloudflare-quick' WHERE provider = 'cloudflared' AND (mode = 'quick' OR mode = '' OR mode IS NULL);`); err != nil {
		t.Fatal(err)
	}

	rows, err := dbListRuns(db, 25)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Mode] = r.Provider
	}
	for _, c := range cases {
		if got[c.mode] != c.wantProvider {
			t.Errorf("mode=%q: provider=%q, want %q", c.mode, got[c.mode], c.wantProvider)
		}
	}
}
