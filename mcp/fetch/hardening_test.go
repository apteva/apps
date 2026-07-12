package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRedirectStripsCredentialsAcrossOrigins(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_loopback": "true"})
	app := &App{}
	received := make(chan http.Header, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	if _, err := app.toolFetchRequest(ctx, map[string]any{
		"method": "GET",
		"url":    source.URL,
		"headers": map[string]any{
			"X-API-Key":     "redirect-secret",
			"X-Custom-Auth": "also-secret",
			"Accept":        "application/json",
		},
		"save_history": false,
	}); err != nil {
		t.Fatal(err)
	}
	headers := <-received
	if headers.Get("X-API-Key") != "" || headers.Get("X-Custom-Auth") != "" {
		t.Fatalf("cross-origin credentials leaked: %#v", headers)
	}
	if headers.Get("Accept") != "application/json" {
		t.Fatalf("safe Accept header was not preserved: %#v", headers)
	}
}

func TestTransportIgnoresEnvironmentProxy(t *testing.T) {
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	ctx := newFetchCtx(t, map[string]string{"allow_loopback": "true"})
	_, err := (&App{}).toolFetchRequest(ctx, map[string]any{
		"method":       "GET",
		"url":          "http://fetch-review.invalid/",
		"timeout_ms":   500,
		"save_history": false,
	})
	if err == nil {
		t.Fatal("expected direct DNS request to fail")
	}
	if hits.Load() != 0 {
		t.Fatalf("environment proxy was used %d time(s)", hits.Load())
	}
}

func TestMetadataAndSpecialUseRangesStayBlocked(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_private_networks": "true", "allow_loopback": "true"})
	for _, raw := range []string{"169.254.169.254", "fd00:ec2::254", "192.0.2.1", "240.0.0.1"} {
		if err := validateIPPolicy(ctx, netip.MustParseAddr(raw)); err == nil {
			t.Errorf("address %s should always be blocked", raw)
		}
	}
	if err := validateIPPolicy(ctx, netip.MustParseAddr("10.0.0.1")); err != nil {
		t.Fatalf("explicitly allowed private address rejected: %v", err)
	}
}

func TestHistoryRedactsURLCredentials(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_loopback": "true"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	out, err := (&App{}).toolFetchRequest(ctx, map[string]any{
		"method": "GET",
		"url":    strings.Replace(server.URL, "http://", "http://user:password@", 1) + "/?api_key=query-secret&safe=yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(*FetchResult)
	if strings.Contains(result.FinalURL, "password") || strings.Contains(result.FinalURL, "query-secret") {
		t.Fatalf("result URL leaked credentials: %s", result.FinalURL)
	}
	var stored string
	if err := ctx.AppDB().QueryRow(`SELECT url FROM fetch_history WHERE id=?`, result.HistoryID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "password") || strings.Contains(stored, "query-secret") || !strings.Contains(stored, "safe=yes") {
		t.Fatalf("history URL not safely redacted: %s", stored)
	}
}

func TestHistoryRedactsNonstandardCredentialHeaders(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_loopback": "true"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	out, err := (&App{}).toolFetchRequest(ctx, map[string]any{
		"method": "GET", "url": server.URL,
		"headers": map[string]any{"X-Custom-Auth": "literal-secret", "Accept": "application/json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var requestJSON string
	if err := ctx.AppDB().QueryRow(`SELECT redacted_request_json FROM fetch_history WHERE id=?`, out.(*FetchResult).HistoryID).Scan(&requestJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(requestJSON, "literal-secret") || !strings.Contains(requestJSON, "[redacted]") {
		t.Fatalf("credential header leaked into history: %s", requestJSON)
	}
}

func TestEnvironmentSecretsEncryptedAndListingDoesNotDeadlock(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	codec := codecForTest(t, ctx)
	env, err := dbEnvironmentCreate(ctx.AppDB(), "test-proj", &Environment{
		Name: "Production",
		Vars: []EnvironmentVar{{Key: "API_TOKEN", Value: "plaintext-secret", IsSecret: true}},
	}, codec)
	if err != nil {
		t.Fatal(err)
	}
	var value, encrypted string
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(value,''), COALESCE(value_encrypted,'') FROM fetch_environment_vars WHERE environment_id=?`, env.ID).Scan(&value, &encrypted); err != nil {
		t.Fatal(err)
	}
	if value != "" || encrypted == "" || strings.Contains(encrypted, "plaintext-secret") {
		t.Fatalf("secret not encrypted at rest: value=%q encrypted=%q", value, encrypted)
	}
	done := make(chan error, 1)
	go func() {
		list, err := dbEnvironmentList(ctx.AppDB(), "test-proj", codec)
		if err == nil && (len(list) != 1 || len(list[0].Vars) != 1 || list[0].Vars[0].Value != "" || !list[0].Vars[0].HasValue) {
			err = io.ErrUnexpectedEOF
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("environment listing deadlocked")
	}
}

func TestRenamedMaskedEnvironmentSecretRequiresValue(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	codec := codecForTest(t, ctx)
	env, err := dbEnvironmentCreate(ctx.AppDB(), "test-proj", &Environment{
		Name: "Production",
		Vars: []EnvironmentVar{{Key: "API_TOKEN", Value: "plaintext-secret", IsSecret: true}},
	}, codec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbEnvironmentUpdate(ctx.AppDB(), "test-proj", env.ID, map[string]any{
		"vars": []any{map[string]any{
			"key": "RENAMED_TOKEN", "value": secretMask, "is_secret": true, "has_value": true,
		}},
	}, codec)
	if err == nil || !strings.Contains(err.Error(), "renamed without a replacement value") {
		t.Fatalf("expected renamed secret error, got %v", err)
	}
}

func TestSavedRequestSecretsEncryptedMaskedAndPreserved(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	codec := codecForTest(t, ctx)
	saved, err := dbSavedCreate(ctx.AppDB(), "test-proj", &SavedRequest{
		Name:        "Authenticated",
		Method:      "GET",
		URLTemplate: "https://example.com/resource",
		Headers:     json.RawMessage(`{"Authorization":"Bearer literal-secret","Accept":"application/json"}`),
		Query:       json.RawMessage(`{"api_key":"query-secret","page":"1"}`),
		Body:        json.RawMessage(`{"json":{"password":"body-secret","name":"Alice"}}`),
	}, codec)
	if err != nil {
		t.Fatal(err)
	}
	var rawHeaders, rawQuery, rawBody string
	if err := ctx.AppDB().QueryRow(`SELECT headers_json, query_json, body_json FROM fetch_saved_requests WHERE id=?`, saved.ID).Scan(&rawHeaders, &rawQuery, &rawBody); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{rawHeaders, rawQuery, rawBody} {
		if strings.Contains(raw, "literal-secret") || strings.Contains(raw, "query-secret") || strings.Contains(raw, "body-secret") {
			t.Fatalf("saved request leaked plaintext: %s", raw)
		}
	}
	if !strings.Contains(string(saved.Headers), secretMask) || !strings.Contains(string(saved.Query), secretMask) || !strings.Contains(string(saved.Body), secretMask) {
		t.Fatalf("public request is not masked: %+v", saved)
	}
	updated, err := dbSavedUpdate(ctx.AppDB(), "test-proj", saved.ID, map[string]any{
		"headers": map[string]any{"Authorization": secretMask, "Accept": "text/plain"},
	}, codec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated.Headers), secretMask) {
		t.Fatalf("masked secret disappeared after update: %s", updated.Headers)
	}
	internal, err := dbSavedGet(ctx.AppDB(), "test-proj", saved.ID, codec, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(internal.Headers), "literal-secret") || strings.Contains(string(internal.Headers), secretMask) {
		t.Fatalf("secret was not preserved internally: %s", internal.Headers)
	}
}

func TestSavedURLRejectsLiteralSensitiveQuery(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	codec := codecForTest(t, ctx)
	_, err := dbSavedCreate(ctx.AppDB(), "test-proj", &SavedRequest{
		Name: "Unsafe", Method: "GET", URLTemplate: "https://example.com/?api_key=literal",
	}, codec)
	if err == nil || !strings.Contains(err.Error(), "environment variable") {
		t.Fatalf("expected sensitive saved URL rejection, got %v", err)
	}
}

func TestLegacyMigrationEncryptsAndScrubsStoredSecrets(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	codec := codecForTest(t, ctx)
	result, err := ctx.AppDB().Exec(`INSERT INTO fetch_environments(project_id, slug, name) VALUES ('test-proj', 'legacy', 'Legacy')`)
	if err != nil {
		t.Fatal(err)
	}
	envID, _ := result.LastInsertId()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO fetch_environment_vars(project_id, environment_id, key, value, is_secret) VALUES ('test-proj', ?, 'API_TOKEN', 'legacy-secret', 1)`, envID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO fetch_history(project_id, source, method, url, redacted_request_json, redacted_response_json, error)
		 VALUES ('test-proj', 'agent', 'GET', 'https://user:pass@example.com/?api_key=legacy-query',
		 '{"headers":{"X-Custom-Auth":"legacy-query"}}', '{}', 'request legacy-query failed')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacySecrets(ctx.AppDB(), codec, ctx.Logger()); err != nil {
		t.Fatal(err)
	}
	var value, encrypted, historyURL, requestJSON, runError string
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(value,''), COALESCE(value_encrypted,'') FROM fetch_environment_vars WHERE environment_id=?`, envID).Scan(&value, &encrypted); err != nil {
		t.Fatal(err)
	}
	if value != "" || encrypted == "" {
		t.Fatalf("legacy environment secret not encrypted: value=%q encrypted=%q", value, encrypted)
	}
	if err := ctx.AppDB().QueryRow(`SELECT url, redacted_request_json, error FROM fetch_history LIMIT 1`).Scan(&historyURL, &requestJSON, &runError); err != nil {
		t.Fatal(err)
	}
	for label, stored := range map[string]string{"url": historyURL, "request": requestJSON, "error": runError} {
		if strings.Contains(stored, "pass") || strings.Contains(stored, "legacy-query") {
			t.Fatalf("legacy %s was not scrubbed: %s", label, stored)
		}
	}
}

func TestRequestAndHTTPInputLimits(t *testing.T) {
	if _, _, err := buildBody(map[string]any{"text": strings.Repeat("x", maxRequestBodyBytes+1)}, nil); err == nil {
		t.Fatal("oversized outbound body accepted")
	}
	newFetchCtx(t, nil)
	server := newHTTPServer(t)
	defer server.Close()
	response, err := http.Post(server.URL+"/execute?project_id=test-proj", "application/json", bytes.NewBufferString(`{"padding":"`+strings.Repeat("x", maxInboundJSONBytes)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
}

func TestRequestMetadataLimits(t *testing.T) {
	ctx := newFetchCtx(t, nil)
	codec := codecForTest(t, ctx)
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "url",
			args: map[string]any{"url": "https://example.com/" + strings.Repeat("x", maxURLBytes)},
			want: "url exceeds",
		},
		{
			name: "query",
			args: map[string]any{"url": "https://example.com", "query": map[string]any{"q": strings.Repeat("x", maxQueryBytes)}},
			want: "query exceeds",
		},
		{
			name: "headers",
			args: map[string]any{"url": "https://example.com", "headers": map[string]any{"X-Large": strings.Repeat("x", maxHeaderBytes)}},
			want: "headers exceed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := normalizeRequestSpec(ctx, "test-proj", test.args, codec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}

func TestHistoryRetention(t *testing.T) {
	ctx := newFetchCtx(t, map[string]string{"allow_loopback": "true", "history_max_entries": "3"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	app := &App{}
	for i := 0; i < 5; i++ {
		if _, err := app.toolFetchRequest(ctx, map[string]any{"method": "GET", "url": server.URL}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM fetch_history WHERE project_id='test-proj'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("history count=%d, want 3", count)
	}
}
