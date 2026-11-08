package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The gateway uses server-owned caller scope and event source metadata, like
// production. MQTT and Devices are real, separately built sidecars.
func TestSidecarProvisionWithRealMQTT(t *testing.T) {
	if testing.Short() {
		t.Skip("real sidecars")
	}
	for _, secure := range []bool{false, true} {
		name := "local"
		if secure {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) { testSidecarMQTT(t, secure) })
	}
}
func testSidecarMQTT(t *testing.T, secure bool) {
	if testing.Short() {
		t.Skip("real sidecar integration")
	}
	const scope = "project-integration"
	var mu sync.RWMutex
	var broker, devices *tk.Sidecar
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		b, d := broker, devices
		mu.RUnlock()
		if r.URL.Path == "/api/apps/callback/apps/mqtt/call" {
			if b == nil {
				http.Error(w, "broker unavailable", 503)
				return
			}
			var body struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": body.Tool, "arguments": body.Input}})
			req, _ := http.NewRequestWithContext(r.Context(), "POST", b.URL()+"/mcp", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+b.Token())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Apteva-Project-ID", scope)
			req.Header.Set(sdk.HeaderBoundCallerAppName, "devices")
			req.Header.Set(sdk.HeaderBoundCallerInstallID, "2")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
		if r.URL.Path == "/api/app-events/internal/emit" {
			var body struct {
				Topic     string         `json:"topic"`
				ProjectID string         `json:"project_id"`
				Data      map[string]any `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid event", 400)
				return
			}
			if strings.HasPrefix(body.Topic, "mqtt.") && d != nil && b != nil && r.Header.Get("Authorization") == "Bearer "+b.Token() {
				raw, _ := json.Marshal(sdk.Event{Event: body.Topic, ProjectID: body.ProjectID, SourceApp: "mqtt", SourceInstallID: 1, Data: body.Data})
				req, _ := http.NewRequestWithContext(r.Context(), "POST", d.URL()+"/events", bytes.NewReader(raw))
				req.Header.Set("Authorization", "Bearer "+d.Token())
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					http.Error(w, err.Error(), 502)
					return
				}
				defer resp.Body.Close()
				w.WriteHeader(resp.StatusCode)
				_, _ = io.Copy(w, resp.Body)
				return
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer gateway.Close()
	config := map[string]string{"transport_mode": "local", "listen_port": "0", "bind_interface": "127.0.0.1", "ha_discovery_enabled": "false"}
	var tlsConfig *tls.Config
	if secure {
		cert, key, roots := testMQTTCertificate(t)
		config = map[string]string{"transport_mode": "tls", "tls_listen_port": "0", "advertised_host": "127.0.0.1", "bind_interface": "127.0.0.1", "tls_provider": "files", "tls_cert_file": cert, "tls_key_file": key, "ha_discovery_enabled": "false"}
		tlsConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	b := tk.SpawnSidecar(t, "../mqtt", tk.WithProjectID(scope), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithConfig(config))
	mu.Lock()
	broker = b
	mu.Unlock()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(scope), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithConfig(map[string]string{"policy_admin_user_ids": "1"}))
	mu.Lock()
	devices = sc
	mu.Unlock()
	call := func(tool string, args map[string]any) map[string]any {
		return sc.MCPAs(tool, args, 1, "integration", scope)
	}
	enrollment := call("devices_provision", map[string]any{"name": "Integration board", "device_id": "integration-board", "protocol": "apteva.devices/v2", "idempotency_key": "one", "capabilities": map[string]any{"operations": []any{map[string]any{"name": "lamp.set"}}}})
	cred, _ := enrollment["mqtt"].(map[string]any)
	if cred == nil || cred["password"] == "" {
		t.Fatalf("enrollment=%#v", enrollment)
	}
	topics := enrollment["topics"].(map[string]any)
	scheme := "tcp://"
	if secure {
		scheme = "ssl://"
	}
	if cred["tls"] != secure {
		t.Fatal("enrollment TLS metadata mismatch")
	}
	opts := mqtt.NewClientOptions().AddBroker(scheme + cred["host"].(string) + ":" + jsonNumberString(cred["port"])).SetUsername(cred["username"].(string)).SetPassword(cred["password"].(string)).SetClientID("integration-board-client").SetAutoReconnect(false)
	if secure {
		opts.SetTLSConfig(tlsConfig)
	}
	client := mqtt.NewClient(opts)
	if token := client.Connect(); !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatalf("MQTT connect: %v", token.Error())
	}
	defer client.Disconnect(100)
	receipts := make(chan string, 10)
	commands := make(chan string, 10)
	token := client.Subscribe(topics["receipts"].(string), 1, func(_ mqtt.Client, m mqtt.Message) { receipts <- string(m.Payload()) })
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatal("subscribe receipts")
	}
	token = client.Subscribe(topics["commands"].(string), 1, func(_ mqtt.Client, m mqtt.Message) {
		var wire map[string]any
		_ = json.Unmarshal(m.Payload(), &wire)
		id, _ := wire["command_id"].(string)
		commands <- id
		response, _ := json.Marshal(map[string]any{"command_id": id, "success": true, "result": map[string]any{"on": true}})
		client.Publish(topics["response"].(string), 1, false, response)
	})
	if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
		t.Fatal("subscribe commands")
	}
	payload := dataPayload("sample-1", nowText(), map[string]any{"id": "opaque", "temperature": 23.5})
	for i := 0; i < 2; i++ {
		token = client.Publish(topics["state"].(string), 1, false, payload)
		if !token.WaitTimeout(5*time.Second) || token.Error() != nil {
			t.Fatal("publish state")
		}
		select {
		case <-receipts:
		case <-time.After(10 * time.Second):
			t.Fatal("no application receipt")
		}
	}
	history := call("devices_data_list", map[string]any{"device_id": "integration-board"})
	if len(history["items"].([]any)) != 1 {
		t.Fatalf("history=%#v", history)
	}
	command := call("devices_command_send", map[string]any{"device_id": "integration-board", "operation": "lamp.set", "arguments": map[string]any{"on": true}, "idempotency_key": "command-1", "timeout_ms": 10000, "wait": true})
	if command["command"].(map[string]any)["status"] != "succeeded" {
		t.Fatalf("command=%#v", command)
	}
	select {
	case <-commands:
	case <-time.After(time.Second):
		t.Fatal("command not delivered")
	}
	var out map[string]any
	response := sc.RequestWithHeaders("GET", "/devices", nil, &out, map[string]string{"X-Apteva-Project-ID": "foreign"})
	if response.Status != 403 {
		t.Fatalf("HTTP scope: %d", response.Status)
	}
	httpCredential := call("devices_ingest_credential_create", map[string]any{"device_id": "integration-board", "idempotency_key": "https-one"})
	deviceToken := httpCredential["token"].(string)
	publicGateway := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/ingest/") {
			http.NotFound(w, r)
			return
		}
		request, _ := http.NewRequestWithContext(r.Context(), r.Method, sc.URL()+r.URL.Path, r.Body)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Device-Token", r.Header.Get("X-Device-Token"))
		response, e := http.DefaultClient.Do(request)
		if e != nil {
			http.Error(w, "sidecar unavailable", 502)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	defer publicGateway.Close()
	httpPayload := dataPayload("https-sample", nowText(), map[string]any{"temperature": 24.5})
	for i, expected := range []int{201, 200} {
		req, _ := http.NewRequest("POST", publicGateway.URL+"/ingest/integration-board/state", strings.NewReader(httpPayload))
		req.Header.Set("X-Device-Token", deviceToken)
		response, e := publicGateway.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("HTTPS attempt %d status=%d %s", i, response.StatusCode, raw)
		}
	}
	summary := call("devices_usage_summary", map[string]any{})
	if summary["totals"].(map[string]any)["data.messages"] != "2" {
		t.Fatalf("aggregate usage=%#v", summary)
	}
	var policy map[string]any
	resp := sc.RequestWithHeaders("PUT", "/policy", map[string]any{"expected_revision": 0, "request_id": "live-policy", "limits": map[string]any{"max_devices": 1}}, &policy, map[string]string{"X-Apteva-Operator-ID": "1"})
	if resp.Status != 200 || policy["revision"] != float64(1) {
		t.Fatalf("live policy=%d %s", resp.Status, resp.Body)
	}

	deleted := call("devices_delete", map[string]any{"device_id": "integration-board", "confirm": true, "idempotency_key": "delete-1"})
	if deleted["status"] != "completed" {
		t.Fatalf("delete=%#v", deleted)
	}
	replay := call("devices_delete", map[string]any{"device_id": "integration-board", "confirm": true, "idempotency_key": "delete-1"})
	if replay["operation_id"] != deleted["operation_id"] {
		t.Fatal("delete replay changed operation")
	}
}
func jsonNumberString(value any) string { raw, _ := json.Marshal(value); return string(raw) }

func testMQTTCertificate(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Local test MQTT"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = os.WriteFile(certPath, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(cert)
	return certPath, keyPath, roots
}
