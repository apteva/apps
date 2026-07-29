package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ─── Admin HTTP RBAC surface ─────────────────────────────────────────

func (a *App) handleAdminRolesList(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	roles, err := dbListRoles(getAppCtx(r).AppDB(), pid, org.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"roles": roles, "count": len(roles)})
}

func (a *App) handleAdminRolesCreate(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	role, err := dbCreateRole(getAppCtx(r).AppDB(), pid, org.ID, body.Key, body.Name, body.Description)
	if err != nil {
		httpErr(w, rbacCreateStatus(err), err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, nil, "", "role_created", r.RemoteAddr, r.UserAgent(),
		map[string]any{"role_id": role.ID, "key": role.Key})
	httpStatus(w, http.StatusCreated, map[string]any{"role": role})
}

func (a *App) handleAdminRolesUpdate(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Name == nil && body.Description == nil {
		httpErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	role, err := dbUpdateRole(getAppCtx(r).AppDB(), pid, org.ID, id, body.Name, body.Description)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, nil, "", "role_updated", r.RemoteAddr, r.UserAgent(),
		map[string]any{"role_id": id, "key": role.Key})
	httpJSON(w, map[string]any{"role": role})
}

func (a *App) handleAdminRolesDelete(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbDeleteRole(getAppCtx(r).AppDB(), pid, org.ID, id); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, nil, "", "role_deleted", r.RemoteAddr, r.UserAgent(),
		map[string]any{"role_id": id})
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleAdminRolePermissionsSet(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		PermissionIDs *[]int64 `json:"permission_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.PermissionIDs == nil {
		httpErr(w, http.StatusBadRequest, "permission_ids required")
		return
	}
	role, err := dbSetRolePermissions(getAppCtx(r).AppDB(), pid, org.ID, id, *body.PermissionIDs)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, nil, "", "role_permissions_set", r.RemoteAddr, r.UserAgent(),
		map[string]any{"role_id": id, "permissions": role.Permissions})
	httpJSON(w, map[string]any{"role": role})
}

func (a *App) handleAdminPermissionsList(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	permissions, err := dbListPermissions(getAppCtx(r).AppDB(), pid, org.ID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"permissions": permissions, "count": len(permissions)})
}

func (a *App) handleAdminPermissionsCreate(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	permission, err := dbCreatePermission(getAppCtx(r).AppDB(), pid, org.ID, body.Key, body.Name, body.Description)
	if err != nil {
		httpErr(w, rbacCreateStatus(err), err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, nil, "", "permission_created", r.RemoteAddr, r.UserAgent(),
		map[string]any{"permission_id": permission.ID, "key": permission.Key})
	httpStatus(w, http.StatusCreated, map[string]any{"permission": permission})
}

func (a *App) handleAdminPermissionsUpdate(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Name == nil && body.Description == nil {
		httpErr(w, http.StatusBadRequest, "nothing to update")
		return
	}
	permission, err := dbUpdatePermission(getAppCtx(r).AppDB(), pid, org.ID, id, body.Name, body.Description)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, nil, "", "permission_updated", r.RemoteAddr, r.UserAgent(),
		map[string]any{"permission_id": id, "key": permission.Key})
	httpJSON(w, map[string]any{"permission": permission})
}

func (a *App) handleAdminPermissionsDelete(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dbDeletePermission(getAppCtx(r).AppDB(), pid, org.ID, id); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, nil, "", "permission_deleted", r.RemoteAddr, r.UserAgent(),
		map[string]any{"permission_id": id})
	httpJSON(w, map[string]any{"ok": true})
}

func (a *App) handleAdminUserRolesSet(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	userID, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		RoleIDs *[]int64 `json:"role_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.RoleIDs == nil {
		httpErr(w, http.StatusBadRequest, "role_ids required")
		return
	}
	authorization, err := dbSetUserRoles(getAppCtx(r).AppDB(), pid, org.ID, userID, *body.RoleIDs)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	dbAudit(getAppCtx(r).AppDB(), pid, org.ID, &userID, "", "user_roles_set", r.RemoteAddr, r.UserAgent(),
		map[string]any{
			"roles":                 authorization.Roles,
			"authorization_version": authorization.AuthorizationVersion,
		})
	httpJSON(w, map[string]any{"authorization": authorization})
}

func rbacCreateStatus(err error) int {
	if strings.Contains(strings.ToLower(err.Error()), "unique") {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// ─── MCP RBAC tools ──────────────────────────────────────────────────

func (a *App) toolRolesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	roles, err := dbListRoles(ctx.AppDB(), pid, org.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"roles": roles, "count": len(roles)}, nil
}

func (a *App) toolRolesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	role, err := dbCreateRole(ctx.AppDB(), pid, org.ID,
		stringArg(args, "key", ""), stringArg(args, "name", ""), stringArg(args, "description", ""))
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "role_created", "", "agent",
		map[string]any{"role_id": role.ID, "key": role.Key})
	return map[string]any{"role": role}, nil
}

