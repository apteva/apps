package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// resetClonedA2AState prevents a filesystem clone from duplicating an A2A
// node identity. It removes only the cloned global A2A install's private data;
// its install row, version, config, and agent bindings remain intact and the
// app creates a fresh node identity the next time the clone starts.
func (a *App) resetClonedA2AState(ctx *sdk.AppCtx, host fleetHost, configDir string) (bool, error) {
	if host.IsLocal() {
		return resetLocalClonedA2AState(configDir)
	}
	const script = `import os, shutil, sqlite3, sys
root = os.path.realpath(sys.argv[1])
db_path = os.path.join(root, "apteva.db")
if not os.path.isfile(db_path):
    print("absent")
    raise SystemExit(0)
db = sqlite3.connect("file:" + db_path + "?mode=ro", uri=True)
tables = db.execute("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('apps','app_installs')").fetchone()[0]
if tables != 2:
    db.close()
    print("absent")
    raise SystemExit(0)
row = db.execute("SELECT ai.id FROM app_installs ai JOIN apps a ON a.id=ai.app_id WHERE a.name='a2a' AND COALESCE(ai.project_id,'')='' LIMIT 1").fetchone()
db.close()
if row is None:
    print("absent")
    raise SystemExit(0)
install_id = int(row[0])
if install_id <= 0:
    raise RuntimeError("invalid A2A install id")
data_dir = os.path.realpath(os.path.join(root, "apps", "a2a", "data", str(install_id)))
expected = os.path.realpath(os.path.join(root, "apps", "a2a", "data")) + os.sep
if not data_dir.startswith(expected):
    raise RuntimeError("A2A data path escaped tenant root")
shutil.rmtree(data_dir, ignore_errors=True)
print("reset")`
	out, code, err := instanceRunCommand(ctx, host.InstanceID,
		fmt.Sprintf("python3 -c %s %s", sh(script), sh(configDir)), 20)
	if err != nil || code != 0 {
		return false, fmt.Errorf("reset cloned A2A state on instance %d (exit=%d): %s: %w",
			host.InstanceID, code, strings.TrimSpace(out), err)
	}
	return strings.Contains(out, "reset"), nil
}

func resetLocalClonedA2AState(configDir string) (bool, error) {
	dbPath := filepath.Join(configDir, "apteva.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return false, err
	}
	defer db.Close()
	var tableCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('apps', 'app_installs')`).Scan(&tableCount); err != nil {
		return false, err
	}
	if tableCount != 2 {
		return false, nil
	}
	var installID int64
	err = db.QueryRow(`
		SELECT ai.id
		FROM app_installs ai
		JOIN apps a ON a.id = ai.app_id
		WHERE a.name = 'a2a' AND COALESCE(ai.project_id, '') = ''
		LIMIT 1`).Scan(&installID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if installID <= 0 {
		return false, fmt.Errorf("invalid cloned A2A install id %d", installID)
	}
	dataRoot := filepath.Join(configDir, "apps", "a2a", "data")
	dataDir := filepath.Join(dataRoot, fmt.Sprintf("%d", installID))
	rel, err := filepath.Rel(dataRoot, dataDir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false, errors.New("A2A data path escaped tenant root")
	}
	if err := os.RemoveAll(dataDir); err != nil {
		return false, err
	}
	return true, nil
}
