package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Serialize lifecycle changes for one post without holding a SQL transaction
// across network calls. SQL compare-and-swap checks remain the durable guard.
var socialPostLocks = struct {
	sync.Mutex
	entries map[postLockKey]*postLock
}{entries: map[postLockKey]*postLock{}}

type postLockKey struct {
	db   *sql.DB
	id   int64
	kind string
}
type postLock struct {
	sync.Mutex
	refs int
}

func lockSocialPost(ctx *sdk.AppCtx, id int64) func() {
	return lockSocialResource(ctx, id, "post")
}

func lockSocialResource(ctx *sdk.AppCtx, id int64, kind string) func() {
	key := postLockKey{ctx.AppDB(), id, kind}
	socialPostLocks.Lock()
	entry := socialPostLocks.entries[key]
	if entry == nil {
		entry = &postLock{}
		socialPostLocks.entries[key] = entry
	}
	entry.refs++
	socialPostLocks.Unlock()
	entry.Lock()
	return func() {
		entry.Unlock()
		socialPostLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(socialPostLocks.entries, key)
		}
		socialPostLocks.Unlock()
	}
}

// Media URLs come from the Storage app's scoped files_get/link contract.
// Private destinations are intentional for self-hosted storage. Arbitrary
// provider avatar URLs use the separate public-only transport.
var mediaHTTPClient = &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many storage redirects")
	}
	return validateAvatarURL(req.URL)
}, Transport: &http.Transport{
	Proxy: http.ProxyFromEnvironment, MaxIdleConns: 32, MaxIdleConnsPerHost: 4,
	IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
}}

type providerPendingError struct{}

func (*providerPendingError) Error() string {
	return "provider accepted publication; confirmation pending"
}

func providerPublicationResult(raw json.RawMessage, account string) (string, string, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", "", err
	}
	item := envelope
	if post, ok := envelope["post"].(map[string]any); ok {
		item = post
	}
	var target map[string]any
	if platforms, ok := item["platforms"].([]any); ok {
		for _, v := range platforms {
			p, ok := v.(map[string]any)
			if ok && (firstString(p, "accountId", "account_id") == account || (len(platforms) == 1 && firstString(p, "accountId", "account_id") == "")) {
				target = p
				break
			}
		}
	}
	if platforms, ok := item["platforms"].([]any); ok && len(platforms) > 0 && target == nil {
		return "", "", &providerPendingError{}
	}
	status, _, _ := zernioWorkflowStatus(item, target)
	identity := target
	if identity == nil {
		identity = item
	}
	id := firstString(identity, "platformPostId", "platform_post_id", "externalId", "external_id")
	url := firstString(identity, "platformPostUrl", "platform_post_url", "platformUrl", "platform_url", "permalink", "permalinkUrl", "shareUrl")
	if status == "failed" {
		return "", "", &upstreamCallError{Status: 422, Body: "provider rejected publication: " + firstString(target, "error", "message")}
	}
	if status != "published" {
		return "", "", &providerPendingError{}
	}
	return id, url, nil
}

