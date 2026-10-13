package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"time"
)

type actorReadCheckpoint struct {
	Session    browserSession `json:"session"`
	SeenIDs    []string       `json:"seen_ids"`
	CurrentIDs []string       `json:"current_ids"`
	ResumeNext bool           `json:"resume_next"`
	URL        string         `json:"url"`
}

func (e *actorExecution) readOperation() string {
	var input struct {
		Operation string `json:"operation"`
	}
	_ = json.Unmarshal([]byte(e.run.InputJSON), &input)
	return firstNonEmpty(input.Operation, "run")
}
func (e *actorExecution) readConfigHash(c actorReadViews) string {
	// Copy through JSON to avoid mutating the live pagination bounds.
	b, _ := json.Marshal(c)
	var copyConfig actorReadViews
	_ = json.Unmarshal(b, &copyConfig)
	copyConfig.ContinuationRef = ""
	copyConfig.ContinuationTTLSeconds = 0
	for i := range copyConfig.Views {
		copyConfig.Views[i].Pagination.MaxPages = nil
	}
	b, _ = json.Marshal(struct {
		Config  actorReadViews
		Browser actorBrowser
	}{copyConfig, e.definition.Browser})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (e *actorExecution) resumeReadContinuation(c actorReadViews) error {
	if c.ContinuationRef == "" {
		return nil
	}
	var payload, hash, operation, state string
	var actorID int64
	var revision int
	var expiry int64
	err := e.ctx.AppDB().QueryRow(`SELECT actor_id,actor_revision,operation,config_hash,payload_json,state,expires_at FROM actors_read_continuations WHERE ref=? AND project_id=?`, c.ContinuationRef, projectID(e.ctx)).Scan(&actorID, &revision, &operation, &hash, &payload, &state, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("continuation unavailable in this project; restart inspection explicitly")
	}
	if err != nil {
		return err
	}
	if state != "available" || expiry <= time.Now().Unix() {
		return errors.New("continuation expired or consumed; restart inspection explicitly")
	}
	if actorID != e.run.ActorID || revision != e.run.ActorRevision || operation != e.readOperation() || hash != e.readConfigHash(c) {
		return errors.New("continuation actor, revision, operation, context or read configuration mismatch")
	}
	var checkpoint actorReadCheckpoint
	if err = json.Unmarshal([]byte(payload), &checkpoint); err != nil {
		return err
	}
	if checkpoint.Session.SessionID == "" || len(checkpoint.SeenIDs) > maxActorItems {
		return errors.New("invalid read continuation payload")
	}
	r, err := e.ctx.AppDB().Exec(`UPDATE actors_read_continuations SET state='claimed',claimed_by=? WHERE ref=? AND project_id=? AND state='available' AND expires_at>?`, e.run.ID, c.ContinuationRef, projectID(e.ctx), time.Now().Unix())
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("continuation already claimed; concurrent/repeated reuse refused")
	}
	e.claimedReadRef = c.ContinuationRef
	e.session = &checkpoint.Session
	e.currentURL = checkpoint.URL
	e.readResume = &checkpoint
	var status map[string]any
	err = sdk.CallAppResultContext(e.workerCtx, e.ctx.PlatformAPI(), "computer", "browser_session", withProjectID(e.ctx, map[string]any{"action": "status", "session_id": e.session.SessionID}), &status)
	if err != nil {
		return fmt.Errorf("continuation browser unavailable; restart explicitly: %w", err)
	}
	if status["closed"] == true || stringFromAny(status["status"]) != "active" {
		return errors.New("continuation browser is closed; restart explicitly")
	}
	if stringFromAny(status["session_id"]) != e.session.SessionID || stringFromAny(status["backend"]) != e.session.Backend || (e.definition.Browser.ContextID != "" && stringFromAny(status["app_context_id"]) != e.definition.Browser.ContextID) {
		return errors.New("continuation live browser identity mismatch; restart explicitly")
	}
	return nil
}
func (e *actorExecution) saveReadContinuation() (map[string]any, error) {
	if e.readConfig == nil || !e.readConfig.ContinuationEnabled || e.coverage == nil || !e.coverage.MoreRemaining || e.readCheckpoint == nil || e.session == nil {
		return nil, nil
	}
	if len(e.readCheckpoint.SeenIDs) > maxActorItems {
		return nil, errors.New("continuation key bound exceeded")
	}
	refBytes := make([]byte, 24)
	if _, err := rand.Read(refBytes); err != nil {
		return nil, err
	}
	ref := "read_" + hex.EncodeToString(refBytes)
	expiry := time.Now().Add(time.Duration(boundedInt(e.readConfig.ContinuationTTLSeconds, 600, 60, 1800)) * time.Second)
	if providerExpiry, err := time.Parse(time.RFC3339, e.session.ProviderExpiresAt); err == nil && providerExpiry.Before(expiry) {
		expiry = providerExpiry
	}
	if !expiry.After(time.Now()) {
		return nil, errors.New("provider session expired; restart explicitly")
	}
	payload, _ := json.Marshal(e.readCheckpoint)
	_, err := e.ctx.AppDB().Exec(`INSERT INTO actors_read_continuations(ref,project_id,actor_id,actor_revision,operation,config_hash,payload_json,expires_at,source_run_id) VALUES(?,?,?,?,?,?,?,?,?)`, ref, projectID(e.ctx), e.run.ActorID, e.run.ActorRevision, e.readOperation(), e.readConfigHash(*e.readConfig), string(payload), expiry.Unix(), e.run.ID)
	if err != nil {
		return nil, err
	}
	e.pendingReadRef = ref
	return map[string]any{"ref": ref, "expires_at": expiry.UTC().Format(time.RFC3339), "actor_id": e.run.ActorID, "revision": e.run.ActorRevision, "operation": e.readOperation(), "source_run_id": e.run.ID, "read_only": true, "resume_next": e.readCheckpoint.ResumeNext}, nil
}
func (a *App) toolReadContinuationClose(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ref := stringArg(args, "ref")
	if ref == "" {
		return nil, errors.New("ref required")
	}
	var payload, state string
	err := ctx.AppDB().QueryRow(`SELECT payload_json,state FROM actors_read_continuations WHERE ref=? AND project_id=?`, ref, projectID(ctx)).Scan(&payload, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("continuation not found")
	}
	if err != nil {
		return nil, err
	}
	if state == "consumed" {
		return nil, errors.New("continuation was consumed; close its successor reference instead")
	}
	if state == "claimed" {
		return nil, errors.New("continuation is being resumed; cancel that run instead")
	}
	r, err := ctx.AppDB().Exec(`UPDATE actors_read_continuations SET state='closed' WHERE ref=? AND project_id=? AND state IN ('available','close_pending')`, ref, projectID(ctx))
	if err != nil {
		return nil, err
	}
	n, _ := r.RowsAffected()
	if n == 1 {
		var c actorReadCheckpoint
		if err = json.Unmarshal([]byte(payload), &c); err != nil {
			return nil, err
		}
		if err = a.closeReadBrowser(ctx, c.Session.SessionID); err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE actors_read_continuations SET state='close_pending',expires_at=? WHERE ref=? AND project_id=?`, time.Now().Unix(), ref, projectID(ctx))
			return nil, err
		}
	}
	return map[string]any{"closed": true, "ref": ref}, nil
}
func (a *App) closeReadBrowser(ctx *sdk.AppCtx, sid string) error {
	var out map[string]any
	return ctx.PlatformAPI().CallAppResult("computer", "browser_close", withProjectID(ctx, map[string]any{"session_id": sid}), &out)
}
func (a *App) cleanupReadContinuations(_ context.Context, ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT ref,payload_json,project_id FROM actors_read_continuations WHERE state IN ('available','close_pending') AND expires_at<=? LIMIT 20`, time.Now().Unix())
	if err != nil {
		return err
	}
	var list [][3]string
	for rows.Next() {
		var pair [3]string
		if err = rows.Scan(&pair[0], &pair[1], &pair[2]); err != nil {
			rows.Close()
			return err
		}
		list = append(list, pair)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, pair := range list {
		r, err := ctx.AppDB().Exec(`UPDATE actors_read_continuations SET state='expired' WHERE ref=? AND project_id=? AND state IN ('available','close_pending') AND expires_at<=?`, pair[0], pair[2], time.Now().Unix())
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n == 1 {
			var c actorReadCheckpoint
			if json.Unmarshal([]byte(pair[1]), &c) == nil {
				if err = a.closeReadBrowser(ctx.WithProject(pair[2]), c.Session.SessionID); err != nil {
					_, _ = ctx.AppDB().Exec(`UPDATE actors_read_continuations SET state='close_pending' WHERE ref=? AND project_id=?`, pair[0], pair[2])
					ctx.Logger().Warn("read continuation browser close pending", "ref", pair[0], "err", err.Error())
				}
			}
		}
	}
	return nil
}
