package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Twenty stored specs total the report's 4,025,080 bytes. Structured integration
// data exercises real map/array decoding, rather than only one large string.
func largeReconcileSpec(t testing.TB) EnvironmentSpec {
	t.Helper()
	records := make([]map[string]any, 1000)
	for i := range records {
		records[i] = map[string]any{"id": fmt.Sprint(i), "owner": "customer-123", "status": "available", "start": "2026-10-14T15:00:00Z", "duration": 60, "tags": []string{"booking", "fixture"}}
	}
	data := map[string]any{"records": records, "padding": ""}
	spec := EnvironmentSpec{Version: 1, IntegrationFixtures: []sdk.RuntimeIntegrationMock{{App: "booking", Tool: "availability", Data: data}}}
	raw, err := rawSpec(spec)
	if err != nil || len(raw) > 201254 {
		t.Fatalf("large spec size=%d err=%v", len(raw), err)
	}
	data["padding"] = strings.Repeat("x", 201254-len(raw))
	return spec
}

func saveLargeStoppedDefinitions(t testing.TB, s *service) {
	t.Helper()
	spec := largeReconcileSpec(t)
	for i := 0; i < 20; i++ {
		if err := s.db.saveDefinition(&Definition{ID: fmt.Sprintf("stopped-%02d", i), Name: "Large stopped definition", DesiredState: "stopped", Spec: spec}); err != nil {
			t.Fatal(err)
		}
	}
	var bytes int
	if err := s.db.db.QueryRow(`SELECT SUM(length(spec_json)) FROM environment_definitions`).Scan(&bytes); err != nil || bytes != 4025080 {
		t.Fatalf("stored bytes=%d err=%v", bytes, err)
	}
}

