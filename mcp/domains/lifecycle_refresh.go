package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func refreshListing(ctx *sdk.AppCtx, pid, domain string, b *sdk.BoundIntegration, source string, parents ...context.Context) (*ListingSnapshot, error) {
	tool := "get_sellerhub_domain"
	if b.AppSlug == "dynadot" {
		tool = "get_domain_info"
	}
	var result *sdk.ExecuteResult
	var err error
	if len(parents) > 0 {
		result, err = sdk.ExecuteIntegrationToolContext(parents[0], ctx.PlatformAPI(), b.ConnectionID, tool, map[string]any{"domain": domain})
	} else {
		result, err = ctx.PlatformAPI().ExecuteIntegrationTool(b.ConnectionID, tool, map[string]any{"domain": domain})
	}
	if err != nil {
		listingFailure(ctx, pid, domain, b, err)
		return nil, err
	}
	if result == nil {
		err = errors.New("provider returned no listing response")
		listingFailure(ctx, pid, domain, b, err)
		return nil, err
	}
	if result.Status == 404 {
		return saveListing(ctx, pid, domain, b, "missing", "missing", map[string]any{"name": domain, "status": "missing"}, source)
	}
	if !result.Success {
		err = fmt.Errorf("listing refresh HTTP %d: %s", result.Status, truncate(string(result.Data), 300))
		listingFailure(ctx, pid, domain, b, err)
		return nil, err
	}
	var raw map[string]any
	if err = json.Unmarshal(result.Data, &raw); err != nil {
		listingFailure(ctx, pid, domain, b, err)
		return nil, err
	}
	state, status, clean, err := normalizeListing(b.AppSlug, domain, raw)
	if err != nil {
		listingFailure(ctx, pid, domain, b, err)
		return nil, err
	}
	return saveListing(ctx, pid, domain, b, state, status, clean, source)
}

type saleCursor struct {
	Cursor, From, To, Completed, LastError string
	Next                                   int64
}

