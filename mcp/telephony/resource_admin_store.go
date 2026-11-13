package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func adminCursorMAC(key, last, binding string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(last + "\x00" + binding))
	return hex.EncodeToString(mac.Sum(nil))
}

type phoneAdminMutation struct {
	ID             string         `json:"id"`
	Name           string         `json:"name,omitempty"`
	Kind           string         `json:"kind,omitempty"`
	Config         map[string]any `json:"config,omitempty"`
	Enabled        bool           `json:"enabled"`
	Principal      phoneIdentity  `json:"principal"`
	Resource       phoneResource  `json:"resource"`
	Actions        []string       `json:"actions,omitempty"`
	Expected       *string        `json:"expected_revision"`
	IdempotencyKey string         `json:"idempotency_key"`
}

func parsePhoneAdminMutation(operation string, args map[string]any) (phoneAdminMutation, error) {
	var m phoneAdminMutation
	allowed := map[string]bool{"id": true, "expected_revision": true, "idempotency_key": true, "_project_id": true}
	switch operation {
	case "destinations_upsert":
		for _, key := range []string{"name", "kind", "config", "enabled"} {
			allowed[key] = true
		}
	case "grants_upsert":
		for _, key := range []string{"principal", "resource", "actions", "enabled"} {
			allowed[key] = true
		}
	}
	for key := range args {
		if !allowed[key] {
			return m, adminError("invalid_argument", "unexpected mutation field")
		}
	}
	if decodeAdminValue(argsWithoutProject(args), &m) != nil {
		return m, adminError("invalid_argument", "invalid mutation fields")
	}
	if !boundedAdminID(m.ID) || !boundedAdminID(m.IdempotencyKey) {
		return m, adminError("invalid_argument", "id and idempotency_key must be bounded nonempty strings")
	}
	if _, ok := args["expected_revision"]; !ok {
		return m, adminError("invalid_argument", "expected_revision is required; use null to create")
	}
	if m.Expected != nil && !boundedAdminID(*m.Expected) {
		return m, adminError("invalid_argument", "invalid revision")
	}
	if operation != "grants_delete" {
		if _, ok := args["enabled"].(bool); !ok {
			return m, adminError("invalid_argument", "enabled is required")
		}
	}
	if operation == "destinations_upsert" {
		m.Name = strings.TrimSpace(m.Name)
		m.Kind = strings.ToLower(strings.TrimSpace(m.Kind))
		if m.Kind != "browser" {
			return m, adminError("invalid_argument", "only browser destinations can be provisioned")
		}
		if m.Config == nil {
			return m, adminError("invalid_argument", "destination config is required")
		}
	} else if operation == "grants_upsert" {
		if !m.Principal.valid() {
			return m, adminError("unsupported_principal_type", "complete browser user principal required")
		}
		for _, v := range []string{m.Principal.IssuerApp, m.Principal.IssuerInstallID, m.Principal.SubjectID} {
			if !boundedAdminID(v) {
				return m, adminError("invalid_argument", "invalid principal")
			}
		}
		if len(m.Principal.OrganizationID) > 128 {
			return m, adminError("invalid_argument", "invalid organization")
		}
		if (m.Resource.Kind != "destination" && m.Resource.Kind != "outbound_number") || !boundedAdminID(m.Resource.ID) {
			return m, adminError("invalid_reference", "invalid phone resource")
		}
		var err error
		m.Actions, err = normalizedAdminActions(m.Actions)
		if err != nil {
			return m, err
		}
		if m.Resource.Kind == "outbound_number" && (len(m.Actions) != 1 || m.Actions[0] != "call.dial") {
			return m, adminError("unsupported_action", "outbound number grants authorize call.dial only")
		}
	}
	return m, nil
}
func argsWithoutProject(args map[string]any) map[string]any {
	copy := make(map[string]any, len(args))
	for key, value := range args {
		if key != "_project_id" {
			copy[key] = value
		}
	}
	return copy
}
func revisionMatches(expected *string, revision string) bool {
	return expected == nil && revision == "" || expected != nil && *expected == revision
}

