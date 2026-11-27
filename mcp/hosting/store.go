package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ─── Backends, states ────────────────────────────────────────────────────

const (
	BackendContainer       = "container"
	BackendMachinePool     = "machine_pool"
	BackendMachineOnDemand = "machine_on_demand"
)

// Tenant states.
const (
	TenantProvisioning      = "provisioning"
	TenantWaitingForMachine = "waiting_for_machine"
	TenantActive            = "active"
	TenantSuspending        = "suspending"
	TenantSuspended         = "suspended"
	TenantResuming          = "resuming"
	TenantReleasing         = "releasing"
	TenantReleased          = "released"
	TenantFailed            = "failed"
	// TenantDegraded: an app-managed tenant whose device is offline or held
	// by someone else. It returns to active by itself when that is over.
	TenantDegraded = "degraded"
)

// Machine states (pool backend).
const (
	MachineQuarantined = "quarantined"
	MachineAvailable   = "available"
	MachineReserved    = "reserved"
	MachinePreparing   = "preparing"
	MachineAssigned    = "assigned"
	MachineSuspended   = "suspended"
	MachineCleaning    = "cleaning"
	MachineMaintenance = "maintenance"
)

// Operation states.
const (
	OpQueued    = "queued"
	OpRunning   = "running"
	OpWaiting   = "waiting"
	OpSucceeded = "succeeded"
	OpFailed    = "failed"
	OpCancelled = "cancelled"
)

// now is swapped in tests.
var now = func() time.Time { return time.Now().UTC() }

const sqlTimeLayout = "2006-01-02 15:04:05"

func sqlTime(t time.Time) string { return t.UTC().Format(sqlTimeLayout) }

func parseSQLTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{sqlTimeLayout, time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// ─── Profiles ────────────────────────────────────────────────────────────

// Profile is what a caller asks for: where a tenant runs (backend) and what
// runs on it (product).
type Profile struct {
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Backend    string          `json:"backend"`
	ProductKey string          `json:"product_key"`
	PoolKey    string          `json:"pool_key,omitempty"`
	Image      string          `json:"image,omitempty"`
	CPU        float64         `json:"cpu"`
	MemoryMB   int64           `json:"memory_mb"`
	StorageMB  int64           `json:"storage_mb"`
	Settings   json.RawMessage `json:"settings"`
	Status     string          `json:"status"`
	Metadata   json.RawMessage `json:"metadata"`
	// PresaleLimit is how many orders a machine_pool profile takes ahead of
	// hardware. nil: no limit on the waiting line and nothing sold ahead.
	PresaleLimit *int64 `json:"presale_limit,omitempty"`
	PresaleNote  string `json:"presale_note,omitempty"`
	// PresaleReadyDays is the promise made to customers who wait: a waiting
	// tenant's due_at is its order time plus this many days.
	PresaleReadyDays *int64 `json:"presale_ready_days,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// OnDemandSettings is the machine_on_demand part of a profile's settings.
type OnDemandSettings struct {
	Provider             string         `json:"provider"`
	ProviderConnectionID int64          `json:"provider_connection_id,omitempty"`
	ServerType           string         `json:"server_type"`
	Region               string         `json:"region,omitempty"`
	Image                string         `json:"image,omitempty"`
	Platform             string         `json:"platform,omitempty"`
	Setup                map[string]any `json:"setup,omitempty"`
}

func (p *Profile) onDemand() OnDemandSettings {
	var s OnDemandSettings
	_ = json.Unmarshal(p.Settings, &s)
	return s
}

func (p *Profile) isMachine() bool {
	return p.Backend == BackendMachinePool || p.Backend == BackendMachineOnDemand
}

const profileSelect = `SELECT key, name, backend, product_key, COALESCE(pool_key,''), image, cpu, memory_mb, storage_mb, settings_json, status, metadata_json, presale_limit, presale_note, presale_ready_days, created_at, updated_at FROM hosting_profiles`

func scanProfile(row rowScanner) (*Profile, error) {
	var p Profile
	var settings, meta string
	var presale, readyDays sql.NullInt64
	if err := row.Scan(&p.Key, &p.Name, &p.Backend, &p.ProductKey, &p.PoolKey, &p.Image, &p.CPU, &p.MemoryMB, &p.StorageMB, &settings, &p.Status, &meta, &presale, &p.PresaleNote, &readyDays, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if readyDays.Valid {
		v := readyDays.Int64
		p.PresaleReadyDays = &v
	}
	if presale.Valid {
		v := presale.Int64
		p.PresaleLimit = &v
	}
	p.Settings = json.RawMessage(settings)
	p.Metadata = json.RawMessage(meta)
	return &p, nil
}

func dbProfileGet(db querier, key string) (*Profile, error) {
	p, err := scanProfile(db.QueryRow(profileSelect+` WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func requireProfile(db querier, key string) (*Profile, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("profile required")
	}
	p, err := dbProfileGet(db, key)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("profile %q not found", key)
	}
	return p, nil
}

