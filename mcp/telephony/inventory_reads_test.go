package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type inventoryTestPlatform struct {
	*answerPlatform
	mu                     sync.Mutex
	delay                  time.Duration
	counts                 map[string]int
	credentialReads        int
	active, peak, canceled int
	failure                string
	appGate                chan struct{}
	appStarted             chan struct{}
	apps                   map[string]map[string]any
	numbers                []map[string]any
}

func (p *inventoryTestPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.credentialReads++
	return p.answerPlatform.GetConnectionCredentials(id)
}
func (p *inventoryTestPlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	return p.ExecuteIntegrationToolContext(context.Background(), id, tool, args)
}
func (p *inventoryTestPlatform) ExecuteIntegrationToolContext(request context.Context, conn int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	id := stringValue(args["id"])
	p.mu.Lock()
	p.counts[fmt.Sprintf("%d:%s:%s", conn, tool, id)]++
	p.active++
	if p.active > p.peak {
		p.peak = p.active
	}
	delay, failure, gate, started := p.delay, p.failure, p.appGate, p.appStarted
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	if tool == "get_call_control_application" && gate != nil {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		select {
		case <-gate:
		case <-request.Done():
			p.mu.Lock()
			p.canceled++
			p.mu.Unlock()
			return nil, request.Err()
		}
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-request.Done():
			p.mu.Lock()
			p.canceled++
			p.mu.Unlock()
			return nil, request.Err()
		}
	}
	if tool == "get_call_control_application" && id == failure {
		return nil, errors.New("application unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var value any
	switch tool {
	case "list_phone_numbers":
		value = p.numbers
	case "list_outbound_voice_profiles":
		value = []any{map[string]any{"id": "profile-default", "enabled": true, "name": "Default"}}
	case "get_call_control_application":
		value = p.apps[id]
	case "update_call_control_application":
		p.apps[id]["outbound"] = args["outbound"]
		value = p.apps[id]
	default:
		value = map[string]any{}
	}
	raw, err := json.Marshal(map[string]any{"data": value})
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}, err
}
func inventoryFixture(t testing.TB, delay time.Duration) (*App, *sdk.AppCtx, *inventoryTestPlatform) {
	t.Helper()
	t.Setenv("APTEVA_PUBLIC_URL", "https://127.0.0.1")
	base := multiCarrierPlatform()
	base.bindings["carrier"] = float64(10)
	base.credentialsByID[10].Fields["public_key"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	p := &inventoryTestPlatform{answerPlatform: base, delay: delay, counts: map[string]int{}, apps: map[string]map[string]any{}}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatal(file, err)
		}
	}
	manifest, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	ctx := sdk.NewAppCtxForTest(manifest, db, sdk.Config{}, p, nil).WithProject("project-a")
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	a := &App{installID: 42}
	for i := 0; i < 11; i++ {
		id, phone := fmt.Sprintf("application-%02d", i), fmt.Sprintf("+33189313%03d", i)
		p.numbers = append(p.numbers, map[string]any{"id": fmt.Sprintf("number-%d", i), "phone_number": phone, "connection_id": id, "features": []string{"voice"}})
		app := map[string]any{"id": id, "active": true, "outbound": map[string]any{"outbound_voice_profile_id": "profile-default"}}
		if i < 10 {
			config, _ := json.Marshal(telnyxRouteConfig{ApplicationID: id})
			route := routeRow{ID: fmt.Sprintf("route-%d", i), ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 10, PhoneNumber: phone, PhoneNumberSID: fmt.Sprintf("number-%d", i), Enabled: true, PreviousVoiceURL: string(config), Secret: "test-secret", AnswerMode: answerModeHumanBrowser}
			if err := a.db().insertRoute(route); err != nil {
				t.Fatal(err)
			}
			app["webhook_event_url"] = a.inboundRouteURL(route)
		}
		p.apps[id] = app
	}
	return a, ctx, p
}
func (p *inventoryTestPlatform) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, v := range p.counts {
		n += v
	}
	return n
}
func inventoryViews(t testing.TB, a *App, ctx *sdk.AppCtx, request context.Context) []connectedNumberView {
	t.Helper()
	result, err := a.connectedNumbers(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	return result["numbers"].([]connectedNumberView)
}

// Reconstruct the previous carrier-check path for an equal-response baseline:
// one list, ten webhook application reads, one profile list, eleven outbound reads.
func legacyInventoryChecks(t testing.TB, a *App, ctx *sdk.AppCtx) []connectedNumberView {
	t.Helper()
	provider, err := a.numberProviderFor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := listOwnedCarrierNumbers(ctx, provider)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := a.db().listRoutesForProjectConnection("project-a", provider.ConnID)
	if err != nil {
		t.Fatal(err)
	}
	byPhone := map[string]*routeRow{}
	for i := range routes {
		byPhone[routes[i].PhoneNumber] = &routes[i]
	}
	views := []connectedNumberView{}
	for _, n := range owned {
		views = append(views, a.connectedNumberView(ctx, provider.Slug, n, byPhone[n.PhoneNumber], func(int64) string { return "" }))
	}
	profiles, err := listTelnyxOutboundProfiles(ctx, provider.ConnID)
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range owned {
		views[i].Outbound, err = a.telnyxOutboundReadiness(ctx, provider.ConnID, telnyxRouteApplicationID(byPhone[n.PhoneNumber], n.ConnectionID), profiles)
		if err != nil {
			t.Fatal(err)
		}
	}
	return views
}
func TestInventoryReadsDeduplicateAndPreserveChecks(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	before := legacyInventoryChecks(t, a, ctx)
	if p.total() != 23 {
		t.Fatalf("legacy requests=%d", p.total())
	}
	p.mu.Lock()
	p.counts = map[string]int{}
	p.credentialReads = 0
	p.mu.Unlock()
	after := inventoryViews(t, a, ctx, context.Background())
	if p.total() != 13 {
		t.Fatalf("requests=%d want 13", p.total())
	}
	if p.credentialReads != 1 {
		t.Fatalf("credentials read %d times", p.credentialReads)
	}
	for i := range before {
		// Admission controls are added after carrier checks; compare their unchanged contract.
		before[i].OutboundEnabled = true
		before[i].OutboundNumberEnabled = true
		before[i].CarrierConnectionID = 10
		if after[i].VerifiedAt == "" {
			t.Fatal("verification time missing")
		}
		after[i].VerifiedAt = ""
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("changed results:\nbefore=%+v\nafter=%+v", before, after)
	}
}
func TestInventoryReadsCacheFreshFailureAndCredentials(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	first := inventoryViews(t, a, ctx, context.Background())
	second := inventoryViews(t, a, ctx, context.Background())
	if p.total() != 13 || !reflect.DeepEqual(first, second) {
		t.Fatal("cached inventory changed or refetched")
	}
	p.mu.Lock()
	p.failure = "application-03"
	p.mu.Unlock()
	failed := inventoryViews(t, a, ctx, inventoryFreshContext(context.Background(), true))
	for _, v := range failed {
		if v.Outbound.ApplicationID == "application-03" {
			if v.Outbound.Status != outboundConfigError || v.RoutingHealth != "unverified" {
				t.Fatalf("failure became healthy: %+v", v)
			}
		} else if v.Outbound.Status != outboundReady {
			t.Fatalf("other number affected: %+v", v)
		}
	}
	// A failed fresh check must replace, rather than leave an older healthy cache.
	again := inventoryViews(t, a, ctx, context.Background())
	for _, v := range again {
		if v.Outbound.ApplicationID == "application-03" && v.Outbound.Status != outboundConfigError {
			t.Fatal("old healthy cache survived failed refresh")
		}
	}
	p.mu.Lock()
	p.failure = ""
	p.credentialsByID[10].Fields["public_key"] = "invalid"
	p.mu.Unlock()
	badKey := inventoryViews(t, a, ctx, context.Background())
	for _, v := range badKey {
		if v.Route != nil && v.RoutingHealth == "healthy" {
			t.Fatal("cached healthy signing key")
		}
	}
}
func TestInventoryReadsBoundConcurrencyAndCancellation(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 10*time.Millisecond)
	inventoryViews(t, a, ctx, context.Background())
	if p.peak != 4 {
		t.Fatalf("peak=%d want 4", p.peak)
	}
	a.inventoryReads.invalidate()
	p.mu.Lock()
	p.delay = 0
	p.appGate = make(chan struct{})
	p.appStarted = make(chan struct{}, 11)
	p.mu.Unlock()
	request, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = a.connectedNumbers(ctx, request) }()
	<-p.appStarted
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("inventory did not cancel")
	}
	deadline := time.After(time.Second)
	for {
		p.mu.Lock()
		active, canceled := p.active, p.canceled
		p.mu.Unlock()
		if active == 0 && canceled > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("outstanding reads not canceled")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
func TestInventoryReadsShareConcurrentRefresh(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 20*time.Millisecond)
	const readers = 12
	start := make(chan struct{})
	freshRequest := inventoryFreshContext(context.Background(), true)
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := a.connectedNumbers(ctx, freshRequest)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := p.total(); got != 13 {
		t.Fatalf("simultaneous requests=%d want 13", got)
	}
}
func TestInventoryReadsScopeAndLocalChanges(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	inventoryViews(t, a, ctx, context.Background())
	other := inventoryViews(t, a, ctx.WithProject("project-b"), context.Background())
	if p.total() != 26 {
		t.Fatal("cache crossed project")
	}
	for _, v := range other {
		if v.Route != nil {
			t.Fatal("project route leaked")
		}
	}
	route, _ := a.db().findRoute("route-0")
	if _, err := a.setInboundRouteEnabled(route, false); err != nil {
		t.Fatal(err)
	}
	views := inventoryViews(t, a, ctx, context.Background())
	found := false
	for _, v := range views {
		if v.PhoneNumber == route.PhoneNumber {
			found = true
			if v.Route.Enabled || v.RouteStatus != "disabled" {
				t.Fatal("route toggle not applied")
			}
		}
	}
	if !found || views[0].Route == nil || !views[0].Route.Enabled {
		t.Fatal("route ordering not applied")
	}
	p.mu.Lock()
	count := p.totalUnsafe()
	p.mu.Unlock()
	if _, err := a.outboundPolicy(ctx, map[string]any{"scope": "number", "value": route.PhoneNumber, "enabled": false}); err != nil {
		t.Fatal(err)
	}
	for _, v := range inventoryViews(t, a, ctx, context.Background()) {
		if v.PhoneNumber == route.PhoneNumber && v.OutboundEnabled {
			t.Fatal("cached inventory bypassed disabled number")
		}
	}
	if p.total() <= count {
		t.Fatal("policy mutation did not invalidate")
	}
	// Exercise the existing browser permission filter after a warmed carrier cache.
	for _, allowed := range []string{views[0].PhoneNumber, views[1].PhoneNumber} {
		r := httptest.NewRequest("GET", "/softphone/numbers", nil)
		r = r.WithContext(context.WithValue(r.Context(), phonePrincipalKey{}, &phonePrincipal{Project: "project-a", Numbers: map[string]bool{allowed: true}}))
		w := httptest.NewRecorder()
		a.handleSoftphoneNumbers(w, r, "project-a")
		var result struct {
			Numbers []connectedNumberView `json:"numbers"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Numbers) != 1 || result.Numbers[0].PhoneNumber != allowed {
			t.Fatalf("permission cache leak: %s", w.Body.String())
		}
	}
}
func (p *inventoryTestPlatform) totalUnsafe() int {
	n := 0
	for _, v := range p.counts {
		n += v
	}
	return n
}
func TestInventoryScopeIncludesAllAuthorizationAndConfiguration(t *testing.T) {
	provider := &numberProvider{Slug: "telnyx", ConnID: 10, Fields: map[string]string{"api_key": "a"}}
	bindings := []*sdk.BoundIntegration{{ConnectionID: 10, AppSlug: "telnyx", ToolFor: func(s string) string { return s }}}
	base, err := inventoryScope(1, "p", provider, nil, bindings)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{}
	k, _ := inventoryScope(2, "p", provider, nil, bindings)
	cases = append(cases, k)
	k, _ = inventoryScope(1, "q", provider, nil, bindings)
	cases = append(cases, k)
	copy := *provider
	copy.ConnID = 11
	k, _ = inventoryScope(1, "p", &copy, nil, bindings)
	cases = append(cases, k)
	copy = *provider
	copy.Fields = map[string]string{"api_key": "b"}
	k, _ = inventoryScope(1, "p", &copy, nil, bindings)
	cases = append(cases, k)
	k, _ = inventoryScope(1, "p", provider, []routeRow{{ID: "changed"}}, bindings)
	cases = append(cases, k)
	k, _ = inventoryScope(1, "p", provider, nil, nil)
	cases = append(cases, k)
	for _, key := range cases {
		if key == base {
			t.Fatal("scope collision")
		}
	}
}
func TestInventoryCacheGlobalBoundInvalidationAndTimeout(t *testing.T) {
	var cache inventoryReadCache
	request, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	gate := make(chan struct{})
	started := make(chan struct{}, 100)
	var active, peak atomic.Int32
	fetch := func(work context.Context) ([]byte, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-gate:
			return []byte("old"), nil
		case <-work.Done():
			return nil, work.Err()
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); cache.read(request, fmt.Sprint(i), false, fetch) }(i)
	}
	for i := 0; i < inventoryReadConcurrency; i++ {
		<-started
	}
	if peak.Load() > inventoryReadConcurrency {
		t.Fatal("global bound exceeded")
	}
	deadline := time.After(time.Second)
	for {
		cache.mu.Lock()
		n := len(cache.flights)
		cache.mu.Unlock()
		if n == 60 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("requests did not join")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cache.invalidate()
	close(gate)
	wg.Wait()
	if len(cache.entries) != 0 {
		t.Fatal("late completion restored invalidated entries")
	}
	var calls int
	read := func(context.Context) ([]byte, error) { calls++; return []byte("new"), nil }
	cache.read(request, "0", false, read)
	if calls != 1 {
		t.Fatal("old flight restored cache")
	}
	cache.mu.Lock()
	v := cache.entries["0"]
	v.at = time.Now().Add(-inventoryCacheTTL)
	cache.entries["0"] = v
	cache.mu.Unlock()
	cache.read(request, "0", false, read)
	if calls != 2 {
		t.Fatal("expired cache reused")
	}
}
func TestInventoryCacheSharedReaderCancellation(t *testing.T) {
	var cache inventoryReadCache
	leader, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := make(chan struct{})
	started := make(chan struct{})
	fetch := func(work context.Context) ([]byte, error) {
		close(started)
		select {
		case <-gate:
			return []byte("ready"), nil
		case <-work.Done():
			return nil, work.Err()
		}
	}
	first := make(chan inventoryReadResult, 1)
	go func() { first <- cache.read(leader, "k", false, fetch) }()
	<-started
	second := make(chan inventoryReadResult, 1)
	go func() { second <- cache.read(context.Background(), "k", false, fetch) }()
	deadline := time.After(time.Second)
	for {
		cache.mu.Lock()
		n := cache.flights["k"].waiters
		cache.mu.Unlock()
		if n == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second reader did not join")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if !errors.Is((<-first).err, context.Canceled) {
		t.Fatal("leader not canceled")
	}
	close(gate)
	if v := <-second; v.err != nil || string(v.raw) != "ready" {
		t.Fatal("other reader interrupted", v.err)
	}
}
func BenchmarkNumberInventory(b *testing.B) {
	for _, mode := range []string{"legacy_200ms", "cold_200ms", "cached", "concurrent_cold_12"} {
		b.Run(mode, func(b *testing.B) {
			a, ctx, p := inventoryFixture(b, 200*time.Millisecond)
			if mode == "cached" {
				inventoryViews(b, a, ctx, context.Background())
			}
			p.mu.Lock()
			p.counts = map[string]int{}
			p.mu.Unlock()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				switch mode {
				case "legacy_200ms":
					legacyInventoryChecks(b, a, ctx)
				case "cold_200ms":
					a.inventoryReads.invalidate()
					inventoryViews(b, a, ctx, context.Background())
				case "cached":
					inventoryViews(b, a, ctx, context.Background())
				default:
					a.inventoryReads.invalidate()
					var wg sync.WaitGroup
					start := make(chan struct{})
					errs := make(chan error, 12)
					for j := 0; j < 12; j++ {
						wg.Add(1)
						go func() { defer wg.Done(); <-start; _, err := a.connectedNumbers(ctx, context.Background()); errs <- err }()
					}
					close(start)
					wg.Wait()
					close(errs)
					for err := range errs {
						if err != nil {
							b.Fatal(err)
						}
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(p.total())/float64(b.N), "carrier_reads/op")
		})
	}
}

// Ensure the response never contains request credentials or cache keys.
func TestInventoryResponseDoesNotExposeCredentials(t *testing.T) {
	a, ctx, p := inventoryFixture(t, 0)
	p.credentialsByID[10].Fields["api_key"] = "secret-inventory-key"
	r, err := a.connectedNumbers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "secret-inventory-key") {
		t.Fatal("secret leaked")
	}
}
