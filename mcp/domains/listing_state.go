package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"regexp"
	"strings"
	"time"
)

type ListingSnapshot struct {
	ID             int64          `json:"id"`
	ProjectID      string         `json:"project_id"`
	Domain         string         `json:"domain"`
	Provider       string         `json:"provider"`
	ConnectionID   int64          `json:"connection_id"`
	State          string         `json:"state"`
	ProviderStatus string         `json:"provider_status"`
	Epoch          int            `json:"epoch"`
	Raw            map[string]any `json:"raw"`
	CheckedAt      string         `json:"checked_at"`
	NextCheckAt    int64          `json:"next_check_at"`
	Error          string         `json:"error"`
	Fingerprint    string         `json:"-"`
}

func listingGet(db *sql.DB, pid, domain string, conn int64) (*ListingSnapshot, error) {
	var s ListingSnapshot
	var raw string
	err := db.QueryRow(`SELECT id,project_id,domain,provider_slug,connection_id,state,provider_status,epoch,snapshot_json,checked_at,next_check_at,last_error,fingerprint FROM domain_listing_snapshots WHERE project_id=? AND domain=? AND connection_id=?`, pid, domain, conn).Scan(&s.ID, &s.ProjectID, &s.Domain, &s.Provider, &s.ConnectionID, &s.State, &s.ProviderStatus, &s.Epoch, &raw, &s.CheckedAt, &s.NextCheckAt, &s.Error, &s.Fingerprint)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal([]byte(raw), &s.Raw) != nil {
		return nil, errors.New("invalid saved listing")
	}
	return &s, nil
}

var moneyAmount = regexp.MustCompile(`^[0-9]{1,15}(\.[0-9]{1,2})?$`)
var moneyCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

func saleMoney(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("money object missing")
	}
	amount := strArg(m, "amount")
	currency := strArg(m, "currency")
	if !moneyAmount.MatchString(amount) || !moneyCurrency.MatchString(currency) {
		return nil, errors.New("invalid money amount or currency")
	}
	cents, err := decimalCents(amount)
	if err != nil && strings.Trim(amount, "0.") != "" {
		return nil, err
	}
	return map[string]any{"amount": fmt.Sprintf("%d.%02d", cents/100, cents%100), "currency": currency}, nil
}

func normalizeListing(provider, domain string, raw map[string]any) (string, string, map[string]any, error) {
	clean := map[string]any{"name": domain}
	if provider == "spaceship" {
		if strArg(raw, "name") != domain {
			return "", "", nil, errors.New("listing domain mismatch")
		}
		status := strArg(raw, "status")
		state := "unknown"
		switch status {
		case "onSale":
			state = "on_sale"
		case "verifying":
			state = "verifying"
		case "onSaleStopped":
			state = "unlisted"
		case "saleProcessing", "privateSale":
			state = "processing"
		case "leaseActive":
			state = "lease_active"
		case "sold":
			state = "sold"
		case "failed":
			state = "failed"
		default:
			if status == "" {
				return "", "", nil, errors.New("listing status missing")
			}
		}
		clean["status"] = status
		for _, key := range []string{"binPriceEnabled", "minPriceEnabled", "description", "displayName"} {
			if v, ok := raw[key]; ok {
				clean[key] = v
			}
		}
		for _, key := range []string{"binPrice", "minPrice"} {
			if v, ok := raw[key]; ok && v != nil {
				m, e := saleMoney(v)
				if e != nil {
					return "", "", nil, e
				}
				clean[key] = m
			}
		}
		return state, status, clean, nil
	}
	// Dynadot exposes listing presence, not a completed-sale report.
	if provider == "dynadot" {
		value, ok := findValue(raw, "isForSale")
		if !ok {
			return "", "", nil, errors.New("Dynadot omitted isForSale; sale status is unconfirmed")
		}
		flag := strings.ToLower(fmt.Sprint(value))
		var state string
		switch flag {
		case "true", "yes", "1":
			state = "on_sale"
		case "false", "no", "0":
			state = "unlisted"
		default:
			return "", "", nil, errors.New("invalid Dynadot isForSale")
		}
		clean["isForSale"] = state == "on_sale"
		return state, flag, clean, nil
	}
	return "", "", nil, errors.New("unsupported sales provider")
}