func dbProfileList(db *sql.DB, includeArchived bool) ([]*Profile, error) {
	q := profileSelect
	if !includeArchived {
		q += ` WHERE status='active'`
	}
	rows, err := db.Query(q + ` ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func dbProfileUpsert(db *sql.DB, p *Profile) error {
	_, err := db.Exec(`
		INSERT INTO hosting_profiles (key, name, backend, product_key, pool_key, image, cpu, memory_mb, storage_mb, settings_json, status, metadata_json, presale_limit, presale_note, presale_ready_days)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name=excluded.name, backend=excluded.backend, product_key=excluded.product_key, pool_key=excluded.pool_key,
			image=excluded.image, cpu=excluded.cpu, memory_mb=excluded.memory_mb, storage_mb=excluded.storage_mb,
			settings_json=excluded.settings_json, status=excluded.status, metadata_json=excluded.metadata_json,
			presale_limit=excluded.presale_limit, presale_note=excluded.presale_note, presale_ready_days=excluded.presale_ready_days,
			updated_at=CURRENT_TIMESTAMP`,
		p.Key, p.Name, p.Backend, p.ProductKey, nullStr(p.PoolKey), p.Image, p.CPU, p.MemoryMB, p.StorageMB,
		stringOrJSON(p.Settings, "{}"), firstNonEmpty(p.Status, "active"), stringOrJSON(p.Metadata, "{}"), p.PresaleLimit, p.PresaleNote, p.PresaleReadyDays)
	return err
}

// seedProfiles keeps a container profile for every product so Hosting works
// out of the box without any setup.
func seedProfiles(db *sql.DB, defaultImage string) error {
	defaults := []Profile{
		{Key: "free", Name: "Apteva Free", ProductKey: "apteva", Image: defaultImage, CPU: 0.5, MemoryMB: 512, StorageMB: 512},
		{Key: "starter", Name: "Apteva Starter", ProductKey: "apteva", Image: defaultImage, CPU: 1, MemoryMB: 1024, StorageMB: 10240},
		{Key: "docker-starter", Name: "Docker Starter", ProductKey: "custom-docker", CPU: 1, MemoryMB: 1024, StorageMB: 10240},
		{Key: "wordpress-starter", Name: "WordPress Starter", ProductKey: "wordpress-single", Image: "wordpress:php8.3-apache", CPU: 1, MemoryMB: 1024, StorageMB: 10240},
	}
	for _, p := range defaults {
		if _, err := db.Exec(`
			INSERT OR IGNORE INTO hosting_profiles (key, name, backend, product_key, image, cpu, memory_mb, storage_mb)
			VALUES (?, ?, 'container', ?, ?, ?, ?, ?)`,
			p.Key, p.Name, p.ProductKey, p.Image, p.CPU, p.MemoryMB, p.StorageMB); err != nil {
			return err
		}
	}
	return nil
}

// ─── Pools ───────────────────────────────────────────────────────────────

type Pool struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	// ManagedBy names the app that drives the pool's machines ("mobile" for
	// phones); empty for Instances hosts.
	ManagedBy        string          `json:"managed_by,omitempty"`
	ScriptSet        string          `json:"-"` // always the platform; scripts are picked by platform
	ExecutionEnabled bool            `json:"execution_enabled"`
	Metadata         json.RawMessage `json:"metadata"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}

const poolSelect = `SELECT key, name, platform, managed_by, script_set, execution_enabled, metadata_json, created_at, updated_at FROM hosting_pools`

func scanPool(row rowScanner) (*Pool, error) {
	var p Pool
	var enabled int
	var meta string
	if err := row.Scan(&p.Key, &p.Name, &p.Platform, &p.ManagedBy, &p.ScriptSet, &enabled, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.ExecutionEnabled = enabled != 0
	p.Metadata = json.RawMessage(meta)
	return &p, nil
}

func dbPoolGet(db querier, key string) (*Pool, error) {
	p, err := scanPool(db.QueryRow(poolSelect+` WHERE key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func requirePool(db querier, key string) (*Pool, error) {
	p, err := dbPoolGet(db, key)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("pool %q not found", key)
	}
	return p, nil
}

func dbPoolList(db *sql.DB) ([]*Pool, error) {
	rows, err := db.Query(poolSelect + ` ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Pool
	for rows.Next() {
		p, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─── Machines ────────────────────────────────────────────────────────────

type Machine struct {
	ID         int64  `json:"id"`
	PoolKey    string `json:"pool_key"`
	InstanceID int64  `json:"instance_id,omitempty"`
	// DeviceRef is the managing app's device id on an app-managed pool;
	// DeviceSession the session Hosting holds on it.
	DeviceRef string `json:"device_ref,omitempty"`
	// DeviceSession works like a password for the phone: it is never
	// serialized. Only hosting_tenant_get shows it, to the owning tenant.
	DeviceSession      string            `json:"-"`
	DeviceSessionOwner string            `json:"-"`
	SessionHeld        bool              `json:"session_held,omitempty"`
	ManagedBy          string            `json:"managed_by,omitempty"`
	Name               string            `json:"name"`
	State              string            `json:"state"`
	StateReason        string            `json:"state_reason"`
	Online             bool              `json:"online"`
	LastSeenAt         string            `json:"last_seen_at,omitempty"`
	PublicHost         string            `json:"public_host"`
	PublicPort         int               `json:"public_port"`
	ConnectMode        string            `json:"connect_mode"`
	EndpointSource     string            `json:"endpoint_source"`
	EndpointIssue      string            `json:"endpoint_issue,omitempty"`
	Serial             string            `json:"serial"`
	ExpectedOS         string            `json:"expected_os"`
	ManagementUser     string            `json:"management_user"`
	ScriptHashes       map[string]string `json:"script_hashes"`
	Validated          bool              `json:"validated"`
	TenantID           string            `json:"tenant_id,omitempty"`
	Metadata           json.RawMessage   `json:"metadata"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`
}

const machineSelect = `SELECT id, pool_key, COALESCE(instance_id,0), device_ref, device_session, device_session_owner,
	COALESCE((SELECT p.managed_by FROM hosting_pools p WHERE p.key=hosting_machines.pool_key),''), name, state, state_reason, online, COALESCE(last_seen_at,''), public_host, public_port,
	connect_mode, endpoint_source, endpoint_issue, serial, expected_os, management_user, script_hashes_json, validated, COALESCE(tenant_id,''), metadata_json, created_at, updated_at FROM hosting_machines`

func scanMachine(row rowScanner) (*Machine, error) {
	var m Machine
	var online, validated int
	var hashes, meta string
	if err := row.Scan(&m.ID, &m.PoolKey, &m.InstanceID, &m.DeviceRef, &m.DeviceSession, &m.DeviceSessionOwner, &m.ManagedBy, &m.Name, &m.State, &m.StateReason, &online, &m.LastSeenAt, &m.PublicHost, &m.PublicPort,
		&m.ConnectMode, &m.EndpointSource, &m.EndpointIssue, &m.Serial, &m.ExpectedOS, &m.ManagementUser, &hashes, &validated, &m.TenantID, &meta, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Online = online != 0
	m.SessionHeld = m.DeviceSession != ""
	m.Validated = validated != 0
	m.ScriptHashes = map[string]string{}
	_ = json.Unmarshal([]byte(hashes), &m.ScriptHashes)
	m.Metadata = json.RawMessage(meta)
	return &m, nil
}

func dbMachineGet(db querier, id int64) (*Machine, error) {
	m, err := scanMachine(db.QueryRow(machineSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

func requireMachine(db querier, id int64) (*Machine, error) {
	if id <= 0 {
		return nil, errors.New("machine_id required")
	}
	m, err := dbMachineGet(db, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("machine %d not found", id)
	}
	return m, nil
}

func dbMachineList(db *sql.DB, poolKey, state string) ([]*Machine, error) {
	q := machineSelect + ` WHERE 1=1`
	var args []any
	if poolKey != "" {
		q += ` AND pool_key=?`
		args = append(args, poolKey)
	}
	if state != "" {
		q += ` AND state=?`
		args = append(args, state)
	}
	rows, err := db.Query(q+` ORDER BY pool_key, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func dbMachineSetState(db querier, id int64, state, reason string) error {
	_, err := db.Exec(`UPDATE hosting_machines SET state=?, state_reason=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, state, reason, id)
	return err
}

// endpointOK: customers can reach the machine. An app-managed device is
// reached through its managing app, so it has no SSH endpoint.
const endpointOK = `(m.device_ref != '' OR (m.public_host != '' AND m.endpoint_issue = ''))`

// forSaleCondition is the SQL predicate for a machine Hosting may hand to a
// new tenant: available, online, validated, in a pool with execution on.
const forSaleCondition = `m.state='available' AND m.online=1 AND m.validated=1 AND m.tenant_id IS NULL
	AND ` + endpointOK + `
	AND EXISTS (SELECT 1 FROM hosting_pools p WHERE p.key=m.pool_key AND p.execution_enabled=1)`

func (m *Machine) managed() bool { return m != nil && m.DeviceRef != "" }

// ─── Tenants ─────────────────────────────────────────────────────────────

type Tenant struct {
	ID               string          `json:"id"`
	ExternalRef      string          `json:"external_ref"`
	ProfileKey       string          `json:"profile_key"`
	Backend          string          `json:"backend"`
	WorkloadID       string          `json:"workload_id,omitempty"`
	MachineID        int64           `json:"machine_id,omitempty"`
	InstanceID       int64           `json:"instance_id,omitempty"`
	ReservationID    string          `json:"reservation_id,omitempty"`
	Slug             string          `json:"slug"`
	DefaultHostname  string          `json:"default_hostname,omitempty"`
	OwnerEmail       string          `json:"owner_email"`
	SSHUser          string          `json:"ssh_user,omitempty"`
	AccessKeys       []string        `json:"access_keys,omitempty"`
	Status           string          `json:"status"`
	AptevaVersion    string          `json:"apteva_version,omitempty"`
	Image            string          `json:"image,omitempty"`
	LastHealthStatus string          `json:"last_health_status"`
	LastError        string          `json:"last_error"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	Access           *TenantAccess   `json:"access,omitempty"`
	// Device is set for a tenant on an app-managed pool (a phone): the
	// customer uses the managing app with this session.
	Device     *TenantDevice `json:"device,omitempty"`
	ReleasedAt string        `json:"released_at,omitempty"`
	// DueAt is when a tenant that had to wait was promised its machine
	// (order time + the profile's presale_ready_days).
	DueAt     string `json:"due_at,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// TenantDevice is how a customer reaches a rented phone: Mobile's tools
// with session_id on device_ref. The session id is a bearer: whoever has it
// drives the phone.
type TenantDevice struct {
	ManagedBy string `json:"managed_by"`
	DeviceRef string `json:"device_ref"`
	// SessionID is filled only by hosting_tenant_get (revealSession).
	SessionID   string `json:"session_id,omitempty"`
	SessionHeld bool   `json:"session_held"`
	Online      bool   `json:"online"`
	session     string
}

// revealSession puts the phone's session id on the tenant when the session
// is the tenant's own and the rental is live. Called by hosting_tenant_get
// only, so lists, machine views and events never carry it.
func (t *Tenant) revealSession() {
	if t != nil && t.Device != nil && t.Device.SessionHeld && (t.Status == TenantActive || t.Status == TenantDegraded) {
		t.Device.SessionID = t.Device.session
	}
}

// TenantAccess is what storefronts show the customer.
type TenantAccess struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	Command string `json:"ssh_command"`
	Issue   string `json:"issue,omitempty"` // why customers cannot reach the machine
	Granted bool   `json:"granted"`
}

const tenantSelect = `SELECT id, external_ref, profile_key, backend, workload_id, COALESCE(machine_id,0), COALESCE(instance_id,0), COALESCE(reservation_id,''),
	slug, COALESCE(default_hostname,''), owner_email, ssh_user, access_keys_json, status, apteva_version, image, last_health_status, last_error,
	metadata_json, COALESCE(released_at,''), created_at, updated_at, COALESCE(due_at,''),
	COALESCE((SELECT hm.public_host FROM hosting_machines hm WHERE hm.id = hosting_tenants.machine_id), ''),
	COALESCE((SELECT hm.public_port FROM hosting_machines hm WHERE hm.id = hosting_tenants.machine_id), 0),
	COALESCE((SELECT hm.endpoint_issue FROM hosting_machines hm WHERE hm.id = hosting_tenants.machine_id), ''),
	COALESCE((SELECT hm.device_ref || char(9) || hm.device_session || char(9) || hm.online || char(9) || p.managed_by || char(9) || hm.device_session_owner
	  FROM hosting_machines hm JOIN hosting_pools p ON p.key = hm.pool_key WHERE hm.id = hosting_tenants.machine_id AND hm.device_ref != ''), '')
	FROM hosting_tenants`

func scanTenant(row rowScanner) (*Tenant, error) {
	var t Tenant
	var keys, meta, machineHost, machineIssue, device string
	var machinePort int
	if err := row.Scan(&t.ID, &t.ExternalRef, &t.ProfileKey, &t.Backend, &t.WorkloadID, &t.MachineID, &t.InstanceID, &t.ReservationID,
		&t.Slug, &t.DefaultHostname, &t.OwnerEmail, &t.SSHUser, &keys, &t.Status, &t.AptevaVersion, &t.Image, &t.LastHealthStatus, &t.LastError,
		&meta, &t.ReleasedAt, &t.CreatedAt, &t.UpdatedAt, &t.DueAt, &machineHost, &machinePort, &machineIssue, &device); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(keys), &t.AccessKeys)
	t.Metadata = json.RawMessage(meta)
	md := mapFromAny(t.Metadata)
	// A pool tenant's endpoint is read from its machine, so a moved tunnel
	// port or an operator's fix reaches the customer at once. On-demand
	// tenants keep the endpoint found when their instance became ready.
	host, port, issue := strArg(md, "access_host"), intArg(md, "access_port", 22), ""
	if t.MachineID > 0 {
		host, port, issue = machineHost, machinePort, machineIssue
	}
	if parts := strings.Split(device, "\t"); len(parts) == 5 {
		t.Device = &TenantDevice{DeviceRef: parts[0], Online: parts[2] == "1", ManagedBy: parts[3]}
		if parts[1] != "" && parts[4] == t.ID {
			t.Device.session, t.Device.SessionHeld = parts[1], true
		}
	}
	if t.SSHUser != "" && (host != "" || issue != "") {
		t.Access = &TenantAccess{Host: host, Port: port, User: t.SSHUser, Issue: issue, Granted: boolFromAny(md["access_granted"], false)}
		if host != "" {
			t.Access.Command = fmt.Sprintf("ssh %s@%s -p %d", t.SSHUser, host, port)
		}
	}
	return &t, nil
}

func dbTenantGet(db querier, id string) (*Tenant, error) {
	t, err := scanTenant(db.QueryRow(tenantSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// dbTenantByRef returns the live (not released) tenant for a caller reference.
func dbTenantByRef(db querier, ref string) (*Tenant, error) {
	t, err := scanTenant(db.QueryRow(tenantSelect+` WHERE external_ref=? AND status!='released' ORDER BY created_at DESC LIMIT 1`, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// resolveTenant accepts tenant_id or external_ref.
func resolveTenant(db querier, args map[string]any) (*Tenant, error) {
	if id := strArg(args, "tenant_id"); id != "" {
		t, err := dbTenantGet(db, id)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, fmt.Errorf("tenant %q not found", id)
		}
		return t, nil
	}
	if ref := strArg(args, "external_ref"); ref != "" {
		t, err := dbTenantByRef(db, ref)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, fmt.Errorf("no live tenant for external_ref %q", ref)
		}
		return t, nil
	}
	return nil, errors.New("tenant_id or external_ref required")
}

func dbTenantList(db *sql.DB, args map[string]any) ([]*Tenant, error) {
	q := tenantSelect + ` WHERE 1=1`
	var params []any
	for _, f := range []struct{ arg, col string }{{"status", "status"}, {"profile_key", "profile_key"}, {"backend", "backend"}, {"owner_email", "owner_email"}, {"external_ref", "external_ref"}} {
		if v := strArg(args, f.arg); v != "" {
			q += ` AND ` + f.col + `=?`
			params = append(params, v)
		}
	}
	if !boolArg(args, "include_released") && strArg(args, "status") == "" {
		q += ` AND status!='released'`
	}
	limit := intArg(args, "limit", 200)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := db.Query(q+fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func dbTenantInsert(db querier, t *Tenant) error {
	var host any
	if t.DefaultHostname != "" {
		host = t.DefaultHostname
	}
	_, err := db.Exec(`INSERT INTO hosting_tenants
		(id, external_ref, profile_key, backend, workload_id, machine_id, instance_id, reservation_id, slug, default_hostname, owner_email,
		 ssh_user, access_keys_json, status, apteva_version, image, metadata_json, due_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))`,
		t.ID, t.ExternalRef, t.ProfileKey, t.Backend, t.WorkloadID, nullInt(t.MachineID), nullInt(t.InstanceID), nullStr(t.ReservationID),
		t.Slug, host, t.OwnerEmail, t.SSHUser, jsonOrEmpty(t.AccessKeys, "[]"), t.Status, t.AptevaVersion, t.Image, stringOrJSON(t.Metadata, "{}"), t.DueAt)
	return err
}

func dbTenantSetStatus(db querier, id, status, errMsg string) error {
	extra := ""
	if status == TenantReleased {
		extra = ", released_at=CURRENT_TIMESTAMP"
	}
	_, err := db.Exec(`UPDATE hosting_tenants SET status=?, last_error=?, updated_at=CURRENT_TIMESTAMP`+extra+` WHERE id=?`, status, errMsg, id)
	return err
}

func dbTenantSetError(db querier, id, errMsg string) error {
	_, err := db.Exec(`UPDATE hosting_tenants SET last_error=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, errMsg, id)
	return err
}

func dbTenantMergeMetadata(db querier, tenantID string, patch map[string]any) error {
	var raw string
	if err := db.QueryRow(`SELECT metadata_json FROM hosting_tenants WHERE id=?`, tenantID).Scan(&raw); err != nil {
		return err
	}
	merged := mergeMaps(mapFromAny(json.RawMessage(raw)), patch)
	_, err := db.Exec(`UPDATE hosting_tenants SET metadata_json=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, jsonOrEmpty(merged, "{}"), tenantID)
	return err
}

// ─── Reservations ────────────────────────────────────────────────────────

type Reservation struct {
	ID         string `json:"id"`
	ProfileKey string `json:"profile_key"`
	MachineID  int64  `json:"machine_id,omitempty"`
	// Presale is a hold without a machine, counted against the profile's
	// presale_limit.
	Presale     bool   `json:"presale,omitempty"`
	ExternalRef string `json:"external_ref"`
	Status      string `json:"status"`
	ExpiresAt   string `json:"expires_at"`
	TenantID    string `json:"tenant_id,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

const reservationSelect = `SELECT id, profile_key, COALESCE(machine_id,0), external_ref, status, expires_at, COALESCE(tenant_id,''), created_at, updated_at FROM hosting_reservations`

func scanReservation(row rowScanner) (*Reservation, error) {
	var r Reservation
	if err := row.Scan(&r.ID, &r.ProfileKey, &r.MachineID, &r.ExternalRef, &r.Status, &r.ExpiresAt, &r.TenantID, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.Presale = r.MachineID == 0
	return &r, nil
}

func dbReservationGet(db querier, id string) (*Reservation, error) {
	r, err := scanReservation(db.QueryRow(reservationSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// ─── Operations ──────────────────────────────────────────────────────────

type Operation struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	TenantID      string          `json:"tenant_id,omitempty"`
	MachineID     int64           `json:"machine_id,omitempty"`
	InstanceID    int64           `json:"instance_id,omitempty"`
	Steps         []string        `json:"steps"`
	Step          int             `json:"step"`
	Status        string          `json:"status"`
	LeaseToken    string          `json:"-"`
	LeaseUntil    string          `json:"lease_until,omitempty"`
	NotBefore     string          `json:"not_before,omitempty"`
	StepStartedAt string          `json:"step_started_at,omitempty"`
	Attempts      int             `json:"attempts"`
	Error         string          `json:"error"`
	Log           []OperationLog  `json:"log"`
	Actor         string          `json:"actor"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	FinishedAt    string          `json:"finished_at,omitempty"`
	Current       string          `json:"current_step,omitempty"`
	Extra         json.RawMessage `json:"-"`
}

type OperationLog struct {
	At     string `json:"at"`
	Step   string `json:"step"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

const operationSelect = `SELECT id, kind, COALESCE(tenant_id,''), COALESCE(machine_id,0), COALESCE(instance_id,0), steps_json, step, status, lease_token,
	COALESCE(lease_until,''), COALESCE(not_before,''), COALESCE(step_started_at,''), attempts, error, log_json, actor, created_at, updated_at, COALESCE(finished_at,'')
	FROM hosting_operations`

func scanOperation(row rowScanner) (*Operation, error) {
	var o Operation
	var steps, logs string
	if err := row.Scan(&o.ID, &o.Kind, &o.TenantID, &o.MachineID, &o.InstanceID, &steps, &o.Step, &o.Status, &o.LeaseToken,
		&o.LeaseUntil, &o.NotBefore, &o.StepStartedAt, &o.Attempts, &o.Error, &logs, &o.Actor, &o.CreatedAt, &o.UpdatedAt, &o.FinishedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(steps), &o.Steps)
	_ = json.Unmarshal([]byte(logs), &o.Log)
	if o.Log == nil {
		o.Log = []OperationLog{}
	}
	if o.Step < len(o.Steps) {
		o.Current = o.Steps[o.Step]
	}
	return &o, nil
}

func dbOperationGet(db querier, id string) (*Operation, error) {
	o, err := scanOperation(db.QueryRow(operationSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

func dbOperationList(db *sql.DB, args map[string]any) ([]*Operation, error) {
	q := operationSelect + ` WHERE 1=1`
	var params []any
	if v := strArg(args, "tenant_id"); v != "" {
		q += ` AND tenant_id=?`
		params = append(params, v)
	}
	if v := int64Arg(args, "machine_id"); v > 0 {
		q += ` AND machine_id=?`
		params = append(params, v)
	}
	if v := strArg(args, "status"); v != "" {
		q += ` AND status=?`
		params = append(params, v)
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.Query(q+fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit), params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Operation
	for rows.Next() {
		o, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// activeOperationFor returns the unfinished run for a tenant or machine, if any.
func activeOperationFor(db querier, tenantID string, machineID int64) (*Operation, error) {
	q := operationSelect + ` WHERE status IN ('queued','running','waiting') AND (`
	var params []any
	var parts []string
	if tenantID != "" {
		parts = append(parts, `tenant_id=?`)
		params = append(params, tenantID)
	}
	if machineID > 0 {
		parts = append(parts, `machine_id=?`)
		params = append(params, machineID)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	o, err := scanOperation(db.QueryRow(q+strings.Join(parts, " OR ")+`) ORDER BY created_at DESC, rowid DESC LIMIT 1`, params...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

// ─── Events ──────────────────────────────────────────────────────────────

func recordEvent(db querier, tenantID, kind, actorName string, payload any) error {
	_, err := db.Exec(`INSERT INTO hosting_events (tenant_id, kind, actor, payload_json) VALUES (?, ?, ?, ?)`,
		tenantID, kind, actorName, jsonOrEmpty(payload, "{}"))
	return err
}

type HostingEvent struct {
	ID        int64           `json:"id"`
	TenantID  string          `json:"tenant_id"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

func dbEventList(db *sql.DB, tenantID string, limit int) ([]HostingEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := db.Query(`SELECT id, tenant_id, kind, actor, payload_json, created_at FROM hosting_events WHERE tenant_id=? ORDER BY id DESC LIMIT ?`, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostingEvent
	for rows.Next() {
		var e HostingEvent
		var payload string
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Kind, &e.Actor, &payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
