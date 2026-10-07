package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var errCrawlLimit = errors.New("crawl budget reached; committed partial data and pending URLs are preserved")
var errCrawlFence = errors.New("crawl page lease is no longer owned by this worker")

func (a *App) executeCrawlRun(exec *actorExecution) error {
	defer func() {
		_, _ = exec.ctx.AppDB().Exec(`DELETE FROM actors_context_locks WHERE project_id=? AND run_id=?`, projectID(exec.ctx), exec.run.ID)
	}()
	out, runErr := exec.runCrawl()
	status := "completed"
	if errors.Is(runErr, errActorCancelled) {
		status = "cancelled"
	} else if runErr != nil && !errors.Is(runErr, errCrawlLimit) {
		status = "failed"
	}
	finishErr := runErr
	if errors.Is(finishErr, errCrawlLimit) {
		finishErr = nil
		if out == nil {
			out = map[string]any{}
		}
		out["partial"] = true
	}
	if out == nil {
		out = map[string]any{}
	}
	out["run_id"] = exec.run.ID
	out["actor_id"] = exec.run.ActorID
	out["actor_revision"] = exec.run.ActorRevision
	out["trace_preview"] = previewActorTrace(exec.trace)
	if err := finishActorRun(exec.ctx, exec.run.ID, status, out, finishErr); err != nil {
		return err
	}
	payload := map[string]any{"run_id": exec.run.ID, "actor_id": exec.run.ActorID, "status": status, "crawl": true}
	if runErr != nil && !errors.Is(runErr, errCrawlLimit) {
		payload["error"] = runErr.Error()
	}
	exec.ctx.Emit("actor.run."+status, payload)
	return nil
}

func (e *actorExecution) runCrawl() (map[string]any, error) {
	c := e.crawl
	for _, seed := range c.Seeds {
		route, ok := crawlRouteForURL(*c, seed)
		if !ok {
			return nil, fmt.Errorf("no route matches %s", seed)
		}
		if err := e.enqueueCrawlURL(seed, route, 0, ""); err != nil {
			return nil, err
		}
	}
	// Each slot owns its Computer session. Saved login contexts are serialized.
	concurrency := max(1, c.Frontier.Concurrency)
	if e.definition.Browser.ContextID != "" {
		concurrency = 1
	}
	ctx, cancel := context.WithCancel(e.workerCtx)
	defer cancel()
	limiter := &crawlLimiter{delay: time.Duration(c.Frontier.DelayMS) * time.Millisecond, next: map[string]time.Time{}}
	var wg sync.WaitGroup
	errs := make(chan error, concurrency)
	for slot := 0; slot < concurrency; slot++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker := *e
			worker.workerCtx = ctx
			worker.session = nil
			worker.trace = nil
			worker.items = nil
			defer func() {
				if worker.session != nil {
					worker.app.closeBrowser(worker.ctx, worker.session.SessionID)
				}
			}()
			if err := worker.crawlLoop(limiter); err != nil {
				errs <- err
				cancel()
			}
		}()
	}
	wg.Wait()
	close(errs)
	var runErr error
	for err := range errs {
		if runErr == nil || errors.Is(runErr, context.Canceled) {
			runErr = err
		}
	}
	stats, err := crawlSummary(e.ctx, e.run.ID)
	if err != nil {
		return nil, err
	}
	e.pageCount = intFromAny(stats["page_count"])
	if errors.Is(runErr, errCrawlLimit) && intFromAny(stats["pending_pages"]) == 0 {
		runErr = nil
	}
	if runErr == nil && intFromAny(stats["failed_pages"]) > 0 {
		runErr = errors.New("crawl has failed pages; inspect frontier and resume to retry")
	}
	if runErr == nil && intFromAny(stats["pending_pages"]) > 0 {
		runErr = errCrawlLimit
	}
	return stats, runErr
}

type crawlLimiter struct {
	mu    sync.Mutex
	delay time.Duration
	next  map[string]time.Time
}

