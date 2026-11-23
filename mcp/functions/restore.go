package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// OnRestore runs before Functions serves data restored from a snapshot. The
// SDK has already moved every project_id, so functions, versions and
// invocations are visible under the new project with their ids unchanged
// (API routes and principals that name function ids keep resolving). This
// hook maps what DefaultRestore cannot see:
//
//   - invocation policies admit authenticated callers by app installation id,
//     so those ids follow the restored installs, and issuers that embed the old
//     public base URL follow the new one;
//   - function environments that point at the old runtime's public URL;
//   - the caller installation and project recorded in invocation audit
//     identities;
//   - build_dir, which records an absolute path under the old data directory.
//     Workers are always started from the current build root, so this only
//     keeps the stored path pointing at the copied artifact.
func (a *App) OnRestore(ctx context.Context, _ *sdk.AppCtx, tx *sql.Tx, rc sdk.RestoreContext) error {
	if tx == nil {
		return nil
	}
	if err := remapInvocationPolicies(ctx, tx, rc); err != nil {
		return err
	}
	if _, err := sdk.RewriteOriginColumns(ctx, tx, rc, "functions.env_json", "function_stages.vars_json", "function_invocation_policies.policy_json"); err != nil {
		return err
	}
	if err := remapInvocationIdentities(ctx, tx, rc); err != nil {
		return err
	}
	// The worker pool roots builds at APTEVA_DATA_DIR (see newPool), not at
	// DataDir's DB_PATH fallback.
	return relocateBuildDirs(ctx, tx, os.Getenv("APTEVA_DATA_DIR"))
}

func remapInvocationPolicies(ctx context.Context, tx *sql.Tx, rc sdk.RestoreContext) error {
	if len(rc.InstallIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT function_id, policy_json FROM function_invocation_policies`)
	if err != nil {
		return fmt.Errorf("read invocation policies: %w", err)
	}
	updates := map[int64]string{}
	for rows.Next() {
		var id int64
		var doc string
		if err := rows.Scan(&id, &doc); err != nil {
			rows.Close()
			return err
		}
		next, changed, err := sdk.RemapJSONInstallIDs(doc, rc, "installation_id")
		if err != nil {
			rows.Close()
			return fmt.Errorf("invocation policy of function %d: %w", id, err)
		}
		if changed > 0 {
			updates[id] = next
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for id, doc := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE function_invocation_policies SET policy_json = ? WHERE function_id = ?`, doc, id); err != nil {
			return fmt.Errorf("update invocation policy of function %d: %w", id, err)
		}
	}
	return nil
}

func remapInvocationIdentities(ctx context.Context, tx *sql.Tx, rc sdk.RestoreContext) error {
	if rc.OldProjectID != "" && rc.OldProjectID != rc.NewProjectID {
		if _, err := tx.ExecContext(ctx, `UPDATE function_invocation_identities
			SET identity_json = json_set(identity_json, '$.project_id', ?)
			WHERE json_valid(identity_json) AND json_extract(identity_json, '$.project_id') = ?`, rc.NewProjectID, rc.OldProjectID); err != nil {
			return fmt.Errorf("restore invocation identity project: %w", err)
		}
	}
	if len(rc.InstallIDs) == 0 {
		return nil
	}
	olds := make([]int64, 0, len(rc.InstallIDs))
	for old := range rc.InstallIDs {
		olds = append(olds, old)
	}
	sort.Slice(olds, func(i, j int) bool { return olds[i] < olds[j] })
	// One CASE over every mapping, so an id that is both old and new is
	// never mapped twice.
	var b strings.Builder
	args := make([]any, 0, len(olds)*3)
	b.WriteString(`UPDATE function_invocation_identities SET identity_json = json_set(identity_json, '$.caller_installation_id', CASE json_extract(identity_json, '$.caller_installation_id')`)
	for _, old := range olds {
		b.WriteString(` WHEN ? THEN ?`)
		args = append(args, old, rc.InstallIDs[old])
	}
	b.WriteString(` END) WHERE json_valid(identity_json) AND json_extract(identity_json, '$.caller_installation_id') IN (` + strings.TrimSuffix(strings.Repeat("?,", len(olds)), ",") + `)`)
	for _, old := range olds {
		args = append(args, old)
	}
	if _, err := tx.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("restore invocation identity installs: %w", err)
	}
	return nil
}

// relocateBuildDirs points build_dir at the same artifact under the new data
// directory's build root (<data dir>/functions-build/<artifact key>/<version
// dir>). Paths outside a functions-build root are left alone.
func relocateBuildDirs(ctx context.Context, tx *sql.Tx, dataDir string) error {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	root := filepath.Join(dataDir, "functions-build")
	rows, err := tx.QueryContext(ctx, `SELECT id, build_dir FROM function_versions WHERE COALESCE(build_dir, '') <> ''`)
	if err != nil {
		return fmt.Errorf("read build dirs: %w", err)
	}
	updates := map[int64]string{}
	for rows.Next() {
		var id int64
		var dir string
		if err := rows.Scan(&id, &dir); err != nil {
			rows.Close()
			return err
		}
		clean := filepath.Clean(dir)
		artifact := filepath.Dir(clean)
		if filepath.Base(filepath.Dir(artifact)) != "functions-build" {
			continue
		}
		if next := filepath.Join(root, filepath.Base(artifact), filepath.Base(clean)); next != dir {
			updates[id] = next
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for id, dir := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE function_versions SET build_dir = ? WHERE id = ?`, dir, id); err != nil {
			return fmt.Errorf("relocate build dir of version %d: %w", id, err)
		}
	}
	return nil
}
