package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Only an operator may write these rules through /access/policy. Installation
// IDs are scoped to this Telephony server, and authorities are exact tuples.
type phoneDelegation struct {
	AppInstallID        int64            `json:"app_install_id"`
	AppName             string           `json:"app_name"`
	Permissions         []string         `json:"permissions"`
	DestinationIDs      []string         `json:"destination_ids,omitempty"`
	DestinationPrefixes []string         `json:"destination_prefixes,omitempty"`
	Authorities         []phoneAuthority `json:"authorities"`
	Actions             []string         `json:"actions"`
	OutboundNumbers     []string         `json:"outbound_numbers,omitempty"`
	ManageOthers        bool             `json:"manage_others,omitempty"`
}
type phoneAuthority struct {
	IssuerApp       string `json:"issuer_app"`
	IssuerInstallID string `json:"issuer_install_id"`
	OrganizationID  string `json:"organization_id"`
	SubjectType     string `json:"subject_type"`
}
type phoneResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type phoneResourceGrant struct {
	ID            string        `json:"id"`
	Principal     phoneIdentity `json:"principal"`
	Resource      phoneResource `json:"resource"`
	Actions       []string      `json:"actions"`
	Enabled       bool          `json:"enabled"`
	Deleted       bool          `json:"deleted"`
	Revision      string        `json:"revision"`
	Administrator int64         `json:"administrator_install_id"`
}
type resourceAdminError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *resourceAdminError) Error() string { return e.Message }
func adminError(code, message string) error { return &resourceAdminError{Code: code, Message: message} }
func adminFailure(err error) map[string]any {
	var domain *resourceAdminError
	if !errors.As(err, &domain) {
		domain = &resourceAdminError{Code: "storage_unavailable", Message: "resource storage unavailable", Retryable: true}
	}
	return map[string]any{"ok": false, "error": domain}
}
func adminSuccess(data any, replayed bool) map[string]any {
	return map[string]any{"ok": true, "data": data, "replayed": replayed}
}
func boundedAdminID(s string) bool {
	return s != "" && len(s) <= 128 && strings.IndexFunc(s, func(r rune) bool { return r < 32 || r == 127 }) < 0
}

var ordinaryPhoneActions = []string{"call.read", "call.dial", "call.answer", "call.attach", "call.hangup", "call.hold", "call.recording.control"}
var phoneAdminPermissions = []string{"telephony.destinations.read", "telephony.destinations.write", "telephony.grants.read", "telephony.grants.write"}

