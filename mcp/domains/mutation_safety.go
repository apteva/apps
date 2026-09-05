package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type mutationLock struct {
	token chan struct{}
	refs  int
}

var mutationLocks = struct {
	sync.Mutex
	entries map[string]*mutationLock
}{entries: map[string]*mutationLock{}}

// Domain-keyed rather than credential-keyed: two account connections may manage
// the same zone. Locks are removed after the last waiter/owner leaves.
func acquireDNSMutation(done <-chan struct{}, domain string) (func(), error) {
	mutationLocks.Lock()
	l := mutationLocks.entries[domain]
	if l == nil {
		l = &mutationLock{token: make(chan struct{}, 1)}
		l.token <- struct{}{}
		mutationLocks.entries[domain] = l
	}
	l.refs++
	mutationLocks.Unlock()
	releaseRef := func() {
		mutationLocks.Lock()
		l.refs--
		if l.refs == 0 {
			delete(mutationLocks.entries, domain)
		}
		mutationLocks.Unlock()
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-l.token:
		var once sync.Once
		return func() { once.Do(func() { l.token <- struct{}{}; releaseRef() }) }, nil
	case <-done:
		releaseRef()
		return nil, errors.New("DNS operation cancelled")
	case <-timer.C:
		releaseRef()
		return nil, apiError(409, "DNS mutation busy; retry later")
	}
}
func lockDNSMutation(_ int64, domain string) func() {
	unlock, err := acquireDNSMutation(nil, domain)
	if err != nil {
		panic(err)
	}
	return unlock
}