func crawlHost(raw string) string { u, _ := url.Parse(raw); return strings.ToLower(u.Hostname()) }
func crawlURLMatches(raw, match string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.HasPrefix(u.Path, match)
}
func (l *crawlLimiter) wait(ctx context.Context, raw string) error {
	host := crawlHost(raw)
	l.mu.Lock()
	when := l.next[host]
	now := time.Now()
	if when.Before(now) {
		when = now
	}
	l.next[host] = when.Add(l.delay)
	l.mu.Unlock()
	timer := time.NewTimer(time.Until(when))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *actorExecution) crawlLoop(limiter *crawlLimiter) error {
	for {
		if err := e.checkpoint(); err != nil {
			return err
		}
		item, err := claimCrawlItem(e.ctx, e.run.ID, e.maxPages)
		if err != nil {
			return err
		}
		if item == nil {
			var active int
			if err := e.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_crawl_queue WHERE project_id=? AND run_id=? AND status IN ('pending','running')`, projectID(e.ctx), e.run.ID).Scan(&active); err != nil {
				return err
			}
			if active == 0 {
				return nil
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-e.workerCtx.Done():
				timer.Stop()
				return e.workerCtx.Err()
			case <-timer.C:
			}
			continue
		}
		if err := limiter.wait(e.workerCtx, item.URL); err != nil {
			return err
		}
		pageCtx, cancel := context.WithTimeout(e.workerCtx, 60*time.Second)
		pageWorker := *e
		pageWorker.workerCtx = pageCtx
		route := e.crawl.Routes[item.Route]
		var page *crawlPage
		err = pageWorker.gotoURL(item.URL)
		if err == nil {
			for _, step := range route.Steps {
				err = pageWorker.runStep(step)
				if err != nil {
					break
				}
			}
		}
		if err == nil {
			var doc *browserExtractResult
			doc, err = pageWorker.extractCrawlDOM()
			if err == nil {
				page, err = extractCrawlPage(*e.crawl, route, item, doc)
			}
		}
		e.session = pageWorker.session
		e.currentURL = pageWorker.currentURL
		if err == nil {
			err = commitCrawlPage(e.ctx, e.run, e.definition, item, page, e.maxItems)
		}
		cancel()
		if errors.Is(err, errCrawlLimit) || errors.Is(err, errCrawlFence) {
			return err
		}
		if err != nil {
			// Capture diagnostic artifacts before closing a broken session.
			if e.session != nil {
				_, _ = e.storeCurrentScreenshot("failed crawl page")
			}
			if failure := failCrawlItem(e.ctx, item, e.crawl.Frontier.MaxRetries, err); failure != nil {
				return failure
			}
			if e.session != nil {
				e.app.closeBrowser(e.ctx, e.session.SessionID)
				e.session = nil
			}
		}
		stats, statErr := crawlSummary(e.ctx, e.run.ID)
		if statErr != nil {
			return statErr
		}
		raw, _ := json.Marshal(stats)
		if _, err := e.ctx.AppDB().Exec(`UPDATE actors_runs SET output_json=?,summary=? WHERE id=? AND project_id=? AND status='running'`, string(raw), fmt.Sprintf("%v pages, %v records", stats["page_count"], stats["item_count"]), e.run.ID, projectID(e.ctx)); err != nil {
			return err
		}
		e.ctx.Emit("actor.run.progress", map[string]any{"run_id": e.run.ID, "crawl": stats})
	}
}

func (e *actorExecution) enqueueCrawlURL(raw, route string, depth int, parent string) error {
	if err := validateHTTPURL(raw); err != nil {
		return err
	}
	if !hostAllowed(raw, e.definition.AllowedHosts) {
		return errors.New("crawl URL is outside allowed_hosts")
	}
	_, err := e.ctx.AppDB().Exec(`INSERT OR IGNORE INTO actors_crawl_queue(project_id,run_id,url,route,depth,parent_key,dedupe_key) VALUES(?,?,?,?,?,?,?)`, projectID(e.ctx), e.run.ID, canonicalCrawlURL(raw), route, depth, parent, canonicalCrawlURL(raw))
	return err
}

func claimCrawlItem(ctx *sdk.AppCtx, runID int64, maxPages int) (*crawlQueueItem, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Fencing invalidates a late result from an expired owner.
	if _, err := tx.Exec(`UPDATE actors_crawl_queue SET status='pending',fence=fence+1,lease_until=NULL WHERE project_id=? AND run_id=? AND status='running' AND lease_until< CURRENT_TIMESTAMP`, projectID(ctx), runID); err != nil {
		return nil, err
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM actors_crawl_queue WHERE project_id=? AND run_id=? AND status IN ('completed','running')`, projectID(ctx), runID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxPages {
		return nil, errCrawlLimit
	}
	var item crawlQueueItem
	err = tx.QueryRow(`SELECT id,url,route,depth,COALESCE(parent_key,''),attempts,fence FROM actors_crawl_queue WHERE project_id=? AND run_id=? AND status='pending' AND (next_attempt_at IS NULL OR next_attempt_at<=CURRENT_TIMESTAMP) ORDER BY id LIMIT 1`, projectID(ctx), runID).Scan(&item.ID, &item.URL, &item.Route, &item.Depth, &item.ParentKey, &item.Attempts, &item.Fence)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.Attempts++
	item.Fence++
	res, err := tx.Exec(`UPDATE actors_crawl_queue SET status='running',attempts=?,fence=?,lease_until=datetime('now','+120 seconds'),updated_at=CURRENT_TIMESTAMP WHERE id=? AND status='pending'`, item.Attempts, item.Fence, item.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errCrawlFence
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func failCrawlItem(ctx *sdk.AppCtx, item *crawlQueueItem, maxRetries int, failure error) error {
	status := "failed"
	if item.Attempts <= maxRetries {
		status = "pending"
	}
	seconds := 1 << min(item.Attempts, 6)
	res, err := ctx.AppDB().Exec(`UPDATE actors_crawl_queue SET status=?,last_error=?,next_attempt_at=datetime('now',?),lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND fence=? AND status='running'`, status, failure.Error(), fmt.Sprintf("+%d seconds", seconds), item.ID, projectID(ctx), item.Fence)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errCrawlFence
	}
	return nil
}

func commitCrawlPage(ctx *sdk.AppCtx, run *actorQueuedRun, def actorDefinition, item *crawlQueueItem, page *crawlPage, maxItems int) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Ack, run evidence, materialized upserts and new frontier entries commit together.
	res, err := tx.Exec(`UPDATE actors_crawl_queue SET status='completed',last_error=NULL,lease_until=NULL,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND run_id=? AND fence=? AND status='running' AND EXISTS(SELECT 1 FROM actors_runs WHERE id=? AND status='running' AND cancel_requested_at IS NULL)`, item.ID, projectID(ctx), run.ID, item.Fence, run.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errCrawlFence
	}
	var count, bytes int
	if err := tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(item_json)),0) FROM actors_crawl_records WHERE project_id=? AND run_id=?`, projectID(ctx), run.ID).Scan(&count, &bytes); err != nil {
		return err
	}
	for _, record := range page.Records {
		raw, err := json.Marshal(record.Item)
		if err != nil {
			return err
		}
		if len(raw) > maxActorItemBytes {
			return errors.New("crawl record exceeds item byte limit")
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO actors_crawl_records(project_id,run_id,dataset,record_key,source_url,item_json) VALUES(?,?,?,?,?,?)`, projectID(ctx), run.ID, record.Dataset, record.Key, item.URL, string(raw))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			count++
			bytes += len(raw)
		}
		if count > maxItems || bytes > maxActorDatasetBytes {
			return errCrawlLimit
		}
		if _, err := tx.Exec(`INSERT INTO actors_crawl_materialized(project_id,actor_id,dataset,record_key,item_json,source_url,last_run_id) VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id,actor_id,dataset,record_key) DO UPDATE SET item_json=excluded.item_json,source_url=excluded.source_url,last_run_id=excluded.last_run_id,updated_at=CURRENT_TIMESTAMP`, projectID(ctx), run.ActorID, record.Dataset, record.Key, string(raw), item.URL, run.ID); err != nil {
			return err
		}
	}
	for _, link := range page.Links {
		if !hostAllowed(link.URL, def.AllowedHosts) {
			return fmt.Errorf("follow URL outside allowed_hosts: %s", link.URL)
		}
		if err := validateHTTPURL(link.URL); err != nil {
			return err
		}
		route, ok := def.Crawl.Routes[link.Route]
		if !ok {
			return errors.New("follow route not found")
		}
		if !crawlURLMatches(link.URL, route.Match) {
			return fmt.Errorf("URL %s does not match route %s", link.URL, link.Route)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO actors_crawl_queue(project_id,run_id,url,route,depth,parent_key,dedupe_key) VALUES(?,?,?,?,?,?,?)`, projectID(ctx), run.ID, link.URL, link.Route, link.Depth, link.ParentKey, canonicalCrawlURL(link.URL)); err != nil {
			return err
		}
	}
	var frontierSize int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM actors_crawl_queue WHERE project_id=? AND run_id=?`, projectID(ctx), run.ID).Scan(&frontierSize); err != nil {
		return err
	}
	if frontierSize > maxCrawlFrontierSize {
		return errCrawlLimit
	}
	return tx.Commit()
}

