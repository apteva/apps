package main

// This opt-in launcher runs the standard Tier 3 YAML scenarios through apteva
// test. It only supplies fresh credentials from the local server connection;
// the assertions and repeat counts live in scenarios/21-23, not in this wrapper.
import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localServerCodexToken(t *testing.T) (string, string) {
	t.Helper()
	home := os.Getenv("APTEVA_CODEX_SERVER_HOME")
	if home == "" {
		t.Fatal("set APTEVA_CODEX_SERVER_HOME to the reauthenticated local server data directory")
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "apteva.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	id := os.Getenv("APTEVA_CODEX_CONNECTION_ID")
	if id == "" {
		t.Fatal("set APTEVA_CODEX_CONNECTION_ID to the active server Codex connection")
	}
	var encrypted string
	if err := db.QueryRow("SELECT encrypted_credentials FROM connections WHERE id=? AND app_slug='openai-codex' AND status='active'", id).Scan(&encrypted); err != nil {
		t.Fatal("read active server Codex connection:", err)
	}
	keyHex, err := os.ReadFile(filepath.Join(home, ".secret"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	if err != nil {
		t.Fatal("invalid server encryption key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal("invalid server encryption key")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := hex.DecodeString(encrypted)
	if err != nil || len(blob) < gcm.NonceSize() {
		t.Fatal("invalid encrypted connection")
	}
	raw, err := gcm.Open(nil, blob[:gcm.NonceSize()], blob[gcm.NonceSize():], nil)
	if err != nil {
		t.Fatal("cannot decrypt server connection")
	}
	var data map[string]any
	if json.Unmarshal(raw, &data) != nil {
		t.Fatal("invalid credential JSON")
	}
	if nested, ok := data["credentials"].(map[string]any); ok {
		data = nested
	}
	token, _ := data["access_token"].(string)
	account, _ := data["account_id"].(string)
	if account == "" {
		account, _ = data["chatgpt_account_id"].(string)
	}
	if token == "" {
		t.Fatal("server connection has no access token; no fallback credentials are permitted")
	}
	// Report only freshness metadata, never claims identifying the account or tokens.
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("server Codex access token is not a JWT")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal("cannot inspect server token expiry")
	}
	var claims struct {
		Issued  int64 `json:"iat"`
		Expires int64 `json:"exp"`
	}
	if json.Unmarshal(claimsJSON, &claims) != nil || claims.Expires == 0 {
		t.Fatal("server Codex token has no readable expiry")
	}
	if time.Until(time.Unix(claims.Expires, 0)) < 20*time.Minute {
		t.Fatal("server Codex token expires too soon for this suite; reauthenticate locally")
	}
	t.Logf("Credential source: %s, active Codex connection %s; last_refresh=%v; issued=%s; expires=%s (token not logged)",
		home, id, data["last_refresh"], time.Unix(claims.Issued, 0).UTC().Format(time.RFC3339), time.Unix(claims.Expires, 0).UTC().Format(time.RFC3339))
	return token, account
}

func TestTier3CodexPacing(t *testing.T) {
	if os.Getenv("RUN_TASKS_CODEX_PACING") != "1" {
		t.Skip("set RUN_TASKS_CODEX_PACING=1 to run Tier 3 pacing scenarios")
	}
	cli := os.Getenv("APTEVA_TEST_CLI_BIN")
	if cli == "" {
		t.Fatal("APTEVA_TEST_CLI_BIN is required (runner must support interaction: event)")
	}
	token, account := localServerCodexToken(t)
	// Override any inherited token before apteva test can load test.env.
	t.Setenv("OPENAI_CODEX_ACCESS_TOKEN", token)
	t.Setenv("OPENAI_CODEX_ACCOUNT_ID", account)
	model := os.Getenv("APTEVA_TEST_CODEX_MODEL")
	if model == "" {
		model = "gpt-5.6-terra"
	}
	for _, scenario := range []string{
		"21-main-wait-no-task.yaml",
		"22-main-idle-no-task.yaml",
		"23-main-future-work-task.yaml",
	} {
		t.Run(scenario, func(t *testing.T) {
			token, account := localServerCodexToken(t)
			t.Setenv("OPENAI_CODEX_ACCESS_TOKEN", token)
			t.Setenv("OPENAI_CODEX_ACCOUNT_ID", account)
			cmd := exec.Command(cli, "test", "--tier", "3", "--provider", "openai-codex",
				"--model", model, "--app-dir", ".", "--max-budget-usd", "3",
				"--artifacts-dir", filepath.Join(t.TempDir(), "artifacts"),
				"--json", filepath.Join("scenarios", scenario))
			output, err := cmd.CombinedOutput()
			// Defensive redaction, even though the runner never prints provider tokens.
			t.Log(strings.ReplaceAll(string(output), token, "[REDACTED]"))
			if err != nil {
				t.Fatalf("Tier 3 runner: %v", err)
			}
		})
	}
}
