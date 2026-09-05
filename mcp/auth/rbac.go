package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func normalizeRoleKey(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if len(key) < 2 || len(key) > 64 {
		return "", errors.New("role key must be 2-64 characters")
	}
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return "", errors.New("role key may contain lowercase letters, digits, hyphens, and underscores")
		}
	}
	return key, nil
}

func normalizePermissionKey(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if len(key) < 3 || len(key) > 128 || !strings.Contains(key, ":") {
		return "", errors.New("permission key must be 3-128 characters and namespaced (for example resources:read)")
	}
	for _, c := range key {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' || c == ':') {
			return "", errors.New("permission key contains unsupported characters")
		}
	}
	for _, segment := range strings.Split(key, ":") {
		if segment == "" {
			return "", errors.New("permission namespace and action segments cannot be empty")
		}
	}
	return key, nil
}

func rbacName(raw, key string) string {
	if name := strings.TrimSpace(raw); name != "" {
		return name
	}
	return key
}

func dbCreateRole(db *sql.DB, projectID string, orgID int64, key, name, description string) (*Role, error) {
	var err error
	if key, err = normalizeRoleKey(key); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`INSERT INTO auth_roles(project_id, organization_id, key, name, description, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)`, projectID, orgID, key, rbacName(name, key), strings.TrimSpace(description), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetRole(db, projectID, orgID, id)
}