func (a *App) mutatePhoneResource(c context.Context, app *sdk.AppCtx, operation string, args map[string]any) (map[string]any, error) {
	// Missing verified caller context never reaches storage or receipt replay.
	caller, initialDelegation, err := a.resourceAdministrator(app.AppDB(), c, app, adminPermission(operation), strArg(args, "id", ""))
	if err != nil {
		return nil, err
	}
	m, err := parsePhoneAdminMutation(operation, args)
	if err != nil {
		return nil, err
	}
	if operation == "grants_upsert" && !initialDelegation.grant(phoneResourceGrant{Principal: m.Principal, Resource: m.Resource, Actions: m.Actions}, false, caller.AppInstallID) {
		return nil, adminError("forbidden", "grant exceeds delegation")
	}
	resourceID := m.ID
	if operation == "grants_upsert" {
		resourceID = m.Resource.ID
	}
	// Number ownership is checked through the same resolver used for outbound
	// calling, before taking SQLite's write transaction across any carrier I/O.
	var hasReceipt bool
	if err = app.AppDB().QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM telephony_admin_receipts WHERE project_id=? AND administrator_install_id=? AND idempotency_key=?)`, caller.ProjectID, caller.AppInstallID, m.IdempotencyKey).Scan(&hasReceipt); err != nil {
		return nil, err
	}
	if !hasReceipt && operation == "grants_upsert" && m.Resource.Kind == "outbound_number" {
		if !validE164(m.Resource.ID) {
			return nil, adminError("invalid_reference", "E.164 caller ID required")
		}
		_, _, _, e := a.selectCarrierBinding(app, caller.ProjectID, m.Resource.ID)
		if e != nil {
			return nil, adminError("invalid_reference", "caller ID is not owned by an authorized carrier")
		}
	}
	canonical, _ := json.Marshal([]any{operation, m})
	requestHash := phoneHash(string(canonical))
	queuedAt := time.Now()
	tx, err := app.AppDB().BeginTx(c, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Acquire SQLite's writer before reading authorization, revisions or receipts.
	if _, err = tx.ExecContext(c, `INSERT OR IGNORE INTO telephony_admin_state(project_id,cursor_key) VALUES(?,?)`, caller.ProjectID, newSecret()); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(c, `UPDATE telephony_admin_state SET cursor_key=cursor_key WHERE project_id=?`, caller.ProjectID); err != nil {
		return nil, err
	}
	admittedAt := time.Now()
	var finishedAt time.Time
	defer func() {
		if finishedAt.IsZero() {
			finishedAt = time.Now()
		}
		app.Logger().Info("Telephony resource administration timing", "operation", operation,
			"queue_ms", float64(admittedAt.Sub(queuedAt))/float64(time.Millisecond),
			"database_ms", float64(finishedAt.Sub(admittedAt))/float64(time.Millisecond))
	}()
	caller, delegation, err := a.resourceAdministrator(tx, c, app, adminPermission(operation), resourceID)
	if err != nil {
		return nil, err
	}
	var existing *phoneResourceGrant
	if operation != "destinations_upsert" {
		existing, err = readPhoneResourceGrant(tx, caller.ProjectID, m.ID)
		var domain *resourceAdminError
		if err != nil && !(errors.As(err, &domain) && domain.Code == "not_found") {
			return nil, err
		}
		if existing != nil && !delegation.grant(*existing, true, caller.AppInstallID) {
			return nil, adminError("forbidden", "grant is outside administrator ownership or delegation")
		}
	}
	if operation == "destinations_upsert" {
		if !delegation.destination(m.ID) {
			return nil, adminError("forbidden", "destination outside delegation")
		}
		var exists int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM routing_destinations WHERE project_id=? AND id=?`, caller.ProjectID, m.ID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists > 0 {
			if _, err = a.adminDestination(tx, caller.ProjectID, m.ID, delegation); err != nil {
				return nil, err
			}
		}
		capacity, e := readDestinationCapacity(mustAdminJSON(m.Config))
		if e != nil || capacity.Limit == 0 {
			return nil, adminError("invalid_argument", "browser destination requires an individual capacity identity")
		}
		if !delegation.principal(capacity.Identity) {
			return nil, adminError("forbidden", "destination authority outside delegation")
		}
	} else if operation == "grants_upsert" {
		proposed := phoneResourceGrant{Principal: m.Principal, Resource: m.Resource, Actions: m.Actions}
		if !delegation.grant(proposed, false, caller.AppInstallID) {
			return nil, adminError("forbidden", "grant exceeds delegated resources, authorities or actions")
		}
	}
	if operation == "grants_delete" && existing == nil {
		return nil, adminError("not_found", "grant not found")
	}
	var oldHash, resultJSON string
	err = tx.QueryRow(`SELECT request_hash,result_json FROM telephony_admin_receipts WHERE project_id=? AND administrator_install_id=? AND idempotency_key=?`, caller.ProjectID, caller.AppInstallID, m.IdempotencyKey).Scan(&oldHash, &resultJSON)
	if err == nil {
		if oldHash != requestHash {
			return nil, adminError("idempotency_conflict", "idempotency key was used for a different request")
		}
		var result map[string]any
		if err = json.Unmarshal([]byte(resultJSON), &result); err != nil {
			return nil, err
		}
		result["replayed"] = true
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := ringTime(time.Now())
	revision := newSecret()
	var data any
	if operation == "destinations_upsert" {
		raw, e := validateRoutingDestination(caller.ProjectID, m.ID, m.Name, m.Kind, m.Config)
		if e != nil {
			return nil, adminError("invalid_argument", e.Error())
		}
		var storedRevision, storedProject, storedConfig string
		err = tx.QueryRow(`SELECT revision,project_id,config_json FROM routing_destinations WHERE id=?`, m.ID).Scan(&storedRevision, &storedProject, &storedConfig)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if storedProject != "" && storedProject != caller.ProjectID {
			return nil, adminError("invalid_reference", "destination ID belongs to another project")
		}
		if !revisionMatches(m.Expected, storedRevision) {
			return nil, adminError("revision_conflict", "destination revision changed")
		}
		oldCapacity, _ := readDestinationCapacity(storedConfig)
		capacity, _ := readDestinationCapacity(raw)
		if storedRevision != "" && oldCapacity.Identity != capacity.Identity {
			var refs int
			if err = tx.QueryRow(`SELECT (SELECT COUNT(*) FROM telephony_resource_grants WHERE project_id=? AND resource_kind='destination' AND resource_id=?) + (SELECT COUNT(*) FROM telephony_call_owners WHERE project_id=? AND destination_id=?)`, caller.ProjectID, m.ID, caller.ProjectID, m.ID).Scan(&refs); err != nil {
				return nil, err
			}
			if refs > 0 {
				return nil, adminError("resource_in_use", "destination identity is referenced by grants or call ownership")
			}
		}
		if _, err = tx.Exec(`INSERT INTO routing_destinations(id,project_id,name,kind,config_json,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,config_json=excluded.config_json,enabled=excluded.enabled,updated_at=excluded.updated_at`, m.ID, caller.ProjectID, m.Name, m.Kind, raw, m.Enabled, now, now); err != nil {
			return nil, err
		}
		result, e := a.adminDestination(tx, caller.ProjectID, m.ID, delegation)
		if e != nil {
			return nil, e
		}
		revision = result["revision"].(string)
		data = result
	} else {
		previousRevision := ""
		if existing != nil {
			previousRevision = existing.Revision
		}
		if !revisionMatches(m.Expected, previousRevision) {
			return nil, adminError("revision_conflict", "grant revision changed")
		}
		if operation == "grants_delete" {
			if _, err = tx.Exec(`UPDATE telephony_resource_grants SET deleted=1,enabled=0,revision=?,updated_at=? WHERE project_id=? AND id=?`, revision, now, caller.ProjectID, m.ID); err != nil {
				return nil, err
			}
		} else {
			if existing != nil && (existing.Principal != m.Principal || existing.Resource != m.Resource) {
				return nil, adminError("invalid_argument", "grant principal and resource are immutable")
			}
			var policyRaw string
			if err = tx.QueryRow(`SELECT policy_json FROM telephony_access_policies WHERE project_id=?`, caller.ProjectID).Scan(&policyRaw); err != nil {
				return nil, err
			}
			var policy phonePolicy
			if err = json.Unmarshal([]byte(policyRaw), &policy); err != nil {
				return nil, err
			}
			supported := false
			for _, provider := range policy.Providers {
				if validPhoneProvider(provider) && provider.IssuerApp == m.Principal.IssuerApp && provider.IssuerInstallID == m.Principal.IssuerInstallID {
					supported = true
				}
			}
			if !supported {
				return nil, adminError("unsupported_principal_type", "principal authority has no approved browser authentication provider")
			}
			if m.Resource.Kind == "destination" {
				dest, e := a.adminDestination(tx, caller.ProjectID, m.Resource.ID, delegation)
				if e != nil {
					var domain *resourceAdminError
					if !errors.As(e, &domain) {
						return nil, e
					}
					return nil, adminError("invalid_reference", "destination is outside the authorized browser resources")
				}
				raw := mustAdminJSON(dest["config"])
				capacity, _ := readDestinationCapacity(raw)
				if capacity.Identity != m.Principal {
					return nil, adminError("invalid_reference", "destination belongs to a different principal")
				}
			}
			if _, err = tx.Exec(`INSERT INTO telephony_resource_grants(project_id,id,principal_json,resource_kind,resource_id,actions_json,enabled,revision,administrator_install_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,id) DO UPDATE SET actions_json=excluded.actions_json,enabled=excluded.enabled,deleted=0,revision=excluded.revision,updated_at=excluded.updated_at`, caller.ProjectID, m.ID, m.Principal.key(), m.Resource.Kind, m.Resource.ID, mustAdminJSON(m.Actions), m.Enabled, revision, caller.AppInstallID, now, now); err != nil {
				return nil, err
			}
		}
		data, err = readPhoneResourceGrant(tx, caller.ProjectID, m.ID)
		if err != nil {
			return nil, err
		}
	}
	result := adminSuccess(data, false)
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO telephony_admin_receipts VALUES(?,?,?,?,?,?)`, caller.ProjectID, caller.AppInstallID, m.IdempotencyKey, requestHash, string(encoded), now); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO telephony_admin_audit(project_id,administrator_install_id,administrator_app,operation,resource_id,revision,idempotency_key,occurred_at) VALUES(?,?,?,?,?,?,?,?)`, caller.ProjectID, caller.AppInstallID, caller.AppName, operation, m.ID, revision, m.IdempotencyKey, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	finishedAt = time.Now()
	a.routingCommitted(caller.ProjectID)
	a.revokeCallListeners(caller.ProjectID)
	return result, nil
}
func mustAdminJSON(v any) string { raw, _ := json.Marshal(v); return string(raw) }