func crawlSummary(ctx *sdk.AppCtx, runID int64) (map[string]any, error) {
	stats := map[string]any{"crawl": true, "page_count": 0, "pending_pages": 0, "failed_pages": 0}
	rows, err := ctx.AppDB().Query(`SELECT status,COUNT(*) FROM actors_crawl_queue WHERE project_id=? AND run_id=? GROUP BY status`, projectID(ctx), runID)
	if err != nil {
		return nil, err
	}
	frontier := map[string]int{}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			rows.Close()
			return nil, err
		}
		frontier[state] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	stats["frontier"] = frontier
	stats["page_count"] = frontier["completed"]
	stats["pending_pages"] = frontier["pending"] + frontier["running"]
	stats["failed_pages"] = frontier["failed"]
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM actors_crawl_records WHERE project_id=? AND run_id=?`, projectID(ctx), runID).Scan(&count); err != nil {
		return nil, err
	}
	stats["item_count"] = count
	datasets := map[string]int{}
	rows, err = ctx.AppDB().Query(`SELECT dataset,COUNT(*) FROM actors_crawl_records WHERE project_id=? AND run_id=? GROUP BY dataset`, projectID(ctx), runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			rows.Close()
			return nil, err
		}
		datasets[name] = count
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	stats["datasets"] = datasets
	return stats, nil
}
