package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestHealthPoolBoundsConcurrentRequests(t *testing.T) {
	a, ctx := newTestApp(t)
	var current, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := current.Add(1)
		defer current.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"version":"0.41.0"}`))
	}))
	defer srv.Close()
	enc, err := a.keys.seal([]byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 128; i++ {
		tn := &Tenant{Slug: fmt.Sprintf("load-%d", i), Kind: KindRemote, BaseURL: srv.URL, OwnerEmail: "load@example.com", Status: StatusActive}
		if err = a.store.insert(tn, enc, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.runHealthPoller(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 8 || peak.Load() < 2 {
		t.Fatalf("request concurrency=%d, expected 2..8", peak.Load())
	}
}
func TestSnapshotStreamsByDefaultAndLegacyWriterIsBounded(t *testing.T) {
	a, ctx := newTestApp(t)
	t.Setenv("APTEVA_PUBLIC_URL", "https://controller.example")
	id := seedTenant(t, a, "stream-default", StatusStopped)
	tn, _, _ := a.store.get(id)
	if err := os.MkdirAll(tn.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	out, err := a.toolFleetTenantSnapshot(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["archive_url"] == nil || result["archive_b64"] != nil {
		t.Fatal("snapshot default is not streaming")
	}
	var buf bytes.Buffer
	writer := boundedWriter{w: &buf, remaining: 4}
	if _, err = writer.Write([]byte("12345")); err == nil || buf.Len() != 0 {
		t.Fatal("legacy writer allocated past its limit")
	}
}
func TestMigrationCommitIsAtomicAndCleanupRetainsCurrentPorts(t *testing.T) {
	a, _ := newTestApp(t)
	id := seedTenant(t, a, "commit", StatusStopped)
	tn, _, _ := a.store.get(id)
	source := &RetainedSource{TenantID: id, SourceInstanceID: 0, SourceConfigDir: tn.ConfigDir, SourceSlug: tn.Slug}
	if err := a.store.createRetainedSource(source); err != nil {
		t.Fatal(err)
	}
	if err := a.store.commitMigration(id, 2, "http://203.0.113.1:7100", remoteFleetRoot+"/commit", source); err == nil {
		t.Fatal("duplicate retained source accepted")
	}
	got, _, _ := a.store.get(id)
	if got.InstanceID != 0 || got.ConfigDir != tn.ConfigDir {
		t.Fatal("partial location commit")
	}
	if err := a.store.deleteRetainedSource(id); err != nil {
		t.Fatal(err)
	}
	if err := a.store.commitMigration(id, 2, "http://203.0.113.1:7100", remoteFleetRoot+"/commit", source); err != nil {
		t.Fatal(err)
	}
	if err := a.store.releaseRetiredPorts(id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM fleet_port_reservations WHERE tenant_id=? AND instance_id=2 AND port=7100`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("current reservation=%d %v", count, err)
	}
}

func BenchmarkFleetList5000(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	migrations, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		b.Fatal(err)
	}
	for _, path := range migrations {
		data, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = db.Exec(string(data)); err != nil {
			b.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		_, err = tx.Exec(`INSERT INTO fleet_tenants(id,slug,kind,base_url,api_key_enc,owner_email,status) VALUES(?,?,'remote','https://tenant.example',X'00','owner@example.com','active')`, fmt.Sprint(i), fmt.Sprintf("tenant-%d", i))
		if err != nil {
			b.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	s := &store{db: db}
	for _, test := range []struct {
		name   string
		filter map[string]string
	}{{"full", map[string]string{}}, {"page50", map[string]string{"limit": "50"}}} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				rows, err := s.list(test.filter)
				if err != nil {
					b.Fatal(err)
				}
				data, err := json.Marshal(rows)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(data)))
			}
		})
	}
}