func (a *App) toolRolesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, org, id, err := rbacMutationArgs(ctx, args, "role_id")
	if err != nil {
		return nil, err
	}
	var name, description *string
	if v, ok := args["name"].(string); ok {
		name = &v
	}
	if v, ok := args["description"].(string); ok {
		description = &v
	}
	if name == nil && description == nil {
		return nil, errors.New("nothing to update")
	}
	role, err := dbUpdateRole(ctx.AppDB(), pid, org.ID, id, name, description)
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "role_updated", "", "agent",
		map[string]any{"role_id": id, "key": role.Key})
	return map[string]any{"role": role}, nil
}

func (a *App) toolRolesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, org, id, err := rbacMutationArgs(ctx, args, "role_id")
	if err != nil {
		return nil, err
	}
	if err := dbDeleteRole(ctx.AppDB(), pid, org.ID, id); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "role_deleted", "", "agent", map[string]any{"role_id": id})
	return map[string]any{"ok": true}, nil
}

func (a *App) toolPermissionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	permissions, err := dbListPermissions(ctx.AppDB(), pid, org.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"permissions": permissions, "count": len(permissions)}, nil
}

func (a *App) toolPermissionsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	permission, err := dbCreatePermission(ctx.AppDB(), pid, org.ID,
		stringArg(args, "key", ""), stringArg(args, "name", ""), stringArg(args, "description", ""))
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "permission_created", "", "agent",
		map[string]any{"permission_id": permission.ID, "key": permission.Key})
	return map[string]any{"permission": permission}, nil
}

func (a *App) toolPermissionsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, org, id, err := rbacMutationArgs(ctx, args, "permission_id")
	if err != nil {
		return nil, err
	}
	var name, description *string
	if v, ok := args["name"].(string); ok {
		name = &v
	}
	if v, ok := args["description"].(string); ok {
		description = &v
	}
	if name == nil && description == nil {
		return nil, errors.New("nothing to update")
	}
	permission, err := dbUpdatePermission(ctx.AppDB(), pid, org.ID, id, name, description)
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "permission_updated", "", "agent",
		map[string]any{"permission_id": id, "key": permission.Key})
	return map[string]any{"permission": permission}, nil
}

func (a *App) toolPermissionsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, org, id, err := rbacMutationArgs(ctx, args, "permission_id")
	if err != nil {
		return nil, err
	}
	if err := dbDeletePermission(ctx.AppDB(), pid, org.ID, id); err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "permission_deleted", "", "agent",
		map[string]any{"permission_id": id})
	return map[string]any{"ok": true}, nil
}

func (a *App) toolRolePermissionsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, org, roleID, err := rbacMutationArgs(ctx, args, "role_id")
	if err != nil {
		return nil, err
	}
	if _, ok := args["permission_ids"]; !ok {
		return nil, errors.New("permission_ids required")
	}
	role, err := dbSetRolePermissions(ctx.AppDB(), pid, org.ID, roleID, int64SliceArg(args, "permission_ids"))
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, nil, "", "role_permissions_set", "", "agent",
		map[string]any{"role_id": roleID, "permissions": role.Permissions})
	return map[string]any{"role": role}, nil
}

func (a *App) toolUserRolesSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, org, userID, err := rbacMutationArgs(ctx, args, "user_id")
	if err != nil {
		return nil, err
	}
	if _, ok := args["role_ids"]; !ok {
		return nil, errors.New("role_ids required")
	}
	authorization, err := dbSetUserRoles(ctx.AppDB(), pid, org.ID, userID, int64SliceArg(args, "role_ids"))
	if err != nil {
		return nil, err
	}
	dbAudit(ctx.AppDB(), pid, org.ID, &userID, "", "user_roles_set", "", "agent",
		map[string]any{
			"roles":                 authorization.Roles,
			"authorization_version": authorization.AuthorizationVersion,
		})
	return map[string]any{"authorization": authorization}, nil
}

func rbacMutationArgs(ctx *sdk.AppCtx, args map[string]any, idKey string) (string, *Organization, int64, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return "", nil, 0, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return "", nil, 0, err
	}
	id, ok := intReq(args, idKey)
	if !ok {
		return "", nil, 0, errors.New(idKey + " required")
	}
	return pid, org, id, nil
}

func int64SliceArg(args map[string]any, key string) []int64 {
	switch values := args[key].(type) {
	case []int64:
		return values
	case []int:
		out := make([]int64, 0, len(values))
		for _, value := range values {
			out = append(out, int64(value))
		}
		return out
	case []any:
		out := make([]int64, 0, len(values))
		for _, value := range values {
			switch n := value.(type) {
			case float64:
				out = append(out, int64(n))
			case int:
				out = append(out, int64(n))
			case int64:
				out = append(out, n)
			}
		}
		return out
	default:
		return []int64{}
	}
}