func dbGetRole(db *sql.DB, projectID string, orgID, roleID int64) (*Role, error) {
	var r Role
	if err := db.QueryRow(`SELECT id, organization_id, key, name, IFNULL(description,''), IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM auth_roles WHERE project_id=? AND organization_id=? AND id=?`, projectID, orgID, roleID).
		Scan(&r.ID, &r.OrganizationID, &r.Key, &r.Name, &r.Description, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	perms, err := dbRolePermissionKeys(db, projectID, orgID, roleID)
	if err != nil {
		return nil, err
	}
	r.Permissions = perms
	return &r, nil
}

func dbListRoles(db *sql.DB, projectID string, orgID int64) ([]Role, error) {
	rows, err := db.Query(`SELECT id, organization_id, key, name, IFNULL(description,''), IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM auth_roles WHERE project_id=? AND organization_id=? ORDER BY key`, projectID, orgID)
	if err != nil {
		return nil, err
	}
	var out []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.OrganizationID, &r.Key, &r.Name, &r.Description, &r.CreatedAt, &r.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	positions := map[int64]int{}
	for i := range out {
		positions[out[i].ID] = i
		out[i].Permissions = []string{}
	}
	rows, err = db.Query(`SELECT rp.role_id,p.key FROM auth_role_permissions rp JOIN auth_permissions p ON p.id=rp.permission_id WHERE rp.project_id=? AND rp.organization_id=? ORDER BY p.key`, projectID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var key string
		if err = rows.Scan(&id, &key); err != nil {
			return nil, err
		}
		if i, ok := positions[id]; ok {
			out[i].Permissions = append(out[i].Permissions, key)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func dbRolePermissionKeys(db *sql.DB, projectID string, orgID, roleID int64) ([]string, error) {
	rows, err := db.Query(`SELECT p.key FROM auth_permissions p
		JOIN auth_role_permissions rp ON rp.permission_id=p.id
		WHERE rp.project_id=? AND rp.organization_id=? AND rp.role_id=? ORDER BY p.key`,
		projectID, orgID, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func dbUpdateRole(db *sql.DB, projectID string, orgID, roleID int64, name, description *string) (*Role, error) {
	sets := []string{"updated_at=?"}
	args := []any{time.Now().UTC().Format(time.RFC3339)}
	if name != nil {
		if strings.TrimSpace(*name) == "" {
			return nil, errors.New("role name cannot be empty")
		}
		sets, args = append(sets, "name=?"), append(args, strings.TrimSpace(*name))
	}
	if description != nil {
		sets, args = append(sets, "description=?"), append(args, strings.TrimSpace(*description))
	}
	args = append(args, projectID, orgID, roleID)
	res, err := db.Exec(`UPDATE auth_roles SET `+strings.Join(sets, ",")+` WHERE project_id=? AND organization_id=? AND id=?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return dbGetRole(db, projectID, orgID, roleID)
}

func dbCreatePermission(db *sql.DB, projectID string, orgID int64, key, name, description string) (*Permission, error) {
	var err error
	if key, err = normalizePermissionKey(key); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(`INSERT INTO auth_permissions(project_id, organization_id, key, name, description, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)`, projectID, orgID, key, rbacName(name, key), strings.TrimSpace(description), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetPermission(db, projectID, orgID, id)
}

func dbGetPermission(db *sql.DB, projectID string, orgID, permissionID int64) (*Permission, error) {
	var p Permission
	err := db.QueryRow(`SELECT id, organization_id, key, name, IFNULL(description,''), IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM auth_permissions WHERE project_id=? AND organization_id=? AND id=?`, projectID, orgID, permissionID).
		Scan(&p.ID, &p.OrganizationID, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt)
	return &p, err
}

func dbListPermissions(db *sql.DB, projectID string, orgID int64) ([]Permission, error) {
	rows, err := db.Query(`SELECT id, organization_id, key, name, IFNULL(description,''), IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM auth_permissions WHERE project_id=? AND organization_id=? ORDER BY key`, projectID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Permission{}
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.ID, &p.OrganizationID, &p.Key, &p.Name, &p.Description, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func dbUpdatePermission(db *sql.DB, projectID string, orgID, permissionID int64, name, description *string) (*Permission, error) {
	sets := []string{"updated_at=?"}
	args := []any{time.Now().UTC().Format(time.RFC3339)}
	if name != nil {
		if strings.TrimSpace(*name) == "" {
			return nil, errors.New("permission name cannot be empty")
		}
		sets, args = append(sets, "name=?"), append(args, strings.TrimSpace(*name))
	}
	if description != nil {
		sets, args = append(sets, "description=?"), append(args, strings.TrimSpace(*description))
	}
	args = append(args, projectID, orgID, permissionID)
	res, err := db.Exec(`UPDATE auth_permissions SET `+strings.Join(sets, ",")+` WHERE project_id=? AND organization_id=? AND id=?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return dbGetPermission(db, projectID, orgID, permissionID)
}

func dbAuthorizationContext(db DBTX, projectID string, org *Organization, userID int64) (AuthorizationContext, error) {
	if conn, ok := db.(*sql.DB); ok {
		tx, err := conn.Begin()
		if err != nil {
			return AuthorizationContext{}, err
		}
		defer tx.Rollback()
		out, err := dbAuthorizationContext(tx, projectID, org, userID)
		if err != nil {
			return out, err
		}
		return out, tx.Commit()
	}

	ctx := AuthorizationContext{
		UserID:           uintToStr(userID),
		OrganizationID:   uintToStr(org.ID),
		OrganizationSlug: org.Slug,
		Roles:            []string{},
		Permissions:      []string{},
	}
	if err := db.QueryRow(`SELECT authorization_version FROM users
		WHERE project_id=? AND organization_id=? AND id=?`, projectID, org.ID, userID).
		Scan(&ctx.AuthorizationVersion); err != nil {
		return ctx, err
	}
	rows, err := db.Query(`SELECT DISTINCT r.key FROM auth_roles r
		JOIN auth_user_roles ur ON ur.role_id=r.id
		WHERE ur.project_id=? AND ur.organization_id=? AND ur.user_id=? ORDER BY r.key`,
		projectID, org.ID, userID)
	if err != nil {
		return ctx, err
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return ctx, err
		}
		ctx.Roles = append(ctx.Roles, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ctx, err
	}
	rows.Close()
	rows, err = db.Query(`SELECT DISTINCT p.key FROM auth_permissions p
		JOIN auth_role_permissions rp ON rp.permission_id=p.id
		JOIN auth_user_roles ur ON ur.role_id=rp.role_id
		WHERE ur.project_id=? AND ur.organization_id=? AND ur.user_id=? ORDER BY p.key`,
		projectID, org.ID, userID)
	if err != nil {
		return ctx, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return ctx, err
		}
		ctx.Permissions = append(ctx.Permissions, key)
	}
	return ctx, rows.Err()
}

func normalizeIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sameIDs(a, b []int64) bool {
	a, b = normalizeIDs(a), normalizeIDs(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func txIDs(tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func validateScopedIDs(tx *sql.Tx, table, projectID string, orgID int64, ids []int64) error {
	for _, id := range normalizeIDs(ids) {
		var n int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE project_id=? AND organization_id=? AND id=?`, table)
		if err := tx.QueryRow(q, projectID, orgID, id).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("%s id %d does not belong to the organization", table, id)
		}
	}
	return nil
}

func incrementUserAuthorizationVersion(tx *sql.Tx, projectID string, orgID, userID int64) error {
	res, err := tx.Exec(`UPDATE users SET authorization_version=authorization_version+1, updated_at=?
		WHERE project_id=? AND organization_id=? AND id=?`,
		time.Now().UTC().Format(time.RFC3339), projectID, orgID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func incrementRoleUsersAuthorizationVersion(tx *sql.Tx, projectID string, orgID, roleID int64) error {
	_, err := tx.Exec(`UPDATE users SET authorization_version=authorization_version+1, updated_at=?
		WHERE project_id=? AND organization_id=? AND id IN (
			SELECT user_id FROM auth_user_roles WHERE project_id=? AND organization_id=? AND role_id=?
		)`, time.Now().UTC().Format(time.RFC3339), projectID, orgID, projectID, orgID, roleID)
	return err
}

func incrementPermissionUsersAuthorizationVersion(tx *sql.Tx, projectID string, orgID, permissionID int64) error {
	_, err := tx.Exec(`UPDATE users SET authorization_version=authorization_version+1, updated_at=?
		WHERE project_id=? AND organization_id=? AND id IN (
			SELECT DISTINCT ur.user_id
			FROM auth_user_roles ur
			JOIN auth_role_permissions rp ON rp.role_id=ur.role_id
			WHERE ur.project_id=? AND ur.organization_id=? AND rp.permission_id=?
		)`, time.Now().UTC().Format(time.RFC3339), projectID, orgID, projectID, orgID, permissionID)
	return err
}

func dbSetRolePermissions(db *sql.DB, projectID string, orgID, roleID int64, permissionIDs []int64) (*Role, error) {
	if len(permissionIDs) > 256 {
		return nil, errors.New("too many assignments")
	}
	for _, id := range permissionIDs {
		if id <= 0 {
			return nil, errors.New("IDs must be positive integers")
		}
	}
	permissionIDs = normalizeIDs(permissionIDs)
	tx, err := beginAuthTx(db, projectID, orgID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := validateScopedIDs(tx, "auth_roles", projectID, orgID, []int64{roleID}); err != nil {
		return nil, err
	}
	if err := validateScopedIDs(tx, "auth_permissions", projectID, orgID, permissionIDs); err != nil {
		return nil, err
	}
	current, err := txIDs(tx, `SELECT permission_id FROM auth_role_permissions
		WHERE project_id=? AND organization_id=? AND role_id=? ORDER BY permission_id`, projectID, orgID, roleID)
	if err != nil {
		return nil, err
	}
	if !sameIDs(current, permissionIDs) {
		if err := incrementRoleUsersAuthorizationVersion(tx, projectID, orgID, roleID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM auth_role_permissions WHERE project_id=? AND organization_id=? AND role_id=?`,
			projectID, orgID, roleID); err != nil {
			return nil, err
		}
		for _, permissionID := range permissionIDs {
			if _, err := tx.Exec(`INSERT INTO auth_role_permissions(project_id, organization_id, role_id, permission_id)
				VALUES(?,?,?,?)`, projectID, orgID, roleID, permissionID); err != nil {
				return nil, err
			}
		}
	}
	if err := validateOrgAuthorizationSizes(tx, projectID, orgID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetRole(db, projectID, orgID, roleID)
}

func dbSetUserRoles(db *sql.DB, projectID string, orgID, userID int64, roleIDs []int64) (AuthorizationContext, error) {
	if len(roleIDs) > 64 {
		return AuthorizationContext{}, errors.New("too many assignments")
	}
	for _, id := range roleIDs {
		if id <= 0 {
			return AuthorizationContext{}, errors.New("IDs must be positive integers")
		}
	}
	roleIDs = normalizeIDs(roleIDs)
	tx, err := beginAuthTx(db, projectID, orgID)
	if err != nil {
		return AuthorizationContext{}, err
	}
	defer tx.Rollback()
	if err := validateScopedIDs(tx, "users", projectID, orgID, []int64{userID}); err != nil {
		return AuthorizationContext{}, err
	}
	if err := validateScopedIDs(tx, "auth_roles", projectID, orgID, roleIDs); err != nil {
		return AuthorizationContext{}, err
	}
	current, err := txIDs(tx, `SELECT role_id FROM auth_user_roles
		WHERE project_id=? AND organization_id=? AND user_id=? ORDER BY role_id`, projectID, orgID, userID)
	if err != nil {
		return AuthorizationContext{}, err
	}
	if !sameIDs(current, roleIDs) {
		if _, err := tx.Exec(`DELETE FROM auth_user_roles WHERE project_id=? AND organization_id=? AND user_id=?`,
			projectID, orgID, userID); err != nil {
			return AuthorizationContext{}, err
		}
		for _, roleID := range roleIDs {
			if _, err := tx.Exec(`INSERT INTO auth_user_roles(project_id, organization_id, user_id, role_id)
				VALUES(?,?,?,?)`, projectID, orgID, userID, roleID); err != nil {
				return AuthorizationContext{}, err
			}
		}
		if err := incrementUserAuthorizationVersion(tx, projectID, orgID, userID); err != nil {
			return AuthorizationContext{}, err
		}
	}
	if err := validateOrgAuthorizationSizes(tx, projectID, orgID); err != nil {
		return AuthorizationContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return AuthorizationContext{}, err
	}
	org, err := dbGetOrgByID(db, projectID, orgID)
	if err != nil {
		return AuthorizationContext{}, err
	}
	return dbAuthorizationContext(db, projectID, org, userID)
}

func dbDeleteRole(db *sql.DB, projectID string, orgID, roleID int64) error {
	tx, err := beginAuthTx(db, projectID, orgID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateScopedIDs(tx, "auth_roles", projectID, orgID, []int64{roleID}); err != nil {
		return err
	}
	if err := incrementRoleUsersAuthorizationVersion(tx, projectID, orgID, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM auth_roles WHERE project_id=? AND organization_id=? AND id=?`, projectID, orgID, roleID); err != nil {
		return err
	}
	return tx.Commit()
}

func dbDeletePermission(db *sql.DB, projectID string, orgID, permissionID int64) error {
	tx, err := beginAuthTx(db, projectID, orgID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateScopedIDs(tx, "auth_permissions", projectID, orgID, []int64{permissionID}); err != nil {
		return err
	}
	if err := incrementPermissionUsersAuthorizationVersion(tx, projectID, orgID, permissionID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM auth_permissions WHERE project_id=? AND organization_id=? AND id=?`,
		projectID, orgID, permissionID); err != nil {
		return err
	}
	return tx.Commit()
}

// Reserve room for identity claims and base64 expansion in the 12 KiB JWT.
func validateOrgAuthorizationSizes(tx *sql.Tx, pid string, oid int64) error {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM (
 SELECT user_id,SUM(size) AS bytes,SUM(perms) AS permission_count FROM (
  SELECT ur.user_id,SUM(length(r.key)+3) AS size,0 AS perms FROM auth_user_roles ur JOIN auth_roles r ON r.id=ur.role_id WHERE ur.project_id=? AND ur.organization_id=? GROUP BY ur.user_id
  UNION ALL
  SELECT user_id,SUM(length(key)+3),COUNT(*) FROM (SELECT DISTINCT ur.user_id,p.key FROM auth_user_roles ur JOIN auth_role_permissions rp ON rp.role_id=ur.role_id JOIN auth_permissions p ON p.id=rp.permission_id WHERE ur.project_id=? AND ur.organization_id=?) GROUP BY user_id
 ) GROUP BY user_id HAVING bytes>6000 OR permission_count>256
)`, pid, oid, pid, oid).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return errors.New("assignment exceeds authorization token limits")
	}
	return nil
}
