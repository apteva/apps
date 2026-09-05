package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Keyed locks are process-local scheduling aids; database constraints remain
// the authority for ownership across processes. References are removed when
// no holder or waiter remains, so a large fleet does not leak lock entries.
type keyedLock struct {
	sem  chan struct{}
	refs int
}

var resourceLocks = struct {
	sync.Mutex
	entries map[string]*keyedLock
}{entries: map[string]*keyedLock{}}

func lockResource(ctx context.Context, key string) (func(), error) {
	resourceLocks.Lock()
	entry := resourceLocks.entries[key]
	if entry == nil {
		entry = &keyedLock{sem: make(chan struct{}, 1)}
		resourceLocks.entries[key] = entry
	}
	entry.refs++
	resourceLocks.Unlock()
	releaseRef := func() {
		resourceLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(resourceLocks.entries, key)
		}
		resourceLocks.Unlock()
	}
	select {
	case entry.sem <- struct{}{}:
		return func() { <-entry.sem; releaseRef() }, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

func (s *store) reserveManagementPort(t *Tenant) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = reserveManagementPortTx(tx, t); err != nil {
		return err
	}
	return tx.Commit()
}
func reserveManagementPortTx(tx *sql.Tx, t *Tenant) error {
	if t.Kind != KindLocal {
		return nil
	}
	port := portFromTenant(t)
	if port == 0 {
		return nil
	}
	rows, err := tx.Query(`SELECT id,base_url FROM fleet_tenants WHERE kind='local' AND instance_id=? AND id!=?`, t.InstanceID, t.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, base string
		if err = rows.Scan(&id, &base); err != nil {
			rows.Close()
			return err
		}
		otherPort, _ := portFromBaseURL(base)
		if port == otherPort {
			rows.Close()
			return fmt.Errorf("port %d is owned by tenant %s", port, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO fleet_port_reservations(instance_id,port,tenant_id,purpose) VALUES(?,?,?,'management') ON CONFLICT(instance_id,port) DO NOTHING`, t.InstanceID, port, t.ID); err != nil {
		return err
	}
	var owner string
	if err = tx.QueryRow(`SELECT tenant_id FROM fleet_port_reservations WHERE instance_id=? AND port=?`, t.InstanceID, port).Scan(&owner); err != nil {
		return err
	}
	if owner != t.ID {
		return fmt.Errorf("port %d is reserved by another tenant", port)
	}
	return nil
}

func (a *App) reserveAppPortBlock(id string, host int64, managementPort int) (int, error) {
	done, err := lockResource(context.Background(), fmt.Sprintf("ports:%d", host))
	if err != nil {
		return 0, err
	}
	defer done()
	// Backfill legacy management reservations before choosing an app range.
	tenants, err := a.store.list(map[string]string{})
	if err != nil {
		return 0, err
	}
	for _, t := range tenants {
		if t.Kind == KindLocal && t.InstanceID == host {
			if err = a.store.reserveManagementPort(t); err != nil {
				return 0, err
			}
		}
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var base int
	err = tx.QueryRow(`SELECT base FROM fleet_app_port_blocks WHERE tenant_id=? AND instance_id=?`, id, host).Scan(&base)
	if err == nil {
		return base, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	for candidate := 10000; candidate+999 <= 65000; candidate += 1000 {
		if managementPort >= candidate && managementPort <= candidate+999 {
			continue
		}
		var used int
		if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM fleet_app_port_blocks WHERE instance_id=? AND base=?)+(SELECT COUNT(*) FROM fleet_port_reservations WHERE instance_id=? AND port BETWEEN ? AND ?)`, host, candidate, host, candidate, candidate+999).Scan(&used); err != nil {
			return 0, err
		}
		if used > 0 {
			continue
		}
		if _, err = tx.Exec(`INSERT INTO fleet_app_port_blocks(instance_id,base,tenant_id) VALUES(?,?,?)`, host, candidate, id); err != nil {
			return 0, err
		}
		if err = tx.Commit(); err != nil {
			return 0, err
		}
		return candidate, nil
	}
	return 0, fmt.Errorf("no disjoint application port range available on this host")
}
func applyAppPortEnv(env []string, base int) []string {
	for key, value := range map[string]int{"DEPLOY_RELEASE_PORT_RANGE_START": base, "DEPLOY_RELEASE_PORT_RANGE_END": base + 899, "CODE_DEV_PORT_RANGE_START": base + 900, "CODE_DEV_PORT_RANGE_END": base + 999} {
		env = setEnv(env, key, strconv.Itoa(value))
	}
	return env
}

// Check both legacy records and new reservations, including wildcard zones.
// A global DNS lock makes the external write sequence atomic within Fleet;
// the ownership table prevents conflicting reservations across Fleet runs.
func (a *App) claimHostname(id, hostname, purpose string, wildcard bool) (bool, error) {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	rows, err := a.store.db.Query(`SELECT tenant_id,hostname,wildcard FROM fleet_hostname_owners UNION ALL SELECT id,domain,0 FROM fleet_tenants WHERE domain IS NOT NULL AND domain!='' UNION ALL SELECT tenant_id,hostname,0 FROM fleet_tenant_hosts UNION ALL SELECT tenant_id,domain,wildcard FROM fleet_domain_grants`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var other, name string
		var wild bool
		if err = rows.Scan(&other, &name, &wild); err != nil {
			rows.Close()
			return false, err
		}
		if other != id && (name == hostname || (wild && strings.HasSuffix(hostname, "."+name)) || (wildcard && strings.HasSuffix(name, "."+hostname))) {
			rows.Close()
			return false, fmt.Errorf("hostname or zone %s is already owned by another tenant", hostname)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	result, err := a.store.db.Exec(`INSERT INTO fleet_hostname_owners(hostname,tenant_id,wildcard,purpose) VALUES(?,?,?,?) ON CONFLICT(hostname,purpose) DO NOTHING`, hostname, id, wildcard, purpose)
	if err != nil {
		return false, err
	}
	var owner string
	if err = a.store.db.QueryRow(`SELECT tenant_id FROM fleet_hostname_owners WHERE hostname=? AND purpose=?`, hostname, purpose).Scan(&owner); err != nil {
		return false, err
	}
	if owner != id {
		return false, fmt.Errorf("hostname already reserved")
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}
