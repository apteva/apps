package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicGraphQLObservationSpansAuthenticationAndLogsOnce(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Millisecond)
		fmt.Fprint(w, `{"org":"default","user":{"id":42},"authorization":{"permissions":[]}}`)
	}))
	defer auth.Close()
	t.Setenv("APTEVA_GATEWAY_URL", auth.URL)
	a := secureTestApp(t, &trustedPlatform{})
	if _, err := setSecurity(a.ctx.AppDB(), "p1", "default", testSecurityPolicy()); err != nil {
		t.Fatal(err)
	}
	schema, _, err := createSchemaForAPI(a.ctx.AppDB(), "p1", "default", "production", `type Query { value: Int }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishSchemaForAPI(a.ctx.AppDB(), "p1", "default", "production", schema.Version); err != nil {
		t.Fatal(err)
	}
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Minute).Unix()))) + ".signature"
	request := httptest.NewRequest("POST", "/public/graphql/default", strings.NewReader(`{"query":"query Healthy { __typename }"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.handlePublicGraphQL(w, request)
	if w.Code != 200 {
		t.Fatalf("public response: %d %s", w.Code, w.Body.String())
	}
	a.stopRequestLogger()
	rows, err := publicLogsFiltered(a.ctx.AppReadDB(), "p1", defaultLogFilter())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one log despite nested handlers: %v", rows)
	}
	row := rows[0]
	phases := row["timings"].(map[string]any)
	if row["request_id"] != w.Header().Get("X-Request-ID") || row["operation_name"] != "Healthy" || row["environment"] != "production" || row["authorization_scope"] != "apteva:auth:default:42" || row["response_bytes"] != w.Body.Len() || row["duration_ms"].(int64) < 3 || phases["auth"].(float64) < 3 {
		t.Fatalf("incomplete observation: %v", row)
	}
	if strings.Contains(fmt.Sprint(row), token) {
		t.Fatal("request token retained")
	}
}