// A crashed native request has an unknown outcome, not a safely retryable
// failure. Known provider operations are queried before considering recovery.
func (a *App) recoverDeliveries(ctx *sdk.AppCtx) {
	a.recoverTikTokDeliveries(ctx)
	rows, err := ctx.AppDB().Query(`SELECT t.id,t.post_id,a.connection_id,a.provider_account_id,t.provider_post_id
 FROM post_targets t JOIN posts p ON p.id=t.post_id JOIN social_accounts a ON a.id=t.social_account_id
 WHERE p.project_id=? AND t.status='publishing' AND a.provider_slug='zernio' AND t.provider_post_id!='' ORDER BY COALESCE(t.provider_updated_at,'') LIMIT 100`, projectScope(ctx))
	if err != nil {
		return
	}
	type pending struct {
		id, post, conn      int64
		account, providerID string
	}
	var list []pending
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.post, &p.conn, &p.account, &p.providerID) == nil {
			list = append(list, p)
		}
	}
	rows.Close()
	for _, p := range list {
		func() {
			unlock := lockSocialPost(ctx, p.post)
			defer unlock()
			_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET provider_updated_at=CURRENT_TIMESTAMP WHERE id=?`, p.id)
			res, err := ctx.PlatformAPI().ExecuteIntegrationTool(p.conn, "get_post", map[string]any{"postId": p.providerID})
			if err != nil || res == nil || !res.Success {
				return
			}
			id, url, err := providerPublicationResult(res.Data, p.account)
			if err == nil {
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET status='published',platform_post_id=?,platform_url=?,published_at=CURRENT_TIMESTAMP,last_error=NULL WHERE id=? AND status='publishing'`, nullable(id), nullable(url), p.id)
			} else {
				var pending *providerPendingError
				if errors.As(err, &pending) {
					return
				}
				a.markTargetError(ctx, p.id, err)
			}
			a.rollupPostStatus(ctx, p.post)
		}()
	}
	rows, err = ctx.AppDB().Query(`SELECT DISTINCT t.post_id FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE p.project_id=? AND t.status='publishing' AND COALESCE(t.provider_post_id,'')='' AND COALESCE(t.publish_operation_id,'')='' AND datetime(t.last_attempt_at)<datetime('now','-1 hour')`, projectScope(ctx))
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		func() {
			unlock := lockSocialPost(ctx, id)
			defer unlock()
			_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET status='failed',failure_code='outcome_unknown',retryable=0,last_error='Delivery confirmation was lost. Verify the remote post before creating a replacement.' WHERE post_id=? AND status='publishing' AND COALESCE(provider_post_id,'')='' AND COALESCE(publish_operation_id,'')='' AND datetime(last_attempt_at)<datetime('now','-1 hour')`, id)
			a.rollupPostStatus(ctx, id)
		}()
	}
}

func (a *App) publishDuePost(ctx *sdk.AppCtx, id int64) {
	unlock := lockSocialPost(ctx, id)
	defer unlock()
	res, err := ctx.AppDB().Exec(`UPDATE posts SET status='publishing' WHERE id=? AND project_id=? AND status='scheduled' AND job_id=0 AND source!='provider' AND datetime(schedule_at)<=CURRENT_TIMESTAMP AND (approval_required=0 OR approved_revision=revision)`, id, projectScope(ctx))
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
	a.publishPostTargets(ctx, id)
}

func postScheduleDue(raw string) bool {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t, err = time.Parse("2006-01-02 15:04:05", raw)
	}
	return err == nil && !t.After(time.Now())
}

func (a *App) toolPostResolveDelivery(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	id := int64(intArg(args, "post_id", 0))
	if id <= 0 {
		return nil, fmt.Errorf("post_id required")
	}
	// Read-only recovery; never retries an unknown native publication.
	a.recoverDeliveries(ctx)
	return a.loadPostByID(ctx, projectScope(ctx), id)
}

// Scheduling accepts a run context so cancellation stops the next queued post.
func publishDueBatch(runCtx context.Context, ids []int64, run func(int64)) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, id := range ids {
		if runCtx.Err() != nil {
			break
		}
		select {
		case slots <- struct{}{}:
		case <-runCtx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(id int64) { defer wg.Done(); defer func() { <-slots }(); run(id) }(id)
	}
	wg.Wait()
}

func persistPublishOperation(ctx *sdk.AppCtx, targetID int64, id string) error {
	if targetID == 0 {
		return nil
	}
	_, err := ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id=? WHERE id=? AND status='publishing'`, id, targetID)
	return err
}

func (a *App) recoverTikTokDeliveries(ctx *sdk.AppCtx) {
	rows, err := ctx.AppDB().Query(`SELECT t.id,t.post_id,a.connection_id,t.publish_operation_id FROM post_targets t JOIN posts p ON p.id=t.post_id JOIN social_accounts a ON a.id=t.social_account_id WHERE p.project_id=? AND t.status='publishing' AND a.platform='tiktok' AND t.publish_operation_id<>'' AND (t.identity_resolve_after IS NULL OR datetime(t.identity_resolve_after)<=CURRENT_TIMESTAMP) LIMIT 20`, projectScope(ctx))
	if err != nil {
		return
	}
	type operation struct {
		id, post, conn int64
		op             string
	}
	var ops []operation
	for rows.Next() {
		var op operation
		if rows.Scan(&op.id, &op.post, &op.conn, &op.op) == nil {
			ops = append(ops, op)
		}
	}
	rows.Close()
	for _, op := range ops {
		func() {
			unlock := lockSocialPost(ctx, op.post)
			defer unlock()
			_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET identity_resolve_after=datetime('now','+5 minutes') WHERE id=?`, op.id)
			status, reason, ids, err := a.getTikTokPublishStatus(ctx, op.conn, op.op)
			if err != nil {
				return
			}
			if status == "FAILED" {
				a.markTargetError(ctx, op.id, &upstreamCallError{Status: 422, Body: "TikTok publish failed: " + reason})
			} else if status == "PUBLISH_COMPLETE" {
				id := ""
				if len(ids) > 0 {
					id = ids[0]
				}
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET status='published',platform_post_id=?,published_at=CURRENT_TIMESTAMP,last_error=NULL WHERE id=? AND status='publishing'`, nullable(id), op.id)
			} else {
				return
			}
			a.rollupPostStatus(ctx, op.post)
		}()
	}
}
