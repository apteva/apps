package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

const (
	persistenceFull     = "full"
	persistenceRedacted = "redacted"
	persistenceNone     = "none"
	redactedValue       = "[REDACTED]"
)

var sensitiveErrorPattern = regexp.MustCompile(`(?i)\b(authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|bearer[_-]?token|client[_-]?secret|password|passphrase|private[_-]?key)\b(\s*[:=]\s*)([^\s,;]+)`)
var sensitivePathIndexPattern = regexp.MustCompile(`\[(\d+)\]`)

func normalizePersistenceMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return persistenceRedacted, nil
	}
	switch value {
	case persistenceFull, persistenceRedacted, persistenceNone:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported persistence mode %q", value)
	}
}

func normalizeSensitivePaths(value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	raw := sliceFromAny(value)
	if raw == nil {
		return nil, fmt.Errorf("sensitive paths must be an array of non-empty strings")
	}
	paths := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, item := range raw {
		path := normalizeSensitivePath(strFromAny(item))
		if path == "" {
			return nil, fmt.Errorf("sensitive paths must be an array of non-empty strings")
		}
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	return paths, nil
}
func normalizeSensitivePath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, "$")
	path = strings.ReplaceAll(path, "[*]", ".*")
	path = sensitivePathIndexPattern.ReplaceAllString(path, ".$1")
	return strings.Trim(path, ".")
}

func sensitivePaths(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var paths []string
	if err := json.Unmarshal(raw, &paths); err != nil {
		return nil
	}
	for i := range paths {
		paths[i] = normalizeSensitivePath(paths[i])
	}
	return paths
}

func persistedFulfillmentValue(mode string, value any, paths json.RawMessage) any {
	if mode == persistenceNone {
		return map[string]any{}
	}
	cloned := cloneJSONValue(value)
	if mode == persistenceFull {
		return cloned
	}
	cloned = redactSensitiveKeys(cloned)
	for _, path := range sensitivePaths(paths) {
		if path != "" {
			cloned = redactSensitivePath(cloned, strings.Split(path, "."))
		}
	}
	return cloned
}

func cloneJSONValue(value any) any {
	if value == nil {
		return map[string]any{}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var cloned any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return map[string]any{}
	}
	return cloned
}

