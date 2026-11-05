package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// A joined page's cursor wraps Database's. Database already refuses a cursor under
// a different request, which includes every bound filter value. The wrapper adds
// what Database can't know: the viewer and the resolver version, so a cursor from
// another viewer or an older plan is refused even when both ask the same thing. It
// also hides what Database's cursor says about the rows.
const cursorPrefix = "gqlc1."

// pipelineConfigVersion identifies a resolver's definition, without the values a
// request supplies.
func pipelineConfigVersion(config map[string]any) string {
	copied := make(map[string]any, len(config))
	for key, value := range config {
		switch key {
		case "_project_id", "_source_id", "graphql_args", "parent":
		default:
			copied[key] = value
		}
	}
	raw, err := json.Marshal(copied)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// cursorViewer is who a cursor belongs to. Token claims such as issue time are left
// out so a refreshed token keeps its place, while a change of permissions does not.
func cursorViewer(ctx context.Context, project string) any {
	if identity := securityIdentity(ctx); identity != nil {
		return []any{identity.Issuer, identity.Subject, identity.Project, identity.API, identity.Tenant, verifiedPermissionList(identity)}
	}
	return executionIdentity(ctx, project)
}

// cursorBinding covers the project, resolver version, step, viewer, effective
// scope, filters and order. Paging values are the only part a cursor may outlive.
func (run *databasePipelineRun) cursorBinding(ctx context.Context, step databasePipelineStep, args map[string]any) (string, error) {
	request := make(map[string]any, len(args))
	for key, value := range args {
		switch key {
		case "cursor", "offset", "limit":
		default:
			request[key] = value
		}
	}
	names := make([]string, 0, len(run.scope))
	for name := range run.scope {
		names = append(names, name)
	}
	sort.Strings(names)
	scope := make([]any, 0, len(names))
	for _, name := range names {
		values := append([]string{}, run.scope[name]...)
		sort.Strings(values)
		scope = append(scope, []any{name, values})
	}
	raw, err := json.Marshal([]any{run.project, run.version, step.ID, cursorViewer(ctx, run.project), scope, request})
	if err != nil {
		return "", invalid("cannot form a cursor for this request")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (a *App) cursorCipher(project string) (cipher.AEAD, error) {
	key, err := a.cursorKey(project)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, runtimeError("storage_error", "cursor key is unavailable")
	}
	return cipher.NewGCM(block)
}

// sealCursor encrypts Database's cursor, whose order values and record keys can name
// fields a viewer isn't shown. The binding is authenticated, so it can't open under
// any other request.
func (a *App) sealCursor(project, binding, native string) (string, error) {
	aead, err := a.cursorCipher(project)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", internal("cannot create a cursor")
	}
	return cursorPrefix + base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(native), []byte(binding))), nil
}

func (a *App) openCursor(project, binding, sealed string) (string, error) {
	refused := runtimeError("invalid_cursor", "cursor does not belong to this request")
	body, isSealed := strings.CutPrefix(sealed, cursorPrefix)
	if !isSealed {
		return "", refused
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", refused
	}
	aead, err := a.cursorCipher(project)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", refused
	}
	native, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(binding))
	if err != nil {
		return "", refused
	}
	return string(native), nil
}

// cursorKey returns the project's cursor key, creating it on first use. It stays
// in the app's own database and is never returned by any API.
func (a *App) cursorKey(project string) ([]byte, error) {
	if key, ok := a.cursorKeys.Load(project); ok {
		return key.([]byte), nil
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, internal("cannot create a cursor key")
	}
	db := a.ctx.AppDB()
	if _, err := db.Exec(`INSERT OR IGNORE INTO graphql_cursor_keys(project_id, secret) VALUES (?, ?)`, project, hex.EncodeToString(fresh)); err != nil {
		return nil, runtimeError("storage_error", "cursor key is unavailable")
	}
	var stored string
	if err := db.QueryRow(`SELECT secret FROM graphql_cursor_keys WHERE project_id = ?`, project).Scan(&stored); err != nil {
		return nil, runtimeError("storage_error", "cursor key is unavailable")
	}
	key, err := hex.DecodeString(stored)
	if err != nil {
		return nil, runtimeError("storage_error", "cursor key is unavailable")
	}
	a.cursorKeys.Store(project, key)
	return key, nil
}
