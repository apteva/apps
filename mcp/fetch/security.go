package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	secretEnvelopeKey = "__fetch_secret_v1"
	secretMask        = "********"
)

type secretCodec struct{ aead cipher.AEAD }

func loadSecretCodec(ctx *sdk.AppCtx) (*secretCodec, error) {
	key, err := fetchMasterKey(ctx)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &secretCodec{aead: aead}, nil
}

func fetchMasterKey(ctx *sdk.AppCtx) ([]byte, error) {
	if encoded := strings.TrimSpace(os.Getenv("FETCH_MASTER_KEY")); encoded != "" {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("FETCH_MASTER_KEY: %w", err)
		}
		if len(key) != 32 {
			return nil, errors.New("FETCH_MASTER_KEY must decode to 32 bytes")
		}
		return key, nil
	}
	if ctx == nil || strings.TrimSpace(ctx.DataDir()) == "" {
		return nil, errors.New("fetch requires APTEVA_DATA_DIR or DB_PATH for its encryption key")
	}
	if err := os.MkdirAll(ctx.DataDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(ctx.DataDir(), "fetch-master.key")
	if existing, err := os.ReadFile(path); err == nil {
		if len(existing) != 32 {
			return nil, fmt.Errorf("fetch-master.key has wrong size %d", len(existing))
		}
		return existing, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func (c *secretCodec) sealString(value, aad string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(value), []byte(aad))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (c *secretCodec) openString(encoded, aad string) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	if len(sealed) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", errors.New("encrypted value is truncated")
	}
	nonce := sealed[:c.aead.NonceSize()]
	plain, err := c.aead.Open(nil, nonce, sealed[c.aead.NonceSize():], []byte(aad))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func environmentAAD(pid string, envID int64, key string) string {
	return fmt.Sprintf("fetch/environment/%s/%d/%s", pid, envID, key)
}

func protectRawJSON(raw json.RawMessage, codec *secretCodec, aad string) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	protected, err := protectJSONValue(value, codec, aad, "", false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protected)
}

func protectJSONValue(value any, codec *secretCodec, aad, path string, sensitive bool) (any, error) {
	if marker, ok := encryptedMarker(value); ok {
		return map[string]any{secretEnvelopeKey: marker}, nil
	}
	if sensitive && !containsTemplate(value) {
		body, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		sealed, err := codec.sealString(string(body), aad+"/"+path)
		if err != nil {
			return nil, err
		}
		return map[string]any{secretEnvelopeKey: sealed}, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			next, err := protectJSONValue(child, codec, aad, childPath, isSensitiveName(key))
			if err != nil {
				return nil, err
			}
			out[key] = next
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			next, err := protectJSONValue(child, codec, aad, fmt.Sprintf("%s[%d]", path, index), false)
			if err != nil {
				return nil, err
			}
			out[index] = next
		}
		return out, nil
	default:
		return value, nil
	}
}

func revealRawJSON(raw json.RawMessage, codec *secretCodec, aad string) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	revealed, err := revealJSONValue(value, codec, aad, "")
	if err != nil {
		return nil, err
	}
	return json.Marshal(revealed)
}

func revealJSONValue(value any, codec *secretCodec, aad, path string) (any, error) {
	if marker, ok := encryptedMarker(value); ok {
		plain, err := codec.openString(marker, aad+"/"+path)
		if err != nil {
			return nil, err
		}
		var decoded any
		if err := json.Unmarshal([]byte(plain), &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			next, err := revealJSONValue(child, codec, aad, childPath)
			if err != nil {
				return nil, err
			}
			out[key] = next
		}
		return out, nil
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			next, err := revealJSONValue(child, codec, aad, fmt.Sprintf("%s[%d]", path, index))
			if err != nil {
				return nil, err
			}
			out[index] = next
		}
		return out, nil
	default:
		return value, nil
	}
}

func publicRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	body, _ := json.Marshal(maskJSONValue(value))
	return body
}

func maskJSONValue(value any) any {
	if _, ok := encryptedMarker(value); ok {
		return secretMask
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			out[key] = maskJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, child := range typed {
			out[index] = maskJSONValue(child)
		}
		return out
	default:
		return value
	}
}

func encryptedMarker(value any) (string, bool) {
	m, ok := value.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	encoded, ok := m[secretEnvelopeKey].(string)
	return encoded, ok && encoded != ""
}

func mergeSecretMasks(next, current any) any {
	if text, ok := next.(string); ok && text == secretMask {
		if _, encrypted := encryptedMarker(current); encrypted {
			return current
		}
	}
	nextMap, nextOK := next.(map[string]any)
	currentMap, currentOK := current.(map[string]any)
	if nextOK && currentOK {
		out := make(map[string]any, len(nextMap))
		for key, value := range nextMap {
			out[key] = mergeSecretMasks(value, currentMap[key])
		}
		return out
	}
	nextList, nextOK := next.([]any)
	currentList, currentOK := current.([]any)
	if nextOK && currentOK {
		out := make([]any, len(nextList))
		for index, value := range nextList {
			var old any
			if index < len(currentList) {
				old = currentList[index]
			}
			out[index] = mergeSecretMasks(value, old)
		}
		return out
	}
	return next
}

func containsTemplate(value any) bool {
	text, ok := value.(string)
	return ok && strings.Contains(text, "{{") && strings.Contains(text, "}}")
}

func isSensitiveName(name string) bool {
	normalized := strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(strings.ToLower(name))
	for _, token := range []string{"authorization", "apikey", "accesstoken", "refreshtoken", "token", "secret", "password", "passwd", "cookie", "sessionid", "privatekey"} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func validateSavedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme == "" || u.Host == "" {
		return errors.New("valid absolute http(s) url required")
	}
	if u.User != nil {
		return errors.New("saved request URLs cannot contain userinfo; use a secret environment variable in a header")
	}
	for key, values := range u.Query() {
		if !isSensitiveName(key) {
			continue
		}
		for _, value := range values {
			if !containsTemplate(value) {
				return fmt.Errorf("sensitive URL query parameter %q must reference an environment variable", key)
			}
		}
	}
	return nil
}

func publicSavedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return raw
	}
	u.User = nil
	query := u.Query()
	for key, values := range query {
		if !isSensitiveName(key) {
			continue
		}
		for index, value := range values {
			if !containsTemplate(value) {
				values[index] = "[redacted]"
			}
		}
		query[key] = values
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func redactURL(raw string, secrets []string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return redactSecrets(raw, secrets)
	}
	u.User = nil
	query := u.Query()
	for key, values := range query {
		for index, value := range values {
			value = redactSecrets(value, secrets)
			if isSensitiveName(key) || strings.Contains(value, "[redacted]") {
				value = "[redacted]"
			}
			values[index] = value
		}
		query[key] = values
	}
	u.RawQuery = query.Encode()
	return redactSecrets(u.String(), secrets)
}

func sanitizeRunError(err error, rawURL, safeURL string, secrets []string) error {
	if err == nil {
		return nil
	}
	message := redactSecrets(err.Error(), secrets)
	if rawURL != "" {
		message = strings.ReplaceAll(message, rawURL, safeURL)
	}
	return errors.New(message)
}

