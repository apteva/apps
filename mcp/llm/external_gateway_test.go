package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// newAliasTestApp wires a gateway against a single fake OpenAI-compatible
// provider and returns the app, ctx, bearer token, and the last model string
// the provider actually received.
func newAliasTestApp(t *testing.T, handler http.HandlerFunc) (*App, *sdk.AppCtx, string, func() string) {
	t.Helper()
	var (
		mu        sync.Mutex
		lastModel string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		lastModel = strArg(body, "model")
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(upstream.Close)

	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL}); err != nil {
		t.Fatal(err)
	}
	token, err := createToken(ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "agent", "subject_id": "a"})
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, token["token"].(string), func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastModel
	}
}

func completionHandler(content string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"c1","model":"test","choices":[{"message":{"role":"assistant","content":"` + content +
			`"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20}}`))
	}
}

func chatRequest(t *testing.T, app *App, token, model string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	return rec
}

func TestAliasResolvesToBackendAndRecordsAlias(t *testing.T) {
	app, ctx, token, lastModel := newAliasTestApp(t, completionHandler("hello"))
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":   "apteva/fast",
		"targets": []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	rec := chatRequest(t, app, token, "apteva/fast")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The upstream must see its own model id, never the public alias.
	if got := lastModel(); got != "test" {
		t.Fatalf("provider received model %q, want %q", got, "test")
	}
	events, err := dbUsageEventsList(ctx.AppDB(), usageFilter{ProjectID: "proj-test"}, 10)
	if err != nil || len(events) == 0 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	if events[0].Alias != "apteva/fast" {
		t.Fatalf("usage event alias=%q, want apteva/fast", events[0].Alias)
	}
	if events[0].Provider != "openai" {
		t.Fatalf("usage event must keep the real backend for cost accounting, got %q", events[0].Provider)
	}
}