func (d phoneDelegation) destination(id string) bool {
	if containsString(d.DestinationIDs, id) {
		return true
	}
	for _, prefix := range d.DestinationPrefixes {
		if prefix != "" && strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}
func (d phoneDelegation) principal(p phoneIdentity) bool {
	for _, authority := range d.Authorities {
		if authority == (phoneAuthority{p.IssuerApp, p.IssuerInstallID, p.OrganizationID, p.SubjectType}) {
			return true
		}
	}
	return false
}
func (d phoneDelegation) resource(r phoneResource) bool {
	switch r.Kind {
	case "destination":
		return d.destination(r.ID)
	case "outbound_number":
		return containsString(d.OutboundNumbers, r.ID)
	}
	return false
}
func (d phoneDelegation) grant(g phoneResourceGrant, manage bool, caller int64) bool {
	if !d.principal(g.Principal) || !d.resource(g.Resource) || (manage && !d.ManageOthers && g.Administrator != caller) {
		return false
	}
	for _, action := range g.Actions {
		if !containsString(d.Actions, action) {
			return false
		}
	}
	return true
}
func validatePhoneDelegations(p phonePolicy) error {
	seen := map[int64]bool{}
	if len(p.Delegations) > 100 {
		return errors.New("too many resource administrators")
	}
	for _, d := range p.Delegations {
		if d.AppInstallID <= 0 || !boundedAdminID(d.AppName) || seen[d.AppInstallID] {
			return errors.New("resource administrators require unique verified installation IDs and app names")
		}
		seen[d.AppInstallID] = true
		for _, permission := range d.Permissions {
			if !containsString(phoneAdminPermissions, permission) {
				return errors.New("unsupported administrative permission")
			}
		}
		for _, id := range append(append([]string{}, d.DestinationIDs...), d.DestinationPrefixes...) {
			if !boundedAdminID(id) {
				return errors.New("invalid destination scope")
			}
		}
		for _, authority := range d.Authorities {
			if !boundedAdminID(authority.IssuerApp) || !boundedAdminID(authority.IssuerInstallID) || authority.SubjectType != "user" || len(authority.OrganizationID) > 128 {
				return errors.New("browser user authority required")
			}
		}
		for _, action := range d.Actions {
			if !containsString(ordinaryPhoneActions, action) {
				return errors.New("supervision delegation is unavailable in this release")
			}
		}
		for _, number := range d.OutboundNumbers {
			if !validE164(number) {
				return errors.New("outbound scope requires E.164 numbers")
			}
		}
	}
	return nil
}

type adminQuery interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

func (a *App) resourceAdministrator(q adminQuery, c context.Context, app *sdk.AppCtx, permission, resource string) (*sdk.Caller, phoneDelegation, error) {
	caller := sdk.CallerFrom(c)
	if caller == nil || caller.AppInstallID <= 0 || caller.AppName == "" || caller.ProjectID == "" || caller.ProjectID != app.CurrentProject() {
		return nil, phoneDelegation{}, adminError("forbidden", "verified calling app and matching project required")
	}
	if !caller.Allows(permission, resource) {
		return nil, phoneDelegation{}, adminError("forbidden", "administrative permission denied")
	}
	var raw string
	err := q.QueryRow(`SELECT policy_json FROM telephony_access_policies WHERE project_id=?`, caller.ProjectID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, phoneDelegation{}, adminError("forbidden", "resource administration is not delegated")
	}
	if err != nil {
		return nil, phoneDelegation{}, err
	}
	var policy phonePolicy
	if err = json.Unmarshal([]byte(raw), &policy); err != nil {
		return nil, phoneDelegation{}, err
	}
	for _, d := range policy.Delegations {
		if d.AppInstallID == caller.AppInstallID && d.AppName == caller.AppName && containsString(d.Permissions, permission) {
			return caller, d, nil
		}
	}
	return nil, phoneDelegation{}, adminError("forbidden", "resource administration is not delegated")
}

func (a *App) resourceAdminTools() []sdk.Tool {
	var tools []sdk.Tool
	for _, operation := range []string{"destinations_get", "destinations_upsert", "grants_get", "grants_list", "grants_upsert", "grants_delete"} {
		operation := operation
		properties := map[string]any{
			"id": map[string]any{"type": "string", "maxLength": 128}, "name": map[string]any{"type": "string"},
			"kind": map[string]any{"type": "string", "enum": []string{"browser"}}, "config": map[string]any{"type": "object"}, "enabled": map[string]any{"type": "boolean"},
			"principal": map[string]any{"type": "object"}, "resource": map[string]any{"type": "object"}, "actions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"expected_revision": map[string]any{"type": []string{"string", "null"}}, "idempotency_key": map[string]any{"type": "string", "maxLength": 128},
			"include_deleted": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "cursor": map[string]any{"type": "string"},
		}
		required := []string{"id"}
		if operation == "grants_list" {
			required = nil
		}
		if strings.HasSuffix(operation, "upsert") || strings.HasSuffix(operation, "delete") {
			required = append(required, "expected_revision", "idempotency_key")
		}
		if operation == "destinations_upsert" {
			required = append(required, "name", "kind", "config", "enabled")
		}
		if operation == "grants_upsert" {
			required = append(required, "principal", "resource", "actions", "enabled")
		}
		tools = append(tools, sdk.Tool{Name: "telephony_" + operation, Exposure: sdk.ToolExposureAppOnly, Description: "Administer scoped Telephony browser resources. Requires operator delegation. Returns ok/data or ok/error.", InputSchema: schemaObject(properties, required), HandlerCtx: func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
			return a.resourceAdmin(c, app, operation, args), nil
		}})
	}
	return tools
}
func (a *App) resourceAdmin(c context.Context, app *sdk.AppCtx, operation string, args map[string]any) map[string]any {
	if operation == "destinations_upsert" || operation == "grants_upsert" || operation == "grants_delete" {
		result, err := a.mutatePhoneResource(c, app, operation, args)
		if err != nil {
			return adminFailure(err)
		}
		return result
	}
	result, err := a.readPhoneResources(c, app, operation, args)
	if err != nil {
		return adminFailure(err)
	}
	return adminSuccess(result, false)
}
func adminPermission(operation string) string {
	group, _, _ := strings.Cut(operation, "_")
	access := "read"
	if strings.HasSuffix(operation, "upsert") || strings.HasSuffix(operation, "delete") {
		access = "write"
	}
	return "telephony." + group + "." + access
}

// Cursors carry no authority. Recompute the binding from the current verified
// caller and delegation, and authenticate the position with a stored random key.
type adminCursor struct {
	Last    string `json:"last"`
	Binding string `json:"binding"`
	MAC     string `json:"mac"`
}

