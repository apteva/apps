package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// stubIntegrationPlatform records the last ExecuteIntegrationTool call
// and returns a canned ExecuteResult. ListConnections drives the slug
// resolver — set Connections to the rows you want a slug lookup to see.
type stubIntegrationPlatform struct {
	tk.BasePlatformClient

	Connections []sdk.PlatformConnection

	lastConnID int64
	lastTool   string
	lastInput  map[string]any

	result *sdk.ExecuteResult
}

func (s *stubIntegrationPlatform) ListConnections(f sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	var out []sdk.PlatformConnection
	for _, c := range s.Connections {
		if f.AppSlug != "" && c.AppSlug != f.AppSlug {
			continue
		}
		if f.ProjectID != "" && c.ProjectID != "" && c.ProjectID != f.ProjectID {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *stubIntegrationPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	s.lastConnID, s.lastTool, s.lastInput = connID, tool, input
	if s.result != nil {
		return s.result, nil
	}
	return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"ok":true}`), Status: 200}, nil
}

// TestContextIntegrationByID: numeric connection id is passed straight
// through to ExecuteIntegrationTool. No slug resolution involved.
func TestContextIntegrationByID(t *testing.T) {
	requireBin(t, "node")
	stub := &stubIntegrationPlatform{
		result: &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"sent":true}`), Status: 200},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "ping-by-id",
		"source": `export default async (event, context) => {
			const r = await context.integration(31, "pushover_send_notification", { message: "hi" });
			return r;
		};`,
	})

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
	if stub.lastConnID != 31 {
		t.Errorf("connID = %d, want 31", stub.lastConnID)
	}
	if stub.lastTool != "pushover_send_notification" {
		t.Errorf("tool = %q, want pushover_send_notification", stub.lastTool)
	}
	if !strings.Contains(res.Response, `"sent":true`) {
		t.Errorf("response = %q, want sent:true", res.Response)
	}
}

// TestContextIntegrationBySlugUnique: slug "pushover" resolves to the
// single matching connection, then ExecuteIntegrationTool is called
// with that connection's id.
func TestContextIntegrationBySlugUnique(t *testing.T) {
	requireBin(t, "node")
	clearConnSlugCache()
	stub := &stubIntegrationPlatform{
		Connections: []sdk.PlatformConnection{
			{ID: 31, AppSlug: "pushover", Name: "Pushover", ProjectID: testProj},
		},
		result: &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"ok":1}`), Status: 200},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "ping-by-slug",
		"source": `export default async (event, context) => {
			return await context.integration("pushover", "pushover_send_notification", { message: "hi" });
		};`,
	})

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
	if stub.lastConnID != 31 {
		t.Errorf("connID resolved from slug = %d, want 31", stub.lastConnID)
	}
}

// TestContextIntegrationBySlugMissing: slug with no matching connection
// in the project should throw a clear error the handler can catch.
func TestContextIntegrationBySlugMissing(t *testing.T) {
	requireBin(t, "node")
	clearConnSlugCache()
	stub := &stubIntegrationPlatform{
		// No connections at all — slug lookup returns empty.
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "ping-missing",
		"source": `export default async (event, context) => {
			try {
				await context.integration("pushover", "pushover_send_notification", { message: "hi" });
				return { unexpected: "succeeded" };
			} catch (e) {
				return { err: String(e.message || e) };
			}
		};`,
	})

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok — handler should catch the rejection (err=%q)", res.Status, res.Error)
	}
	if !strings.Contains(res.Response, `no \"pushover\" connection`) {
		t.Errorf("expected missing-connection error, got %s", res.Response)
	}
	if stub.lastConnID != 0 {
		t.Errorf("ExecuteIntegrationTool should not have been called; got connID=%d", stub.lastConnID)
	}
}

// TestContextIntegrationBySlugAmbiguous: multiple connections of the
// same slug should error with the candidate ids so the handler author
// can disambiguate.
func TestContextIntegrationBySlugAmbiguous(t *testing.T) {
	requireBin(t, "node")
	// Clear cache from previous tests so the multi-match codepath runs
	// instead of returning a stale single-match.
	clearConnSlugCache()
	stub := &stubIntegrationPlatform{
		Connections: []sdk.PlatformConnection{
			{ID: 31, AppSlug: "pushover", Name: "Personal", ProjectID: testProj},
			{ID: 47, AppSlug: "pushover", Name: "On-call", ProjectID: testProj},
		},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "ping-ambiguous",
		"source": `export default async (event, context) => {
			try {
				await context.integration("pushover", "pushover_send_notification", { message: "hi" });
				return { unexpected: "succeeded" };
			} catch (e) {
				return { err: String(e.message || e) };
			}
		};`,
	})

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q)", res.Status, res.Error)
	}
	if !strings.Contains(res.Response, "multiple") || !strings.Contains(res.Response, "31") || !strings.Contains(res.Response, "47") {
		t.Errorf("expected ambiguous-match error with candidate ids, got %s", res.Response)
	}
}

// TestContextIntegrationUpstreamFailure: when the upstream tool returns
// success:false, the handler should see a thrown error (not a fake
// success response) so it can fall through to error handling.
func TestContextIntegrationUpstreamFailure(t *testing.T) {
	requireBin(t, "node")
	clearConnSlugCache()
	stub := &stubIntegrationPlatform{
		Connections: []sdk.PlatformConnection{
			{ID: 31, AppSlug: "pushover", Name: "Pushover", ProjectID: testProj},
		},
		result: &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":"invalid user"}`)},
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(stub))
	app := mountApp(t, ctx)

	fn := createFn(t, app, ctx, map[string]any{
		"name": "ping-failure",
		"source": `export default async (event, context) => {
			try {
				await context.integration("pushover", "pushover_send_notification", { message: "hi" });
				return { unexpected: "succeeded" };
			} catch (e) {
				return { err: String(e.message || e) };
			}
		};`,
	})

	res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok (err=%q)", res.Status, res.Error)
	}
	if !strings.Contains(res.Response, "non-success") || !strings.Contains(res.Response, "invalid user") {
		t.Errorf("expected upstream error surfaced, got %s", res.Response)
	}
}

// clearConnSlugCache wipes the resolver cache between tests so a
// multi-match test isn't shadowed by an earlier test's single-match
// entry. Test-only — the production code path relies on the 60s TTL.
func clearConnSlugCache() {
	connSlugCache.Lock()
	connSlugCache.m = map[string]connCacheEntry{}
	connSlugCache.Unlock()
}