func (p *spaceshipProvider) replace(ctx *sdk.AppCtx, domain string, old DNSRecord, item map[string]any) error {
	token, err := randomToken(16)
	if err != nil {
		return err
	}
	oldItem := spaceshipRestoreItem(old)
	oldJSON, _ := json.Marshal(oldItem)
	newJSON, _ := json.Marshal(item)
	_, err = ctx.AppDB().Exec(`INSERT INTO dns_recoveries(id,project_id,connection_id,domain,previous_json,desired_json) VALUES(?,?,?,?,?,?)`, token, ctx.CurrentProject(), p.bound.ConnectionID, domain, string(oldJSON), string(newJSON))
	if err != nil {
		return fmt.Errorf("persist recovery before DNS replacement: %w", err)
	}
	mark := func(status string, cause error) error {
		message := ""
		if cause != nil {
			message = cause.Error()
		}
		_, err := ctx.AppDB().Exec(`UPDATE dns_recoveries SET status=?,error_message=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, status, message, token)
		return err
	}
	if _, err = providerCall(ctx, p.bound, "delete_dns_records", map[string]any{"domain": domain, "records": []any{spaceshipDeleteItem(old)}}); err != nil {
		_ = mark("unknown", err)
		return fmt.Errorf("delete outcome uncertain; inspect recovery %s before retrying: %w", token, err)
	}
	if _, err = providerCall(ctx, p.bound, "save_dns_records", map[string]any{"domain": domain, "items": []any{item}}); err != nil {
		_, restoreErr := providerCall(ctx, p.bound, "save_dns_records", map[string]any{"domain": domain, "items": []any{oldItem}})
		if restoreErr != nil {
			combined := fmt.Errorf("replacement failed: %v; rollback failed: %v", err, restoreErr)
			_ = mark("unknown", combined)
			return fmt.Errorf("recovery %s requires attention: %w", token, combined)
		}
		// A timed-out save might have applied. A successful restore does not prove
		// that the new record is absent: keep the operation unresolved for inspection.
		_ = mark("unknown", err)
		return fmt.Errorf("old value restored; replacement outcome uncertain, inspect recovery %s: %w", token, err)
	}
	after, err := p.List(ctx, domain)
	if err == nil {
		desired := DNSRecord{Type: fmt.Sprint(item["type"]), Name: spaceshipOwner(item, domain), TTL: spaceshipIntField(item, "ttl")}
		desired.Value, desired.Prio = spaceshipRecordValue(desired.Type, item)
		if !hasExactDesiredRecord(after, domain, normaliseSubaddress(desired.Name), desired.Type, desired.Value, desired.TTL, desired.Prio) {
			err = errors.New("provider did not return the desired record")
		}
		if recordValueEqual(old.Type, old.Value, desired.Value) && old.Prio == desired.Prio {
		} else {
			for _, r := range recordsAtName(after, domain, normaliseSubaddress(desired.Name), old.Type) {
				if recordValueEqual(old.Type, r.Value, old.Value) && r.Prio == old.Prio {
					err = errors.New("previous DNS value remains after replacement")
				}
			}
		}
	}
	if err != nil {
		_ = mark("unknown", err)
		return fmt.Errorf("replacement not verified; inspect recovery %s: %w", token, err)
	}
	return mark("succeeded", nil)
}
func spaceshipRestoreItem(r DNSRecord) map[string]any {
	if len(r.Raw) > 0 {
		m := copyStringAnyMap(r.Raw)
		delete(m, "group")
		delete(m, "id")
		delete(m, "recordId")
		return m
	}
	value := r.Value
	if r.Type == "MX" || r.Type == "SRV" {
		value = fmt.Sprintf("%d %s", r.Prio, r.Value)
	}
	m, _ := spaceshipRecordItem("", normaliseSubaddress(r.Name), r.Type, value, r.TTL)
	return m
}

func (n *namecheapProvider) checkSnapshot(ctx *sdk.AppCtx, domain string, expected []DNSRecord) error {
	for _, r := range expected {
		if (r.Type == "MX" || r.Type == "MXE") && strArg(r.Raw, "email_type") == "" && n.emailTypeOverride == "" {
			return errors.New("Namecheap omitted mail routing mode; refusing a potentially lossy zone replacement. Select its current mail routing mode explicitly with namecheap_email_type before editing.")
		}
	}
	current, err := n.List(ctx, domain)
	if err != nil {
		return err
	}
	canonical := func(records []DNSRecord) string {
		items := make([]string, 0, len(records))
		for _, r := range records {
			b, _ := json.Marshal(r)
			items = append(items, string(b))
		}
		sort.Strings(items)
		b, _ := json.Marshal(items)
		return string(b)
	}
	a, b := canonical(expected), canonical(current)
	if string(a) != string(b) {
		return apiError(409, "Namecheap zone changed during the operation; refresh before retrying")
	}
	return nil
}

// Status/reconciliation are explicit, project-scoped operations. Reconciliation
// observes current state; it never guesses whether an interrupted write is safe
// to repeat or restores an old value over a later operator change.
func (a *App) toolDNSRecovery(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if id := strArg(args, "recovery_id"); id != "" {
		var conn int64
		var domain, oldRaw, newRaw string
		err := ctx.AppDB().QueryRow(`SELECT connection_id,domain,previous_json,desired_json FROM dns_recoveries WHERE id=? AND project_id=?`, id, pid).Scan(&conn, &domain, &oldRaw, &newRaw)
		if err != nil {
			return nil, err
		}
		unlock, err := acquireDNSMutation(ctx.Done(), domain)
		if err != nil {
			return nil, err
		}
		defer unlock()
		prov, _, err := a.providerFor(ctx, conn, pid)
		if err != nil {
			return nil, err
		}
		records, err := prov.List(ctx, domain)
		if err != nil {
			return nil, err
		}
		var item map[string]any
		if err = json.Unmarshal([]byte(newRaw), &item); err != nil {
			return nil, err
		}
		value, prio := spaceshipRecordValue(fmt.Sprint(item["type"]), item)
		desiredPresent := hasExactDesiredRecord(records, domain, normaliseSubaddress(spaceshipOwner(item, domain)), fmt.Sprint(item["type"]), value, spaceshipIntField(item, "ttl"), prio)
		var old map[string]any
		if err = json.Unmarshal([]byte(oldRaw), &old); err != nil {
			return nil, err
		}
		ov, op := spaceshipRecordValue(fmt.Sprint(old["type"]), old)
		oldPresent := hasExactDesiredRecord(records, domain, normaliseSubaddress(spaceshipOwner(old, domain)), fmt.Sprint(old["type"]), ov, spaceshipIntField(old, "ttl"), op)
		status := "unknown"
		if desiredPresent && !oldPresent {
			status = "succeeded"
		} else if oldPresent && !desiredPresent {
			status = "restored"
		}
		_, err = ctx.AppDB().Exec(`UPDATE dns_recoveries SET status=?,updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, status, id, pid)
		return map[string]any{"recovery_id": id, "status": status, "desired_present": desiredPresent, "previous_present": oldPresent, "records": records}, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id,connection_id,domain,status,error_message FROM dns_recoveries WHERE project_id=? AND status IN ('pending','unknown') ORDER BY created_at LIMIT 100`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, domain, status, message string
		var conn int64
		if err = rows.Scan(&id, &conn, &domain, &status, &message); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"recovery_id": id, "domain": domain, "connection_id": conn, "status": status, "error": message})
	}
	return map[string]any{"recoveries": out}, rows.Err()
}