func (a *App) readPhoneResources(c context.Context, app *sdk.AppCtx, operation string, args map[string]any) (any, error) {
	caller, delegation, err := a.resourceAdministrator(app.AppDB(), c, app, adminPermission(operation), strArg(args, "id", ""))
	if err != nil {
		return nil, err
	}
	if operation == "destinations_get" {
		id := strArg(args, "id", "")
		if !boundedAdminID(id) {
			return nil, adminError("invalid_argument", "id required")
		}
		if !delegation.destination(id) {
			return nil, adminError("forbidden", "destination outside delegation")
		}
		return a.adminDestination(app.AppDB(), caller.ProjectID, id, delegation)
	}
	if operation == "grants_get" && !boundedAdminID(strArg(args, "id", "")) {
		return nil, adminError("invalid_argument", "id required")
	}
	if operation == "grants_get" {
		grant, err := readPhoneResourceGrant(app.AppDB(), caller.ProjectID, strArg(args, "id", ""))
		if err != nil {
			return nil, err
		}
		if !delegation.grant(*grant, false, caller.AppInstallID) {
			return nil, adminError("forbidden", "grant outside delegation")
		}
		return grant, nil
	}
	limit := 50
	if raw, ok := args["limit"]; ok {
		if decodeAdminValue(raw, &limit) != nil {
			return nil, adminError("invalid_argument", "limit must be an integer")
		}
	}
	for _, key := range []string{"enabled", "include_deleted"} {
		if value, ok := args[key]; ok {
			if _, valid := value.(bool); !valid {
				return nil, adminError("invalid_argument", "boolean filter required")
			}
		}
	}
	if limit < 1 || limit > 100 {
		return nil, adminError("invalid_argument", "limit must be 1 to 100")
	}
	filters := map[string]any{}
	for _, key := range []string{"principal", "resource", "include_deleted", "kind", "enabled"} {
		if value, ok := args[key]; ok {
			filters[key] = value
		}
	}
	var principal *phoneIdentity
	var resource *phoneResource
	if raw, ok := filters["principal"]; ok {
		principal = &phoneIdentity{}
		if decodeAdminValue(raw, principal) != nil || !principal.valid() {
			return nil, adminError("invalid_argument", "invalid principal filter")
		}
	}
	if raw, ok := filters["resource"]; ok {
		resource = &phoneResource{}
		if decodeAdminValue(raw, resource) != nil || !boundedAdminID(resource.ID) || (resource.Kind != "destination" && resource.Kind != "outbound_number") {
			return nil, adminError("invalid_argument", "invalid resource filter")
		}
	}
	if principal != nil && !delegation.principal(*principal) {
		return nil, adminError("forbidden", "principal filter exceeds delegation")
	}
	if resource != nil && !delegation.resource(*resource) {
		return nil, adminError("forbidden", "resource filter exceeds delegation")
	}
	bindingRaw, _ := json.Marshal([]any{caller.ProjectID, caller.AppInstallID, caller.AppName, caller.Grants, caller.DefaultEffect, delegation, filters, operation})
	binding := phoneHash(string(bindingRaw))
	var key string
	if _, err = app.AppDB().Exec(`INSERT OR IGNORE INTO telephony_admin_state(project_id,cursor_key) VALUES(?,?)`, caller.ProjectID, newSecret()); err != nil {
		return nil, err
	}
	if err = app.AppDB().QueryRow(`SELECT cursor_key FROM telephony_admin_state WHERE project_id=?`, caller.ProjectID).Scan(&key); err != nil {
		return nil, err
	}
	last := ""
	if encoded := strArg(args, "cursor", ""); encoded != "" {
		if len(encoded) > 2048 {
			return nil, adminError("invalid_argument", "invalid cursor")
		}
		var cursor adminCursor
		raw, e := base64.RawURLEncoding.DecodeString(encoded)
		if e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Binding != binding || !secureEqual(cursor.MAC, adminCursorMAC(key, cursor.Last, binding)) {
			return nil, adminError("invalid_argument", "cursor does not match caller or filters")
		}
		last = cursor.Last
	}
	// Read bounded batches so filters cannot force an unbounded allocation.
	items := []any{}
	more := false
	for len(items) <= limit {
		query := `SELECT id FROM telephony_resource_grants WHERE project_id=? AND id>? ORDER BY id LIMIT 100`
		if operation == "destinations_list" {
			query = `SELECT id FROM routing_destinations WHERE project_id=? AND id>? ORDER BY id LIMIT 100`
		}
		rows, e := app.AppDB().QueryContext(c, query, caller.ProjectID, last)
		if e != nil {
			return nil, e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return nil, e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		for _, id := range ids {
			var item any
			if operation == "destinations_list" {
				if delegation.destination(id) && caller.Allows(adminPermission(operation), id) {
					candidate, readErr := a.adminDestination(app.AppDB(), caller.ProjectID, id, delegation)
					var denied *resourceAdminError
					if readErr != nil && (!errors.As(readErr, &denied) || denied.Code != "forbidden") {
						return nil, readErr
					}
					if readErr == nil && (args["kind"] == nil || args["kind"] == candidate["kind"]) && (args["enabled"] == nil || args["enabled"] == candidate["enabled"]) {
						item = candidate
					}
				}
			} else {
				grant, e := readPhoneResourceGrant(app.AppDB(), caller.ProjectID, id)
				if e != nil {
					return nil, e
				}
				if delegation.grant(*grant, false, caller.AppInstallID) && caller.Allows(adminPermission(operation), grant.Resource.ID) && (!grant.Deleted || boolArg(args, "include_deleted", false)) && (principal == nil || *principal == grant.Principal) && (resource == nil || *resource == grant.Resource) {
					item = grant
				}
			}
			if item != nil {
				if len(items) == limit {
					more = true
					break
				}
				items = append(items, item)
			}
			last = id
		}
		if more || len(ids) < 100 {
			break
		}
	}
	next := ""
	if more {
		cursor := adminCursor{Last: last, Binding: binding, MAC: adminCursorMAC(key, last, binding)}
		raw, _ := json.Marshal(cursor)
		next = base64.RawURLEncoding.EncodeToString(raw)
	}
	return map[string]any{"items": items, "cursor": next}, nil
}
func decodeAdminValue(value any, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(out)
}
func readPhoneResourceGrant(q adminQuery, project, id string) (*phoneResourceGrant, error) {
	var g phoneResourceGrant
	var principal, actions string
	err := q.QueryRow(`SELECT id,principal_json,resource_kind,resource_id,actions_json,enabled,deleted,revision,administrator_install_id FROM telephony_resource_grants WHERE project_id=? AND id=?`, project, id).Scan(&g.ID, &principal, &g.Resource.Kind, &g.Resource.ID, &actions, &g.Enabled, &g.Deleted, &g.Revision, &g.Administrator)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, adminError("not_found", "grant not found")
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(principal), &g.Principal); err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(actions), &g.Actions)
	return &g, err
}
func (a *App) adminDestination(q adminQuery, project, id string, d phoneDelegation) (map[string]any, error) {
	var name, kind, raw, revision string
	var enabled bool
	err := q.QueryRow(`SELECT name,kind,config_json,enabled,revision FROM routing_destinations WHERE project_id=? AND id=?`, project, id).Scan(&name, &kind, &raw, &enabled, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, adminError("not_found", "destination not found")
	}
	if err != nil {
		return nil, err
	}
	capacity, e := readDestinationCapacity(raw)
	if kind != "browser" || e != nil || capacity.Limit == 0 || !d.principal(capacity.Identity) {
		return nil, adminError("forbidden", "destination authority outside delegation")
	}
	// Administrative services receive a whitelist, never provider/media secrets.
	return map[string]any{"id": id, "name": name, "kind": kind, "enabled": enabled, "revision": revision, "config": map[string]any{"capacity": capacity}}, nil
}
func normalizedAdminActions(actions []string) ([]string, error) {
	if len(actions) == 0 || len(actions) > len(ordinaryPhoneActions) {
		return nil, adminError("unsupported_action", "ordinary call actions required")
	}
	result := append([]string{}, actions...)
	sort.Strings(result)
	for i, action := range result {
		if !containsString(ordinaryPhoneActions, action) {
			return nil, adminError("unsupported_action", "supervision actions are unavailable")
		}
		if i > 0 && result[i-1] == action {
			return nil, adminError("invalid_argument", "duplicate action")
		}
	}
	return result, nil
}