func TestReconcileIdleDoesNotDecodeSpecifications(t *testing.T) {
	s, p := auditService(t)
	s.db.db.SetMaxOpenConns(1)
	saveLargeStoppedDefinitions(t, s)
	for i := 0; i < 10; i++ {
		if err := s.reconcile(context.Background()); err != nil {
			t.Fatalf("large idle tick %d: %v", i, err)
		}
	}
	// A decoder sentinel makes an accidental read/unmarshal fail immediately.
	// Lifecycle work must remain independent of stopped specification payloads.
	if _, err := s.db.db.Exec(`UPDATE environment_definitions SET spec_json='invalid JSON decoder sentinel'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.listDefinitions(); err == nil {
		t.Fatal("decoder sentinel failed to detect full definition decoding")
	}
	for i := 0; i < 10; i++ {
		if err := s.reconcile(context.Background()); err != nil {
			t.Fatalf("decoder sentinel idle tick %d: %v", i, err)
		}
	}
	if p.created != 0 || p.destroyed != 0 {
		t.Fatalf("idle tick changed runtime state: created=%d destroyed=%d", p.created, p.destroyed)
	}
}

func TestReconcileSkipsSpecsForActiveBackedOffAndDegradedDefinitions(t *testing.T) {
	s, p := auditService(t)
	for _, id := range []string{"active", "backoff", "degraded"} {
		if err := s.db.saveDefinition(&Definition{ID: id, Name: id, DesiredState: "running", Spec: EnvironmentSpec{Version: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	run := &Run{ID: "run-active", EnvironmentID: "active", RuntimeID: "rt-active", Status: "running", StartedAt: time.Now()}
	if err := s.db.createRun(run); err != nil {
		t.Fatal(err)
	}
	p.live[run.RuntimeID] = sdk.RuntimeSummary{ID: run.RuntimeID}
	next := time.Now().Add(time.Hour)
	if err := s.db.setReconcileFailure("backoff", 1, "backoff", "retrying", &next, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.db.setReconcileFailure("degraded", 5, "degraded", "degraded", nil, &next); err != nil {
		t.Fatal(err)
	}
	// Health recovery must clear failure metadata without reading its spec.
	if err := s.db.setReconcileFailure("active", 1, "recovered", "retrying", &next, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.db.Exec(`UPDATE environment_definitions SET spec_json='invalid JSON decoder sentinel'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := s.reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	d, err := s.db.getDefinitionLifecycle("active")
	if err != nil || d.ReconcileStatus != "healthy" || d.ReconcileFailures != 0 || !d.HasActiveRun || p.created != 0 || p.destroyed != 0 {
		t.Fatalf("healthy active state=%+v created=%d destroyed=%d err=%v", d, p.created, p.destroyed, err)
	}
}

type reconcileCapturePlatform struct {
	*auditPlatform
	requests []sdk.RuntimeCreateRequest
}

func (p *reconcileCapturePlatform) CreateRuntime(req sdk.RuntimeCreateRequest) (*sdk.RuntimeSummary, error) {
	p.requests = append(p.requests, req)
	return p.auditPlatform.CreateRuntime(req)
}

func TestReconcileRecoveryUsesUpdatedSpecification(t *testing.T) {
	s, base := auditService(t)
	p := &reconcileCapturePlatform{auditPlatform: base}
	s.ctx = sdk.NewAppCtxForTest(nil, s.db.db, nil, p, nil).WithProject("audit")
	if err := s.db.saveDefinition(&Definition{ID: "env", Name: "Updated", DesiredState: "running", Spec: EnvironmentSpec{Version: 1, TTLSeconds: 60}}); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 || p.requests[0].TTLSeconds != 60 {
		t.Fatalf("initial requests=%+v", p.requests)
	}
	if _, err := s.updateDefinition("env", map[string]any{"spec": map[string]any{"ttl_seconds": 120}}); err != nil {
		t.Fatal(err)
	}
	delete(p.live, p.requests[0].ID)
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err := s.db.getDefinitionLifecycle("env")
	if err != nil || d.HasActiveRun || d.ReconcileStatus != "retrying" || d.ReconcileFailures != 1 || len(p.requests) != 1 {
		t.Fatalf("missing runtime bypassed recovery backoff: state=%+v requests=%d err=%v", d, len(p.requests), err)
	}
	if _, err := s.db.db.Exec(`UPDATE environment_definitions SET reconcile_next_at=? WHERE id='env'`, time.Now().Add(-time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 || p.requests[1].TTLSeconds != 120 {
		t.Fatalf("recovery used stale specification: %+v", p.requests)
	}
	if err := s.stopDefinition("env"); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err = s.db.getDefinitionLifecycle("env")
	if err != nil || d.DesiredState != "stopped" || d.HasActiveRun || len(p.requests) != 2 {
		t.Fatalf("stop was undone: state=%+v requests=%d err=%v", d, len(p.requests), err)
	}
}

func TestReconcileCleanupFailureDoesNotDecodeSpecification(t *testing.T) {
	s, p := auditService(t)
	if err := s.db.saveDefinition(&Definition{ID: "env", Name: "Cleanup", DesiredState: "stopped"}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.createRun(&Run{ID: "run", EnvironmentID: "env", RuntimeID: "rt", Status: "stopping", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.db.Exec(`UPDATE environment_definitions SET spec_json='invalid JSON decoder sentinel'`); err != nil {
		t.Fatal(err)
	}
	p.destroyErr = errors.New("temporary cleanup failure")
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err := s.db.getDefinitionLifecycle("env")
	if err != nil || d.ReconcileFailures != 1 || d.ReconcileStatus != "retrying" || !d.HasActiveRun {
		t.Fatalf("cleanup failed to record metadata: %+v err=%v", d, err)
	}
	if err := s.reconcile(context.Background()); err != nil || p.destroyed != 1 {
		t.Fatalf("cleanup backoff ignored: destroys=%d err=%v", p.destroyed, err)
	}
	if _, err := s.db.db.Exec(`UPDATE environment_definitions SET reconcile_next_at=? WHERE id='env'`, time.Now().Add(-time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	p.destroyErr = nil
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err = s.db.getDefinitionLifecycle("env")
	if err != nil || d.HasActiveRun || d.ReconcileStatus != "healthy" || d.ReconcileFailures != 0 || p.destroyed != 2 {
		t.Fatalf("cleanup recovery failed: %+v destroys=%d err=%v", d, p.destroyed, err)
	}
}

func TestReconcileInvalidEligibleSpecIsBackedOffIndependently(t *testing.T) {
	s, p := auditService(t)
	for _, id := range []string{"broken", "valid"} {
		if err := s.db.saveDefinition(&Definition{ID: id, Name: id, DesiredState: "running"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.db.Exec(`UPDATE environment_definitions SET spec_json='invalid JSON decoder sentinel' WHERE id='broken'`); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, err := s.db.getDefinitionLifecycle("broken")
	if err != nil || d.ReconcileFailures != 1 || d.ReconcileStatus != "retrying" || p.created != 1 {
		t.Fatalf("invalid spec blocked another definition: %+v created=%d err=%v", d, p.created, err)
	}
}

func BenchmarkIdleReconcileLargeDefinitions(b *testing.B) {
	for _, name := range []string{"previous_full_specs", "metadata_only"} {
		b.Run(name, func(b *testing.B) {
			b.Setenv("ENVIRONMENTS_RETENTION_DAYS", "0")
			s, _ := auditService(b)
			saveLargeStoppedDefinitions(b, s)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if name == "metadata_only" {
					if err := s.reconcile(context.Background()); err != nil {
						b.Fatal(err)
					}
					continue
				}
				// Same idle path as v0.7.11/12: live runtimes, all specs,
				// active runs, then all specs again, with retention unchanged.
				if _, err := s.liveMap(); err != nil {
					b.Fatal(err)
				}
				if _, err := s.db.listDefinitions(); err != nil {
					b.Fatal(err)
				}
				if _, err := s.db.activeRuns(""); err != nil {
					b.Fatal(err)
				}
				if _, err := s.db.listDefinitions(); err != nil {
					b.Fatal(err)
				}
				if err := s.pruneHistory(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