func TestAliasFailsOverToNextTarget(t *testing.T) {
	var calls int
	var mu sync.Mutex
	app, ctx, token, _ := newAliasTestApp(t, func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"upstream down"}`))
			return
		}
		completionHandler("recovered")(w, nil)
	})
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias": "apteva/fast",
		"targets": []any{
			map[string]any{"provider": "openai", "model": "primary"},
			map[string]any{"provider": "openai", "model": "secondary"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rec := chatRequest(t, app, token, "apteva/fast")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "recovered") {
		t.Fatalf("alias should fail over to its second target: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAliasPolicyIsEvaluatedOnTheAlias(t *testing.T) {
	app, ctx, token, _ := newAliasTestApp(t, completionHandler("hello"))
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":   "apteva/fast",
		"targets": []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	// A customer granted only the public catalog must still be served, and must
	// not be able to reach the underlying provider directly.
	if _, err := dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{
		"subject_type":   "agent",
		"subject_id":     "a",
		"allowed_models": []any{"apteva/*"},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := chatRequest(t, app, token, "apteva/fast"); rec.Code != http.StatusOK {
		t.Fatalf("aliased request should pass policy: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := chatRequest(t, app, token, "openai/test"); rec.Code != http.StatusForbidden {
		t.Fatalf("direct backend access should be denied: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestModelsListAdvertisesAliases(t *testing.T) {
	app, ctx, token, _ := newAliasTestApp(t, completionHandler("hello"))
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":        "apteva/fast",
		"display_name": "Apteva Fast",
		"targets":      []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, m := range out.Data {
		if m.ID == "apteva/fast" {
			if m.OwnedBy != "apteva" {
				t.Fatalf("alias should be owned_by apteva, got %q", m.OwnedBy)
			}
			return
		}
	}
	t.Fatalf("alias missing from /v1/models: %s", rec.Body.String())
}

func TestAliasNamespaceCannotShadowAProvider(t *testing.T) {
	_, ctx, _, _ := newAliasTestApp(t, completionHandler("hello"))
	// openai/... already means "route to OpenAI"; letting an alias claim it
	// would silently change what existing callers' model strings resolve to.
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":   "openai/fast",
		"targets": []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err == nil {
		t.Fatal("alias in a provider namespace should be rejected")
	}
}

func TestRequestsPerMinuteLimitReturns429(t *testing.T) {
	gatewayLimiter = newRateLimiter()
	app, ctx, token, _ := newAliasTestApp(t, completionHandler("hello"))
	if _, err := dbPolicySet(ctx.AppDB(), "proj-test", map[string]any{
		"subject_type": "agent",
		"subject_id":   "a",
		"limits":       map[string]any{"requests_per_minute": 1},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := chatRequest(t, app, token, "openai/test"); rec.Code != http.StatusOK {
		t.Fatalf("first request should pass: status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := chatRequest(t, app, token, "openai/test")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request should be rate limited: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("rate limited response must carry Retry-After")
	}
}

func TestRateLimitWindowRollsOver(t *testing.T) {
	limiter := newRateLimiter()
	base := time.Now()
	limiter.now = func() time.Time { return base }
	if err := limiter.reserveWindow(1, "requests_per_minute", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := limiter.reserveWindow(1, "requests_per_minute", 1, 1); err == nil {
		t.Fatal("second call in the same window should be rejected")
	}
	limiter.now = func() time.Time { return base.Add(rateWindowSize + time.Second) }
	if err := limiter.reserveWindow(1, "requests_per_minute", 1, 1); err != nil {
		t.Fatalf("window should have rolled over: %v", err)
	}
}

func TestZeroLimitMeansUnlimited(t *testing.T) {
	limiter := newRateLimiter()
	for i := 0; i < 100; i++ {
		if err := limiter.reserveWindow(1, "requests_per_minute", 1, 0); err != nil {
			t.Fatalf("a zero limit must not throttle: %v", err)
		}
	}
	if err := limiter.acquireSlot(1, 0); err != nil {
		t.Fatalf("a zero concurrency limit must not throttle: %v", err)
	}
}

func TestConcurrencySlotsAreReleased(t *testing.T) {
	limiter := newRateLimiter()
	if err := limiter.acquireSlot(7, 1); err != nil {
		t.Fatal(err)
	}
	if err := limiter.acquireSlot(7, 1); err == nil {
		t.Fatal("second concurrent request should be rejected")
	}
	limiter.releaseSlot(7, 1)
	if err := limiter.acquireSlot(7, 1); err != nil {
		t.Fatalf("slot should be reusable after release: %v", err)
	}
}

func TestTightestRateLimitsTakesTheStrictest(t *testing.T) {
	project := &Policy{Limits: Limits{RequestsPerMinute: 100, MaxConcurrentRequests: 0}}
	subject := &Policy{Limits: Limits{RequestsPerMinute: 10, MaxConcurrentRequests: 2}}
	requests, tokens, concurrent := tightestRateLimits([]*Policy{project, subject})
	if requests != 10 || concurrent != 2 || tokens != 0 {
		t.Fatalf("requests=%d tokens=%d concurrent=%d", requests, tokens, concurrent)
	}
}

func TestBillingPricesAgainstTheAlias(t *testing.T) {
	gatewayLimiter = newRateLimiter()
	app, ctx, token, _ := newAliasTestApp(t, completionHandler("hello"))
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":   "apteva/fast",
		"targets": []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	// 1 000 000 microunits per million tokens = 1 microunit per token.
	if _, err := dbModelPriceUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":                         "apteva/fast",
		"input_microunits_per_million":  int64(1_000_000),
		"output_microunits_per_million": int64(2_000_000),
	}); err != nil {
		t.Fatal(err)
	}
	if rec := chatRequest(t, app, token, "apteva/fast"); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	events, err := dbUsageEventsList(ctx.AppDB(), usageFilter{ProjectID: "proj-test"}, 10)
	if err != nil || len(events) == 0 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	// 10 input + 20 output at 1 and 2 microunits per token.
	if want := int64(10*1 + 20*2); events[0].BilledAmountMicrounits != want {
		t.Fatalf("billed=%d want=%d", events[0].BilledAmountMicrounits, want)
	}
	if events[0].BilledCurrency != "USD" || events[0].PriceVersion != 1 {
		t.Fatalf("currency=%q version=%d", events[0].BilledCurrency, events[0].PriceVersion)
	}
}

func TestUnpricedAliasStillMeters(t *testing.T) {
	gatewayLimiter = newRateLimiter()
	app, ctx, token, _ := newAliasTestApp(t, completionHandler("hello"))
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":   "apteva/fast",
		"targets": []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := chatRequest(t, app, token, "apteva/fast"); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	events, _ := dbUsageEventsList(ctx.AppDB(), usageFilter{ProjectID: "proj-test"}, 10)
	if len(events) == 0 || events[0].TotalTokens == 0 {
		t.Fatalf("metering must work before a price book exists: %+v", events)
	}
	if events[0].BilledAmountMicrounits != 0 {
		t.Fatalf("unpriced alias should bill zero, got %d", events[0].BilledAmountMicrounits)
	}
}

func TestPriceUpsertOpensANewVersion(t *testing.T) {
	_, ctx, _, _ := newAliasTestApp(t, completionHandler("hello"))
	first, err := dbModelPriceUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias": "apteva/fast", "input_microunits_per_million": int64(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := dbModelPriceUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias": "apteva/fast", "input_microunits_per_million": int64(2000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.PriceVersion != 1 || second.PriceVersion != 2 {
		t.Fatalf("versions: first=%d second=%d", first.PriceVersion, second.PriceVersion)
	}
	active, err := dbModelPriceResolve(ctx.AppDB(), "proj-test", "apteva/fast")
	if err != nil || active == nil || active.InputMicrounitsPerMillion != 2000 {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	// History must survive so past invoices stay reproducible.
	history, err := dbModelPricesList(ctx.AppDB(), "proj-test", true)
	if err != nil || len(history) != 2 {
		t.Fatalf("history=%d err=%v", len(history), err)
	}
}

func TestCalculateBilledAmountRounding(t *testing.T) {
	price := &ModelPrice{InputMicrounitsPerMillion: 3, OutputMicrounitsPerMillion: 0}
	// 1 token at 3 microunits/million rounds to zero only if we truncate; the
	// half-up rule keeps tiny amounts from silently becoming free.
	if got := calculateBilledAmount(price, 200_000, 0); got != 1 {
		t.Fatalf("got=%d want=1", got)
	}
	withMinimum := &ModelPrice{InputMicrounitsPerMillion: 1, MinimumChargeMicrounits: 50}
	if got := calculateBilledAmount(withMinimum, 1, 0); got != 50 {
		t.Fatalf("minimum charge not applied: got=%d", got)
	}
	if got := calculateBilledAmount(nil, 10, 10); got != 0 {
		t.Fatalf("nil price should bill zero, got %d", got)
	}
}

func TestAliasDeprecationKeepsHistory(t *testing.T) {
	_, ctx, _, _ := newAliasTestApp(t, completionHandler("hello"))
	if _, err := dbModelAliasUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"alias":   "apteva/fast",
		"targets": []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dbModelAliasDelete(ctx.AppDB(), "proj-test", "apteva/fast"); err != nil {
		t.Fatal(err)
	}
	resolved, err := dbModelAliasResolve(ctx.AppDB(), "proj-test", "apteva/fast")
	if err != nil || resolved != nil {
		t.Fatalf("deprecated alias should not resolve: %+v err=%v", resolved, err)
	}
	all, err := dbModelAliasList(ctx.AppDB(), "proj-test", true)
	if err != nil || len(all) != 1 || all[0].Status != "deprecated" {
		t.Fatalf("alias row should be retained as deprecated: %+v err=%v", all, err)
	}
}

// TestUpgradeFromV05SchemaPreservesUsage exercises the path a live v0.5.x
// database takes: the pre-0.6 migrations, then the new migration, then the
// column-aware repair. Existing rows must survive and gain safe defaults.
func TestUpgradeFromV05SchemaPreservesUsage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{
		"migrations/001_init.sql",
		"migrations/002_provider_models.sql",
		"migrations/003_subject_policy_usage_idempotency.sql",
		"migrations/004_gateway_hardening.sql",
		"migrations/005_provider_costs.sql",
	} {
		schema, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if err := ensureGatewaySchema(db); err != nil {
		t.Fatalf("pre-0.6 repair: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO usage_events(project_id,subject_type,subject_id,provider,model,request_tokens,response_tokens,total_tokens,request_id,period,status)
		VALUES ('p','agent','a','openai','openai/test',10,20,30,'req-1','2026-09','completed')`); err != nil {
		t.Fatal(err)
	}

	upgrade, err := os.ReadFile("migrations/006_external_gateway.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(upgrade)); err != nil {
		t.Fatalf("006: %v", err)
	}
	if err := ensureGatewaySchema(db); err != nil {
		t.Fatalf("0.6 repair: %v", err)
	}
	if err := ensureGatewaySchema(db); err != nil {
		t.Fatalf("idempotent repair: %v", err)
	}

	for _, col := range []string{"alias", "billed_amount_microunits", "billed_currency", "price_version"} {
		has, err := txTableHasColumn(db, "usage_events", col)
		if err != nil || !has {
			t.Fatalf("usage_events.%s missing after upgrade: %v", col, err)
		}
	}
	// The pre-existing row must still be intact and default to unbilled.
	var (
		tokens int64
		alias  string
		billed int64
	)
	if err := db.QueryRow(`SELECT total_tokens, alias, billed_amount_microunits FROM usage_events WHERE request_id='req-1'`).
		Scan(&tokens, &alias, &billed); err != nil {
		t.Fatal(err)
	}
	if tokens != 30 || alias != "" || billed != 0 {
		t.Fatalf("tokens=%d alias=%q billed=%d", tokens, alias, billed)
	}
	// And the new tables must be usable immediately.
	if _, err := dbModelAliasUpsert(db, "p", map[string]any{
		"alias": "apteva/fast", "targets": []any{map[string]any{"provider": "openai", "model": "test"}},
	}); err != nil {
		t.Fatalf("alias upsert after upgrade: %v", err)
	}
}