// Existing operator and agent tool contracts remain available. Bound services
// use the resource API so legacy methods cannot bypass delegation or receipts.
func (a *App) administrativeToolBoundary(name string, next func(context.Context, *sdk.AppCtx, map[string]any) (any, error)) func(context.Context, *sdk.AppCtx, map[string]any) (any, error) {
	return func(c context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
		caller := sdk.CallerFrom(c)
		if caller != nil && caller.AppInstallID > 0 {
			if name == "telephony_destinations_list" {
				result := a.resourceAdmin(c, app, "destinations_list", args)
				if data, ok := result["data"].(map[string]any); ok {
					result["destinations"] = data["items"]
					result["cursor"] = data["cursor"]
				}
				return result, nil
			}
			for _, prefix := range []string{"telephony_destinations_", "telephony_flows_", "telephony_ring_groups_", "telephony_routes_", "telephony_outbound_policy"} {
				if strings.HasPrefix(name, prefix) {
					return adminFailure(adminError("forbidden", "bound services must use scoped resource administration")), nil
				}
			}
		}
		return next(c, app, args)
	}
}
func boundServiceHTTP(r *http.Request) bool {
	return r.Header.Get(sdk.HeaderBoundCallerInstallID) != "" || r.Header.Get(sdk.HeaderBoundCallerAppName) != ""
}
