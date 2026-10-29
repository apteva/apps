package engine

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
)

// AppAccess lets one calling app address a project-owned database. Calling
// apps otherwise only see their private namespace. Rows are written by project
// operators (dashboard or agents with database.manage), never by the app.
type AppAccess struct {
	Database string `json:"database"`
	App      string `json:"app"`
	Access   string `json:"access"`
}

var appName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func isAccessOp(op string) bool {
	return op == "access_grant" || op == "access_revoke" || op == "access_list"
}

func (m *Manager) accessOperation(ctx context.Context, scope, op string, r Request) (any, error) {
	if op == "access_list" {
		rows, e := m.registry.QueryContext(ctx, `SELECT database,app,access FROM app_access WHERE scope=? AND database=? ORDER BY app`, scope, r.Database)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		out := []AppAccess{}
		for rows.Next() {
			var a AppAccess
			if e := rows.Scan(&a.Database, &a.App, &a.Access); e != nil {
				return nil, e
			}
			out = append(out, a)
		}
		return out, rows.Err()
	}
	if !appName.MatchString(r.App) {
		return nil, Invalid("app must be an installed app name")
	}
	if op == "access_revoke" {
		res, e := m.registry.ExecContext(ctx, `DELETE FROM app_access WHERE scope=? AND database=? AND app=?`, scope, r.Database, r.App)
		if e != nil {
			return nil, e
		}
		n, _ := res.RowsAffected()
		return map[string]any{"revoked": n > 0}, nil
	}
	if r.Access != "read" && r.Access != "write" {
		return nil, Invalid("access must be read or write")
	}
	// Grants name existing databases only, so access never implies creation.
	var id string
	if e := m.registry.QueryRowContext(ctx, `SELECT id FROM databases WHERE scope=? AND name=?`, scope, r.Database).Scan(&id); errors.Is(e, sql.ErrNoRows) {
		return nil, fail("not_found", "database %s does not exist", r.Database)
	} else if e != nil {
		return nil, e
	}
	if _, e := m.registry.ExecContext(ctx, `INSERT INTO app_access(scope,database,app,access) VALUES(?,?,?,?) ON CONFLICT(scope,database,app) DO UPDATE SET access=excluded.access`, scope, r.Database, r.App, r.Access); e != nil {
		return nil, e
	}
	return AppAccess{Database: r.Database, App: r.App, Access: r.Access}, nil
}

// AppAccessLevel returns "read", "write" or "" for app's access to a
// project-owned database in scope.
func (m *Manager) AppAccessLevel(ctx context.Context, scope, database, app string) (string, error) {
	var access string
	e := m.registry.QueryRowContext(ctx, `SELECT access FROM app_access WHERE scope=? AND database=? AND app=?`, scope, database, app).Scan(&access)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return access, e
}

// AppAccessible lists the project databases app may address.
func (m *Manager) AppAccessible(ctx context.Context, scope, app string) (map[string]string, error) {
	rows, e := m.registry.QueryContext(ctx, `SELECT database,access FROM app_access WHERE scope=? AND app=?`, scope, app)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, access string
		if e := rows.Scan(&name, &access); e != nil {
			return nil, e
		}
		out[name] = access
	}
	return out, rows.Err()
}