func refreshSoldReport(parent context.Context, ctx *sdk.AppCtx, pid string, b *sdk.BoundIntegration, force bool) error {
	var cur saleCursor
	err := ctx.AppDB().QueryRow(`SELECT cursor,window_from,window_to,completed_at,next_check_at,last_error FROM domain_sync_cursors WHERE project_id=? AND connection_id=? AND kind='sales'`, pid, b.ConnectionID).Scan(&cur.Cursor, &cur.From, &cur.To, &cur.Completed, &cur.Next, &cur.LastError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	now := time.Now().UTC()
	if !force && cur.Next > now.Unix() {
		return nil
	}
	if cur.To == "" {
		cur.To = now.Format("2006-01-02T15:04:05.000Z")
		if cur.Completed != "" {
			last, e := time.Parse(time.RFC3339Nano, cur.Completed)
			if e != nil {
				return e
			}
			cur.From = last.Add(-24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	for _, value := range []*string{&cur.From, &cur.To} {
		if *value != "" {
			t, e := time.Parse(time.RFC3339Nano, *value)
			if e != nil {
				return e
			}
			*value = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}
	for page := 0; page < 3; page++ {
		if parent.Err() != nil {
			return parent.Err()
		}
		in := map[string]any{"take": 100, "saleDateTimeTo": cur.To}
		if cur.From != "" {
			in["saleDateTimeFrom"] = cur.From
		}
		if cur.Cursor != "" {
			in["cursor"] = cur.Cursor
		}
		raw, e := providerCall(ctx, b, "list_sellerhub_sold_domains", in, parent)
		if e != nil {
			tx, saveErr := ctx.AppDB().Begin()
			if saveErr != nil {
				return saveErr
			}
			_, saveErr = tx.Exec(`INSERT INTO domain_sync_cursors(project_id,connection_id,kind,cursor,window_from,window_to,completed_at,next_check_at,last_error) VALUES(?,?,'sales',?,?,?,?,unixepoch()+300,?) ON CONFLICT(project_id,connection_id,kind) DO UPDATE SET last_error=excluded.last_error,next_check_at=excluded.next_check_at`, pid, b.ConnectionID, cur.Cursor, cur.From, cur.To, cur.Completed, truncate(e.Error(), 400))
			if saveErr == nil && cur.LastError != truncate(e.Error(), 400) {
				key, _ := randomToken(16)
				saveErr = queueDomainEvent(tx, pid, "", "domain.sync.failed", "sales-report-error:"+key, map[string]any{"provider": b.AppSlug, "connection_id": b.ConnectionID, "kind": "sales_report"})
			}
			if saveErr == nil {
				saveErr = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
			if saveErr != nil {
				return saveErr
			}
			deliverDomainEvents(ctx, pid)

			return e
		}
		var env map[string]any
		if json.Unmarshal(raw, &env) != nil {
			return errors.New("invalid sales report JSON")
		}
		items, ok := env["items"].([]any)
		if !ok {
			return errors.New("sales report items missing")
		}
		for _, value := range items {
			row, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid sales report row")
			}
			domain, e := normaliseDomainName(strArg(row, "name"))
			if e != nil {
				return e
			}
			snapshot, e := listingGet(ctx.AppDB(), pid, domain, b.ConnectionID)
			if e != nil {
				return e
			}
			if snapshot == nil {
				continue
			}
			soldAt, e := time.Parse(time.RFC3339Nano, strArg(row, "saleDateTime"))
			if e != nil {
				return fmt.Errorf("sale confirmation omitted valid date for %s", domain)
			}
			// Past sales before the current acquisition are not sales of this holding.
			var acquired string
			_ = ctx.AppDB().QueryRow(`SELECT COALESCE(MAX(attempted_at),'') FROM registration_intents WHERE project_id=? AND domain=? AND status='succeeded'`, pid, domain).Scan(&acquired)
			if acquired == "" {
				_ = ctx.AppDB().QueryRow(`SELECT created_at FROM domains WHERE project_id=? AND name=? AND deleted_at IS NULL`, pid, domain).Scan(&acquired)
			}
			acquisition, _ := time.Parse(time.RFC3339Nano, acquired)
			if acquisition.IsZero() {
				acquisition, _ = time.Parse("2006-01-02 15:04:05", acquired)
			}
			if !acquisition.IsZero() && soldAt.Before(acquisition) {
				continue
			}
			price, e := saleMoney(row["salePrice"])
			if e != nil {
				return fmt.Errorf("invalid confirmed sale price: %w", e)
			}
			payout, e := saleMoney(row["payout"])
			if e != nil {
				return fmt.Errorf("invalid confirmed payout: %w", e)
			}
			if price["currency"] != payout["currency"] {
				return errors.New("sale and payout currencies differ")
			}
			clean := map[string]any{}
			for k, v := range snapshot.Raw {
				clean[k] = v
			}
			clean["status"] = "sold"
			clean["saleDateTime"] = soldAt.UTC().Format(time.RFC3339Nano)
			clean["salePrice"] = price
			clean["payout"] = payout
			clean["sale_evidence"] = map[string]any{"endpoint": "list_sellerhub_sold_domains", "name": domain, "saleDateTime": clean["saleDateTime"], "salePrice": price, "payout": payout, "source": strArg(row, "source")}
			if _, e = saveListing(ctx, pid, domain, b, "sold", "sold", clean, "sales_report"); e != nil {
				return e
			}
		}
		if value, exists := env["cursor"]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return errors.New("invalid sales report cursor")
			}
		}
		next := strArg(env, "cursor")
		if next != "" && next == cur.Cursor {
			return errors.New("sales report repeated its cursor")
		}
		cur.Cursor = next
		if next == "" {
			cur.Completed = cur.To
			cur.From = ""
			cur.To = ""
			cur.Next = now.Add(5 * time.Minute).Unix()
		} else {
			cur.Next = now.Unix()
		}
		if _, e = ctx.AppDB().Exec(`INSERT INTO domain_sync_cursors(project_id,connection_id,kind,cursor,window_from,window_to,completed_at,next_check_at,last_error) VALUES(?,?,'sales',?,?,?,?,?,'') ON CONFLICT(project_id,connection_id,kind) DO UPDATE SET cursor=excluded.cursor,window_from=excluded.window_from,window_to=excluded.window_to,completed_at=excluded.completed_at,next_check_at=excluded.next_check_at,last_error=''`, pid, b.ConnectionID, cur.Cursor, cur.From, cur.To, cur.Completed, cur.Next); e != nil {
			return e
		}
		if next == "" {
			return nil
		}
	}
	return nil
}

func (a *App) runLifecycleRefresh(parent context.Context, ctx *sdk.AppCtx, force bool) (map[string]any, error) {
	pid := ctx.CurrentProject()
	if pid == "" {
		return map[string]any{"skipped": "no dispatched project"}, nil
	}
	deadline, cancel := context.WithTimeout(parent, 25*time.Second)
	defer cancel()
	warnings := []string{}
	listings := 0
	purchases := 0
	expiredRows, e := ctx.AppDB().Query(`SELECT token FROM registration_intents WHERE project_id=? AND status='prepared' AND julianday(expires_at)<=julianday('now') LIMIT 100`, pid)
	if e != nil {
		return nil, e
	}
	expired := []string{}
	for expiredRows.Next() {
		var token string
		if e = expiredRows.Scan(&token); e != nil {
			break
		}
		expired = append(expired, token)
	}
	if e == nil {
		e = expiredRows.Err()
	}
	expiredRows.Close()
	if e != nil {
		return nil, e
	}
	for _, token := range expired {
		if deadline.Err() != nil {
			break
		}
		in, e := dbRegistrationIntentGet(ctx.AppDB(), pid, token)
		if e == nil && in != nil {
			e = registrationTransition(ctx, in, "expired", nil, "purchase review expired")
		}
		if e != nil {
			warnings = append(warnings, e.Error())
		}
	}
	// Registrations only poll an already saved operation. This worker never submits a purchase.
	rows, err := ctx.AppDB().Query(`SELECT token FROM registration_intents WHERE project_id=? AND provider_slug='spaceship' AND status='processing' AND operation_id!='' ORDER BY updated_at LIMIT 20`, pid)
	if err != nil {
		return nil, err
	}
	tokens := []string{}
	for rows.Next() {
		var token string
		if err = rows.Scan(&token); err != nil {
			break
		}
		tokens = append(tokens, token)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, token := range tokens {
		if deadline.Err() != nil {
			break
		}
		if _, e := a.registrationStatusWithContext(deadline, ctx, map[string]any{"confirmation_token": token, "_project_id": pid}); e != nil {
			warnings = append(warnings, e.Error())
		} else {
			purchases++
		}
	}
	if deadline.Err() == nil {
		warnings = append(warnings, a.refreshValuations(ctx, pid, "", deadline)...)
	}
	// Discover supported accounts from inventory and original registration records.
	rows, err = ctx.AppDB().Query(`SELECT d.name,COALESCE((SELECT r.connection_id FROM registration_intents r WHERE r.project_id=d.project_id AND r.domain=d.name AND r.status='succeeded' ORDER BY r.created_at DESC LIMIT 1),d.connection_id,0) FROM domains d WHERE d.project_id=? AND d.deleted_at IS NULL AND d.registrar_slug IN ('spaceship','dynadot') AND NOT EXISTS(SELECT 1 FROM domain_listing_snapshots s WHERE s.project_id=d.project_id AND s.domain=d.name) ORDER BY d.id LIMIT 20`, pid)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		domain string
		conn   int64
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.domain, &c.conn); err != nil {
			break
		}
		if c.conn > 0 {
			candidates = append(candidates, c)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if deadline.Err() != nil {
			break
		}
		b, e := saleConnection(ctx, map[string]any{"connection_id": c.conn, "_project_id": pid})
		if e != nil {
			warnings = append(warnings, e.Error())
			continue
		}
		if _, e = refreshListing(ctx, pid, c.domain, b, "worker", deadline); e != nil {
			warnings = append(warnings, e.Error())
		} else {
			listings++
		}
	}
	rows, err = ctx.AppDB().Query(`SELECT domain,connection_id FROM domain_listing_snapshots WHERE project_id=? AND (? OR next_check_at<=unixepoch()) ORDER BY next_check_at,id LIMIT 20`, pid, force)
	if err != nil {
		return nil, err
	}
	candidates = nil
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.domain, &c.conn); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if deadline.Err() != nil {
			break
		}
		b, e := saleConnection(ctx, map[string]any{"connection_id": c.conn, "_project_id": pid})
		if e != nil {
			warnings = append(warnings, e.Error())
			continue
		}
		if _, e = refreshListing(ctx, pid, c.domain, b, "worker", deadline); e != nil {
			warnings = append(warnings, e.Error())
		} else {
			listings++
		}
	}
	rows, err = ctx.AppDB().Query(`SELECT DISTINCT connection_id FROM domain_listing_snapshots WHERE project_id=? AND provider_slug='spaceship' ORDER BY connection_id`, pid)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if deadline.Err() != nil {
			break
		}
		b, e := saleConnection(ctx, map[string]any{"connection_id": id, "_project_id": pid})
		if e == nil {
			e = refreshSoldReport(deadline, ctx, pid, b, force)
		}
		if e != nil {
			warnings = append(warnings, e.Error())
		}
	}
	_ = drainDomainEvents(deadline, ctx, pid)
	return map[string]any{"project_id": pid, "listings_checked": listings, "purchases_checked": purchases, "warnings": warnings}, nil
}

