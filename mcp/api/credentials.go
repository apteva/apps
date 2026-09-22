package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var subjectTypePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]{0,63}$`)
var scopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

func normalizedCredentialInput(pid string, apiID int64, args map[string]any) (apiKeyInput, error) {
	in := apiKeyInput{
		ProjectID:   pid,
		APIID:       apiID,
		Name:        strings.TrimSpace(stringArg(args, "name", "default")),
		SubjectType: strings.TrimSpace(stringArg(args, "subject_type", "")),
		SubjectID:   strings.TrimSpace(stringArg(args, "subject_id", "")),
		ExpiresAt:   strings.TrimSpace(stringArg(args, "expires_at", "")),
		ExternalID:  strings.TrimSpace(stringArg(args, "external_id", "")),
		IssuanceKey: strings.TrimSpace(stringArg(args, "idempotency_key", "")),
	}
	if in.Name == "" || len(in.Name) > 200 || strings.ContainsAny(in.Name, "\r\n\x00") {
		return in, errors.New("name must be 1-200 characters without control lines")
	}
	if (in.SubjectType == "") != (in.SubjectID == "") {
		return in, errors.New("subject_type and subject_id must be provided together")
	}
	if in.SubjectType != "" && !subjectTypePattern.MatchString(in.SubjectType) {
		return in, errors.New("subject_type must be a generic identifier of at most 64 characters")
	}
	if in.SubjectID != "" && !identityString(in.SubjectID) {
		return in, errors.New("invalid subject_id")
	}
	if in.ExternalID != "" && !identityString(in.ExternalID) {
		return in, errors.New("invalid external_id")
	}
	if in.IssuanceKey != "" && !identityString(in.IssuanceKey) {
		return in, errors.New("invalid idempotency_key")
	}
	if in.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil {
			return in, errors.New("expires_at must be an RFC3339 timestamp")
		}
		in.ExpiresAt = expires.UTC().Format(time.RFC3339)
	}
	claims, err := normalizedCredentialClaims(args["claims"])
	if err != nil {
		return in, err
	}
	in.ClaimsJSON = string(claims)
	scopes, err := normalizedScopes(args["scopes"])
	if err != nil {
		return in, err
	}
	in.ScopesJSON = string(scopes)
	metadata, err := normalizedMetadata(args["metadata"])
	if err != nil {
		return in, err
	}
	in.MetadataJSON = string(metadata)
	fingerprint, _ := json.Marshal(map[string]any{
		"api_id": apiID, "name": in.Name, "subject_type": in.SubjectType,
		"subject_id": in.SubjectID, "claims": json.RawMessage(in.ClaimsJSON),
		"scopes": json.RawMessage(in.ScopesJSON), "expires_at": in.ExpiresAt,
		"metadata": json.RawMessage(in.MetadataJSON), "external_id": in.ExternalID,
	})
	sum := sha256.Sum256(fingerprint)
	in.Fingerprint = hex.EncodeToString(sum[:])
	return in, nil
}

func normalizedCredentialClaims(value any) ([]byte, error) {
	if value == nil {
		return []byte("{}"), nil
	}
	claims, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("claims must be an object")
	}
	raw, err := json.Marshal(claims)
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("claims exceed 16 KiB")
	}
	// Normalize all JSON numbers to json.Number so direct SDK calls and HTTP
	// calls are validated identically.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return nil, errors.New("claims must be valid JSON")
	}
	if len(claims) > 64 {
		return nil, errors.New("claims accepts at most 64 entries")
	}
	for name, value := range claims {
		if !credentialClaimName(name) || !safeClaimValue(value) {
			return nil, errors.New("invalid, sensitive, or reserved credential claim: " + name)
		}
	}
	raw, err = json.Marshal(claims)
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("claims exceed 16 KiB")
	}
	return raw, nil
}

func credentialClaimName(name string) bool {
	if !claimName.MatchString(name) {
		return false
	}
	low := strings.ToLower(name)
	for _, sensitive := range []string{"password", "passwd", "token", "secret", "credential", "session", "cookie", "api_key", "apikey", "private_key"} {
		if strings.Contains(low, sensitive) {
			return false
		}
	}
	switch low {
	case "issuer", "iss", "subject", "sub", "project_id", "principal", "authorization", "auth", "function_ids", "subject_type", "subject_id", "scopes":
		return false
	}
	return true
}

func normalizedScopes(value any) ([]byte, error) {
	if value == nil {
		return []byte("[]"), nil
	}
	values, ok := value.([]any)
	if !ok {
		if stringsValue, stringsOK := value.([]string); stringsOK {
			values = make([]any, len(stringsValue))
			for i := range stringsValue {
				values[i] = stringsValue[i]
			}
		} else {
			return nil, errors.New("scopes must be an array of strings")
		}
	}
	if len(values) > 64 {
		return nil, errors.New("scopes accepts at most 64 entries")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		scope, ok := value.(string)
		if !ok || !scopePattern.MatchString(scope) || seen[scope] {
			return nil, errors.New("scopes must contain unique, valid, case-sensitive scope names")
		}
		seen[scope] = true
		out = append(out, scope)
	}
	sort.Strings(out)
	return json.Marshal(out)
}

func normalizedMetadata(value any) ([]byte, error) {
	if value == nil {
		return []byte("{}"), nil
	}
	metadata, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("metadata must be an object")
	}
	raw, err := json.Marshal(metadata)
	if err != nil || len(raw) > 16<<10 {
		return nil, errors.New("metadata exceeds 16 KiB")
	}
	return raw, nil
}

func keyHasRequiredScopes(granted, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := make(map[string]bool, len(granted))
	for _, scope := range granted {
		have[scope] = true
	}
	for _, scope := range required {
		if !have[scope] {
			return false
		}
	}
	return true
}

func principalForAPIKey(key *APIKey) (*Principal, time.Time) {
	claims := make(map[string]any, len(key.Claims)+3)
	for name, value := range key.Claims {
		claims[name] = value
	}
	if key.SubjectType != "" {
		claims["subject_type"] = key.SubjectType
		claims["subject_id"] = key.SubjectID
	}
	if len(key.Scopes) > 0 {
		claims["scopes"] = append([]string(nil), key.Scopes...)
	}
	var expiry time.Time
	if key.ExpiresAt != "" {
		expiry, _ = time.Parse(time.RFC3339, key.ExpiresAt)
	}
	return &Principal{
		Issuer: "apteva:api", Subject: "api_key:" + strconv.FormatInt(key.ID, 10),
		ProjectID: key.ProjectID, Claims: claims,
	}, expiry
}