func findValue(v any, key string) (any, bool) {
	switch x := v.(type) {
	case map[string]any:
		if val, ok := x[key]; ok {
			return val, true
		}
		for _, v := range x {
			if val, ok := findValue(v, key); ok {
				return val, true
			}
		}
	case []any:
		for _, v := range x {
			if val, ok := findValue(v, key); ok {
				return val, true
			}
		}
	}
	return nil, false
}

func saveListing(ctx *sdk.AppCtx, pid, domain string, b *sdk.BoundIntegration, state, status string, clean map[string]any, source string) (*ListingSnapshot, error) {
	unlock, err := acquireDNSMutation(ctx.Done(), fmt.Sprintf("listing:%s:%s:%d", pid, domain, b.ConnectionID))
	if err != nil {
		return nil, err
	}
	defer unlock()
	old, err := listingGet(ctx.AppDB(), pid, domain, b.ConnectionID)
	if err != nil {
		return nil, err
	}
	epoch := 1
	if old != nil {
		epoch = old.Epoch
		if old.State == "sold" && state != "sold" {
			if source == "publish" {
				epoch++
			} else {
				state = old.State
				status = old.ProviderStatus
				clean = old.Raw
			}
		}
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(state+"|"+status+"|"+string(raw))))
	now := time.Now().UTC()
	changed := old == nil || old.Fingerprint != fingerprint || old.Epoch != epoch
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO domain_listing_snapshots(project_id,domain,provider_slug,connection_id,state,provider_status,epoch,snapshot_json,fingerprint,checked_at,next_check_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,domain,connection_id) DO UPDATE SET state=excluded.state,provider_status=excluded.provider_status,epoch=excluded.epoch,snapshot_json=excluded.snapshot_json,fingerprint=excluded.fingerprint,checked_at=excluded.checked_at,next_check_at=excluded.next_check_at,last_error='',failure_count=0,updated_at=CURRENT_TIMESTAMP`, pid, domain, b.AppSlug, b.ConnectionID, state, status, epoch, string(raw), fingerprint, now.Format(time.RFC3339Nano), now.Add(5*time.Minute).Unix())
	if err != nil {
		return nil, err
	}
	var id int64
	if err = tx.QueryRow(`SELECT id FROM domain_listing_snapshots WHERE project_id=? AND domain=? AND connection_id=?`, pid, domain, b.ConnectionID).Scan(&id); err != nil {
		return nil, err
	}
	data := map[string]any{"provider": b.AppSlug, "connection_id": b.ConnectionID, "listing_id": id, "state": state, "provider_status": status, "source": source, "epoch": epoch, "listing": clean}
	if changed {
		if _, err = tx.Exec(`INSERT INTO domain_listing_history(listing_id,project_id,domain,state,provider_status,source,snapshot_json,observed_at) VALUES(?,?,?,?,?,?,?,?)`, id, pid, domain, state, status, source, string(raw), now.Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
		key, err := randomToken(16)
		if err != nil {
			return nil, err
		}
		if err = queueDomainEvent(tx, pid, domain, "domain.listing.updated", "listing-change:"+key, data); err != nil {
			return nil, err
		}
		if source == "publish" || source == "remove" {
			topic := "domain.listing.published"
			if source == "remove" {
				topic = "domain.listing.removed"
			}
			if err = queueDomainEvent(tx, pid, domain, topic, "listing-action:"+key, data); err != nil {
				return nil, err
			}
		}
	}
	if state == "sold" {
		evidence := map[string]any{"endpoint": "get_sellerhub_domain", "status": "sold", "name": domain}
		if e, ok := clean["sale_evidence"].(map[string]any); ok {
			evidence = e
		}
		soldAt, price, payout, currency := "", "", "", ""
		soldAt = strArg(clean, "saleDateTime")
		if m, ok := clean["salePrice"].(map[string]any); ok {
			price = strArg(m, "amount")
			currency = strArg(m, "currency")
			data["sale_price"] = m
		}
		if m, ok := clean["payout"].(map[string]any); ok {
			payout = strArg(m, "amount")
			data["payout"] = m
		}
		if soldAt != "" {
			data["sold_at"] = soldAt
		}
		ev, _ := json.Marshal(evidence)
		if _, err = tx.Exec(`INSERT INTO domain_sales(project_id,listing_id,domain,provider_slug,connection_id,epoch,sale_key,evidence_json,confirmed_at,sold_at,sale_price,payout,currency) VALUES(?,?,?,?,?,?,'status',?,?,?,?,?,?) ON CONFLICT(listing_id,epoch,sale_key) DO UPDATE SET evidence_json=CASE WHEN excluded.sold_at!='' THEN excluded.evidence_json ELSE domain_sales.evidence_json END,sold_at=COALESCE(NULLIF(excluded.sold_at,''),domain_sales.sold_at),sale_price=COALESCE(NULLIF(excluded.sale_price,''),domain_sales.sale_price),payout=COALESCE(NULLIF(excluded.payout,''),domain_sales.payout),currency=COALESCE(NULLIF(excluded.currency,''),domain_sales.currency)`, pid, id, domain, b.AppSlug, b.ConnectionID, epoch, string(ev), now.Format(time.RFC3339Nano), soldAt, price, payout, currency); err != nil {
			return nil, err
		}
		data["evidence"] = evidence
		if err = queueDomainEvent(tx, pid, domain, "domain.sold", fmt.Sprintf("sale-confirmed:%d:%d", id, epoch), data); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	deliverDomainEvents(ctx, pid)
	return listingGet(ctx.AppDB(), pid, domain, b.ConnectionID)
}

func listingFailure(ctx *sdk.AppCtx, pid, domain string, b *sdk.BoundIntegration, err error) {
	// Keep the last confirmed state on transient failures; never infer a sale.
	old, _ := listingGet(ctx.AppDB(), pid, domain, b.ConnectionID)
	if old == nil {
		_, _ = saveListing(ctx, pid, domain, b, "unknown", "unknown", map[string]any{"name": domain}, "sync")
		old, _ = listingGet(ctx.AppDB(), pid, domain, b.ConnectionID)
	}
	if old == nil {
		return
	}
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec(`UPDATE domain_listing_snapshots SET last_error=?,failure_count=failure_count+1,checked_at=?,next_check_at=unixepoch()+MIN(3600,300*(1<<MIN(failure_count,4))) WHERE id=?`, truncate(err.Error(), 400), time.Now().UTC().Format(time.RFC3339Nano), old.ID)
	if e != nil {
		return
	}
	if old.Error != truncate(err.Error(), 400) {
		key, _ := randomToken(16)
		if e = queueDomainEvent(tx, pid, domain, "domain.sync.failed", "sync-failed:"+key, map[string]any{"provider": b.AppSlug, "connection_id": b.ConnectionID, "kind": "listing", "state": old.State}); e != nil {
			return
		}
	}
	if tx.Commit() == nil {
		deliverDomainEvents(ctx, pid)
	}

}

func snapshotResult(s *ListingSnapshot) map[string]any {
	return map[string]any{"provider": s.Provider, "domain": s.Domain, "status": s.ProviderStatus, "state": s.State, "listed": s.State == "on_sale", "raw": s.Raw, "snapshot": s, "sale_detection": map[bool]string{true: "provider_confirmed", false: "unavailable"}[s.Provider == "spaceship"]}
}
