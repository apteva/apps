package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// last_used_at is written at most once per interval per token, so busy
// tokens do not turn every request into a write on the single-writer DB.
const tokenLastUsedInterval = time.Minute

var tokenPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}(_[a-z0-9]{1,16}){0,2}_$`)

const maxTokenMetadataBytes = 4096

// tokenMetadataFromAny accepts an object of opaque labels. llm stores it and
// passes it through to hooks and usage records but never interprets it.
func tokenMetadataFromAny(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage(`{}`), nil
	}
	raw := jsonFromAny(v)
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`), nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, errors.New("metadata must be a JSON object")
	}
	out, _ := json.Marshal(obj)
	if len(out) > maxTokenMetadataBytes {
		return nil, fmt.Errorf("metadata must be at most %d bytes", maxTokenMetadataBytes)
	}
	return out, nil
}

func createToken(db *sql.DB, args map[string]any) (map[string]any, error) {
	projectID := projectFromArgs(args)
	if projectID == "" {
		return nil, errors.New("project_id is required")
	}
	subjectType := firstNonEmpty(strArg(args, "subject_type"), "project")
	subjectID := firstNonEmpty(strArg(args, "subject_id"), projectID)
	scopes := stringSlice(args["scopes"])
	if len(scopes) == 0 {
		scopes = []string{"chat", "embeddings", "models", "usage"}
	}
	for _, scope := range scopes {
		switch scope {
		case "chat", "embeddings", "models", "usage", "usage:project", "audio", "images", "*":
		default:
			return nil, fmt.Errorf("unsupported token scope %q", scope)
		}
	}
	prefix := firstNonEmpty(strings.TrimSpace(strArg(args, "prefix")), "llm_")
	if !tokenPrefixPattern.MatchString(prefix) {
		return nil, errors.New("prefix must be lowercase letters and digits ending in '_', for example rl_live_")
	}
	label := strings.TrimSpace(strArg(args, "label"))
	if len(label) > 120 {
		return nil, errors.New("label must be at most 120 characters")
	}
	metadata, err := tokenMetadataFromAny(args["metadata"])
	if err != nil {
		return nil, err
	}
	admission, err := admissionFromAny(args["admission"])
	if err != nil {
		return nil, err
	}
	token := prefix + randomSuffix(32)
	last4 := token[len(token)-4:]
	var expires any
	if exp := strArg(args, "expires_at"); exp != "" {
		if _, err := time.Parse(time.RFC3339, exp); err != nil {
			return nil, fmt.Errorf("expires_at must be RFC3339")
		}
		expires = exp
	}
	scopesJSON, _ := json.Marshal(scopes)
	res, err := db.Exec(`INSERT INTO api_tokens (project_id, subject_type, subject_id, token_hash, scopes_json, expires_at,
		label, last4, metadata_json, admission_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, subjectType, subjectID, hashToken(token), string(scopesJSON), expires,
		label, last4, string(metadata), admissionJSON(admission))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	out := map[string]any{
		"id":           id,
		"token":        token,
		"project_id":   projectID,
		"subject_type": subjectType,
		"subject_id":   subjectID,
		"scopes":       scopes,
		"label":        label,
		"last4":        last4,
		"display":      maskedToken(prefix, last4),
		"metadata":     metadata,
		"base_url":     "/api/apps/llm/v1",
	}
	if admission.enabled() {
		out["admission"] = admission
	}
	return out, nil
}

func maskedToken(prefix, last4 string) string {
	if last4 == "" {
		return ""
	}
	return prefix + "…" + last4
}

// updateToken changes a token's label, metadata, or admission hook. Fields
// that are absent are left as they are; admission: null clears the hook.
func updateToken(db *sql.DB, args map[string]any) (*TokenRecord, error) {
	projectID := projectFromArgs(args)
	tokenID := int64Arg(args, "token_id")
	if projectID == "" || tokenID <= 0 {
		return nil, errors.New("project_id and token_id are required")
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["label"]; ok {
		label := strings.TrimSpace(strAny(v))
		if len(label) > 120 {
			return nil, errors.New("label must be at most 120 characters")
		}
		sets = append(sets, "label=?")
		vals = append(vals, label)
	}
	if v, ok := args["metadata"]; ok {
		metadata, err := tokenMetadataFromAny(v)
		if err != nil {
			return nil, err
		}
		sets = append(sets, "metadata_json=?")
		vals = append(vals, string(metadata))
	}
	if v, ok := args["admission"]; ok {
		admission, err := admissionFromAny(v)
		if err != nil {
			return nil, err
		}
		sets = append(sets, "admission_json=?")
		vals = append(vals, admissionJSON(admission))
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update: pass label, metadata, or admission")
	}
	vals = append(vals, tokenID, projectID)
	res, err := db.Exec(`UPDATE api_tokens SET `+strings.Join(sets, ", ")+` WHERE id=? AND project_id=?`, vals...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("token not found")
	}
	rows, err := dbTokensQuery(db, tokenFilter{ProjectID: projectID, TokenID: tokenID, IncludeRevoked: true, Limit: 1})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

type tokenFilter struct {
	ProjectID      string
	SubjectType    string
	SubjectID      string
	TokenID        int64
	Label          string
	Metadata       map[string]any
	IncludeRevoked bool
	Limit          int
}

func dbTokensList(db *sql.DB, projectID, subjectType, subjectID string, includeRevoked bool) ([]TokenRecord, error) {
	return dbTokensQuery(db, tokenFilter{ProjectID: projectID, SubjectType: subjectType, SubjectID: subjectID, IncludeRevoked: includeRevoked})
}

func tokenFilterFromArgs(args map[string]any) (tokenFilter, error) {
	f := tokenFilter{
		ProjectID:      projectFromArgs(args),
		SubjectType:    strArg(args, "subject_type"),
		SubjectID:      strArg(args, "subject_id"),
		TokenID:        int64Arg(args, "token_id"),
		Label:          strArg(args, "label"),
		IncludeRevoked: boolArg(args, "include_revoked"),
		Limit:          intArg(args, "limit", 500),
	}
	if v, ok := args["metadata"]; ok && v != nil {
		raw := jsonFromAny(v)
		if err := json.Unmarshal(raw, &f.Metadata); err != nil {
			return f, errors.New("metadata filter must be an object")
		}
	}
	return f, nil
}

var metadataKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func dbTokensQuery(db *sql.DB, f tokenFilter) ([]TokenRecord, error) {
	conds := []string{"project_id = ?"}
	args := []any{f.ProjectID}
	if f.SubjectType != "" {
		conds = append(conds, "subject_type = ?")
		args = append(args, f.SubjectType)
	}
	if f.SubjectID != "" {
		conds = append(conds, "subject_id = ?")
		args = append(args, f.SubjectID)
	}
	if f.TokenID > 0 {
		conds = append(conds, "id = ?")
		args = append(args, f.TokenID)
	}
	if f.Label != "" {
		conds = append(conds, "label LIKE ? ESCAPE '\\'")
		args = append(args, "%"+likeEscape(f.Label)+"%")
	}
	for _, key := range sortedAnyKeys(f.Metadata) {
		if !metadataKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("metadata filter key %q is not supported", key)
		}
		value, _ := json.Marshal(f.Metadata[key])
		conds = append(conds, `json_extract(metadata_json, '$."`+key+`"') IS json_extract(?, '$')`)
		args = append(args, string(value))
	}
	if !f.IncludeRevoked {
		conds = append(conds, "revoked_at IS NULL")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT id, project_id, subject_type, subject_id, scopes_json, label, last4, metadata_json, admission_json,
		COALESCE(expires_at,''), COALESCE(created_at,''), COALESCE(last_used_at,''), COALESCE(revoked_at,'')
		FROM api_tokens WHERE `+strings.Join(conds, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TokenRecord{}
	for rows.Next() {
		var token TokenRecord
		var scopesJSON, metadata, admission string
		if err := rows.Scan(&token.ID, &token.ProjectID, &token.SubjectType, &token.SubjectID, &scopesJSON, &token.Label, &token.Last4,
			&metadata, &admission, &token.ExpiresAt, &token.CreatedAt, &token.LastUsedAt, &token.RevokedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(scopesJSON), &token.Scopes)
		token.Metadata = json.RawMessage(firstNonEmpty(metadata, "{}"))
		token.Admission = parseAdmissionJSON(admission)
		out = append(out, token)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func authenticateLLMToken(db *sql.DB, r *http.Request) (*TokenIdentity, error) {
	token := bearerToken(r.Header.Get("X-Apteva-Original-Authorization"))
	if token == "" {
		token = bearerToken(r.Header.Get("Authorization"))
	}
	if token == "" {
		token = strings.TrimSpace(r.Header.Get("X-LLM-API-Key"))
	}
	if token == "" {
		return nil, errors.New("missing LLM bearer token")
	}
	var (
		ident      TokenIdentity
		scopesJSON string
		metadata   string
		admission  string
		expires    sql.NullString
		revoked    sql.NullString
		lastUsed   sql.NullString
	)
	err := db.QueryRow(`SELECT id, project_id, subject_type, subject_id, scopes_json, label, metadata_json, admission_json,
		COALESCE(expires_at,''), COALESCE(revoked_at,''), COALESCE(last_used_at,'') FROM api_tokens WHERE token_hash = ?`,
		hashToken(token)).Scan(&ident.ID, &ident.ProjectID, &ident.SubjectType, &ident.SubjectID, &scopesJSON, &ident.Label,
		&metadata, &admission, &expires, &revoked, &lastUsed)
	if err != nil {
		return nil, errors.New("unknown token")
	}
	if revoked.Valid && revoked.String != "" {
		return nil, errors.New("token revoked")
	}
	if expires.Valid && expires.String != "" {
		t, err := time.Parse(time.RFC3339, expires.String)
		if err != nil {
			return nil, errors.New("token has invalid expiration")
		}
		if time.Now().After(t) {
			return nil, errors.New("token expired")
		}
	}
	_ = json.Unmarshal([]byte(scopesJSON), &ident.Scopes)
	ident.Metadata = json.RawMessage(firstNonEmpty(metadata, "{}"))
	ident.Admission = parseAdmissionJSON(admission)
	if lastUsedStale(lastUsed.String) {
		_, _ = db.Exec(`UPDATE api_tokens SET last_used_at=CURRENT_TIMESTAMP WHERE id=?
			AND (last_used_at IS NULL OR last_used_at < datetime('now', ?))`,
			ident.ID, fmt.Sprintf("-%d seconds", int(tokenLastUsedInterval.Seconds())))
	}
	return &ident, nil
}

func lastUsedStale(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, value); err == nil {
			return time.Since(t) >= tokenLastUsedInterval
		}
	}
	return true
}

func (a *App) toolTokenUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rec, err := updateToken(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": rec}, nil
}

func (a *App) handleTokensUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		http.Error(w, "POST or PATCH only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	args["project_id"] = projectFromRequest(r)
	rec, err := updateToken(globalCtx.AppDB(), args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"token": rec})
}