func redactSensitiveKeys(value any) any {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if isSensitiveKey(key) {
				current[key] = redactedValue
				continue
			}
			current[key] = redactSensitiveKeys(child)
		}
	case []any:
		for i := range current {
			current[i] = redactSensitiveKeys(current[i])
		}
	}
	return value
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(normalized)
	switch normalized {
	case "authorization", "proxy_authorization", "cookie", "set_cookie",
		"token", "access_token", "refresh_token", "bearer_token",
		"api_key", "apikey", "secret", "client_secret", "password",
		"passphrase", "private_key", "credential", "credentials":
		return true
	}
	for _, suffix := range []string{"_api_key", "_access_token", "_refresh_token", "_client_secret", "_password", "_passphrase", "_private_key", "_credential"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func redactSensitivePath(value any, parts []string) any {
	if len(parts) == 0 {
		return redactedValue
	}
	part := strings.TrimSpace(parts[0])
	switch current := value.(type) {
	case map[string]any:
		if part == "*" {
			for key, child := range current {
				current[key] = redactSensitivePath(child, parts[1:])
			}
			return current
		}
		if child, ok := current[part]; ok {
			current[part] = redactSensitivePath(child, parts[1:])
		}
	case []any:
		if part == "*" {
			for i := range current {
				current[i] = redactSensitivePath(current[i], parts[1:])
			}
			return current
		}
		if index, err := strconv.Atoi(part); err == nil && index >= 0 && index < len(current) {
			current[index] = redactSensitivePath(current[index], parts[1:])
		}
	}
	return value
}

func fulfillmentStoreValue(out map[string]any, sourcePath string, sensitiveOutputPaths json.RawMessage) (any, error) {
	value, ok := valueAtPath(out, sourcePath)
	if !ok {
		return nil, fmt.Errorf("fulfillment store source %q not found", sourcePath)
	}
	redacted := persistedFulfillmentValue(persistenceRedacted, out, sensitiveOutputPaths)
	safeValue, safe := valueAtPath(redacted, sourcePath)
	if !safe || !reflect.DeepEqual(value, safeValue) || containsRedactedValue(value) {
		return nil, fmt.Errorf("fulfillment store source %q contains sensitive data", sourcePath)
	}
	return value, nil
}

func containsRedactedValue(value any) bool {
	switch current := value.(type) {
	case string:
		return current == redactedValue
	case map[string]any:
		for _, child := range current {
			if containsRedactedValue(child) {
				return true
			}
		}
	case []any:
		for _, child := range current {
			if containsRedactedValue(child) {
				return true
			}
		}
	}
	return false
}

func redactFulfillmentError(message string, input, output any, inputPaths, outputPaths json.RawMessage) string {
	for _, pair := range [][2]any{
		{input, persistedFulfillmentValue(persistenceRedacted, input, inputPaths)},
		{output, persistedFulfillmentValue(persistenceRedacted, output, outputPaths)},
	} {
		for _, secret := range redactedStrings(pair[0], pair[1]) {
			if len(secret) >= 4 {
				message = strings.ReplaceAll(message, secret, redactedValue)
			}
		}
	}
	return sensitiveErrorPattern.ReplaceAllString(message, "$1$2"+redactedValue)
}

func redactedStrings(original, sanitized any) []string {
	var out []string
	var walk func(any, any)
	walk = func(before, after any) {
		if after == redactedValue {
			if text, ok := before.(string); ok && text != "" {
				out = append(out, text)
			}
			return
		}
		switch prior := before.(type) {
		case map[string]any:
			next, _ := after.(map[string]any)
			for key, child := range prior {
				walk(child, next[key])
			}
		case []any:
			next, _ := after.([]any)
			for i, child := range prior {
				if i < len(next) {
					walk(child, next[i])
				}
			}
		}
	}
	walk(cloneJSONValue(original), cloneJSONValue(sanitized))
	return out
}

func scrubFulfillmentHistory(db *sql.DB) error {
	rows, err := db.Query(`
		SELECT r.id, r.input_json, r.output_json, r.error,
		       a.persist_input, a.persist_output, a.sensitive_input_paths_json, a.sensitive_output_paths_json
		FROM saas_fulfillment_runs r
		JOIN saas_plan_actions a ON a.id=r.plan_action_id AND a.project_id=r.project_id`)
	if err != nil {
		return err
	}
	type record struct {
		id                                                  int64
		input, output, errText, persistInput, persistOutput string
		inputPaths, outputPaths                             string
	}
	var records []record
	for rows.Next() {
		var item record
		if err := rows.Scan(&item.id, &item.input, &item.output, &item.errText, &item.persistInput, &item.persistOutput, &item.inputPaths, &item.outputPaths); err != nil {
			rows.Close()
			return err
		}
		records = append(records, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range records {
		var input, output any
		if err := json.Unmarshal([]byte(item.input), &input); err != nil {
			input = map[string]any{}
		}
		if err := json.Unmarshal([]byte(item.output), &output); err != nil {
			output = map[string]any{}
		}
		safeInput := persistedFulfillmentValue(item.persistInput, input, json.RawMessage(item.inputPaths))
		safeOutput := persistedFulfillmentValue(item.persistOutput, output, json.RawMessage(item.outputPaths))
		safeError := redactFulfillmentError(item.errText, input, output, json.RawMessage(item.inputPaths), json.RawMessage(item.outputPaths))
		inputJSON, outputJSON := jsonOrEmpty(safeInput, "{}"), jsonOrEmpty(safeOutput, "{}")
		if inputJSON == item.input && outputJSON == item.output && safeError == item.errText {
			continue
		}
		if _, err := db.Exec(`UPDATE saas_fulfillment_runs SET input_json=?, output_json=?, error=? WHERE id=?`, inputJSON, outputJSON, safeError, item.id); err != nil {
			return err
		}
	}
	return nil
}