func (a *App) toolDomainRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return a.runLifecycleRefresh(context.Background(), ctx.WithProject(pid), true)
}

func (a *App) toolDomainSaleHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domain := strArg(args, "domain")
	if domain != "" {
		domain, err = normaliseDomainName(domain)
		if err != nil {
			return nil, err
		}
	}
	rows, err := ctx.AppDB().Query(`SELECT id,state,provider_status,source,snapshot_json,observed_at FROM domain_listing_history WHERE project_id=? AND (?='' OR domain=?) ORDER BY id DESC LIMIT 100`, pid, domain, domain)
	if err != nil {
		return nil, err
	}
	history := []map[string]any{}
	for rows.Next() {
		var id int64
		var state, status, source, raw, at string
		if err = rows.Scan(&id, &state, &status, &source, &raw, &at); err != nil {
			break
		}
		var data any
		_ = json.Unmarshal([]byte(raw), &data)
		history = append(history, map[string]any{"id": id, "state": state, "provider_status": status, "source": source, "snapshot": data, "observed_at": at})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = ctx.AppDB().Query(`SELECT id,domain,provider_slug,connection_id,sold_at,sale_price,payout,currency,evidence_json,confirmed_at FROM domain_sales WHERE project_id=? AND (?='' OR domain=?) ORDER BY id DESC LIMIT 100`, pid, domain, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sales := []map[string]any{}
	for rows.Next() {
		var id, conn int64
		var d, p, at, price, payout, currency, evidence, confirmed string
		if err = rows.Scan(&id, &d, &p, &conn, &at, &price, &payout, &currency, &evidence, &confirmed); err != nil {
			return nil, err
		}
		var proof any
		_ = json.Unmarshal([]byte(evidence), &proof)
		sales = append(sales, map[string]any{"id": id, "domain": d, "provider": p, "connection_id": conn, "sold_at": at, "sale_price": price, "payout": payout, "currency": currency, "evidence": proof, "confirmed_at": confirmed})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	syncRows, err := ctx.AppDB().Query(`SELECT connection_id,completed_at,next_check_at,last_error FROM domain_sync_cursors WHERE project_id=? AND kind='sales' AND (?='' OR connection_id IN (SELECT connection_id FROM domain_listing_snapshots WHERE project_id=? AND domain=?))`, pid, domain, pid, domain)
	if err != nil {
		return nil, err
	}
	defer syncRows.Close()
	syncState := []map[string]any{}
	for syncRows.Next() {
		var conn, next int64
		var at, message string
		if err = syncRows.Scan(&conn, &at, &next, &message); err != nil {
			return nil, err
		}
		syncState = append(syncState, map[string]any{"connection_id": conn, "completed_at": at, "next_check_at": next, "error": message})
	}
	return map[string]any{"history": history, "sales": sales, "sync": syncState}, syncRows.Err()
}