func migrateLegacySecrets(db *sql.DB, codec *secretCodec, logger sdk.Logger) error {
	type envRow struct {
		id, envID       int64
		pid, key, value string
	}
	rows, err := db.Query(`SELECT id, environment_id, project_id, key, COALESCE(value,'') FROM fetch_environment_vars WHERE is_secret=1 AND COALESCE(value_encrypted,'')='' AND COALESCE(value,'')<>''`)
	if err != nil {
		return err
	}
	var envRows []envRow
	for rows.Next() {
		var row envRow
		if err := rows.Scan(&row.id, &row.envID, &row.pid, &row.key, &row.value); err != nil {
			_ = rows.Close()
			return err
		}
		envRows = append(envRows, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range envRows {
		sealed, err := codec.sealString(row.value, environmentAAD(row.pid, row.envID, row.key))
		if err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE fetch_environment_vars SET value=NULL, value_encrypted=? WHERE id=?`, sealed, row.id); err != nil {
			return err
		}
	}

	type savedRow struct {
		id                                int64
		pid, rawURL, headers, query, body string
	}
	savedRows, err := db.Query(`SELECT id, project_id, url_template, COALESCE(headers_json,''), COALESCE(query_json,''), COALESCE(body_json,'') FROM fetch_saved_requests`)
	if err != nil {
		return err
	}
	var saved []savedRow
	for savedRows.Next() {
		var row savedRow
		if err := savedRows.Scan(&row.id, &row.pid, &row.rawURL, &row.headers, &row.query, &row.body); err != nil {
			_ = savedRows.Close()
			return err
		}
		saved = append(saved, row)
	}
	if err := savedRows.Close(); err != nil {
		return err
	}
	for _, row := range saved {
		headers, err := protectRawJSON(rawJSON(row.headers), codec, "fetch/saved/"+row.pid+"/headers")
		if err != nil {
			return err
		}
		query, err := protectRawJSON(rawJSON(row.query), codec, "fetch/saved/"+row.pid+"/query")
		if err != nil {
			return err
		}
		body, err := protectRawJSON(rawJSON(row.body), codec, "fetch/saved/"+row.pid+"/body")
		if err != nil {
			return err
		}
		safeURL := publicSavedURL(row.rawURL)
		if _, err := db.Exec(`UPDATE fetch_saved_requests SET url_template=?, headers_json=?, query_json=?, body_json=? WHERE id=?`, safeURL, rawOrNull(headers), rawOrNull(query), rawOrNull(body), row.id); err != nil {
			return err
		}
	}

	if len(envRows) > 0 || len(saved) > 0 {
		logger.Info("fetch: migrated legacy plaintext secrets", "environment_values", len(envRows), "saved_requests", len(saved))
	}
	return scrubLegacyHistory(db)
}

func scrubLegacyHistory(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, url, COALESCE(redacted_request_json,''), COALESCE(redacted_response_json,''), COALESCE(error,'') FROM fetch_history`)
	if err != nil {
		return err
	}
	type item struct {
		id                                  int64
		rawURL, request, response, runError string
	}
	var items []item
	for rows.Next() {
		var row item
		if err := rows.Scan(&row.id, &row.rawURL, &row.request, &row.response, &row.runError); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, row)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, row := range items {
		secrets := sensitiveURLValues(row.rawURL)
		request := scrubHistoryJSON(row.request, secrets)
		response := scrubHistoryJSON(row.response, secrets)
		runError := redactSecrets(row.runError, secrets)
		if _, err := db.Exec(
			`UPDATE fetch_history SET url=?, redacted_request_json=?, redacted_response_json=?, error=? WHERE id=?`,
			redactURL(row.rawURL, secrets), request, response, runError, row.id,
		); err != nil {
			return err
		}
	}
	return nil
}

func sensitiveURLValues(raw string) []string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return nil
	}
	var secrets []string
	if u.User != nil {
		if password, ok := u.User.Password(); ok {
			secrets = append(secrets, password)
		}
	}
	for key, values := range u.Query() {
		if isSensitiveName(key) {
			secrets = append(secrets, values...)
		}
	}
	return secrets
}

func scrubHistoryJSON(raw string, secrets []string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return redactSecrets(raw, secrets)
	}
	scrubbed, err := json.Marshal(redactAny(value, secrets))
	if err != nil {
		return ""
	}
	return string(scrubbed)
}
