package main

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// GameScope is the mandatory boundary for all game data operations.
type GameScope struct {
	ProjectID string
	GameID    string
}
type DBTX interface {
	Exec(string, ...any) (sql.Result, error)
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}
type Game struct {
	ID               string `json:"id"`
	ProjectID        string `json:"project_id"`
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Status           string `json:"status"`
	AuthOrganization string `json:"auth_organization_slug"`
	Legacy           bool   `json:"legacy"`
	CreatedAt        string `json:"created_at"`
}

func (g Game) Scope() GameScope { return GameScope{g.ProjectID, g.ID} }
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

//go:embed schema_v2.sql
var schemaV2 string
var provisioningMu sync.Mutex

const gameTables = `CREATE TABLE IF NOT EXISTS game_legacy_auth_bans(project_id TEXT NOT NULL,game_id TEXT NOT NULL,auth_user_id INTEGER NOT NULL,reason TEXT NOT NULL,PRIMARY KEY(project_id,game_id,auth_user_id));
CREATE TABLE IF NOT EXISTS game_login_tickets(token_hash TEXT PRIMARY KEY,project_id TEXT NOT NULL,game_id TEXT NOT NULL,subject TEXT NOT NULL,expires_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS games (
 id TEXT PRIMARY KEY, project_id TEXT NOT NULL, slug TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
 auth_organization_slug TEXT NOT NULL, legacy INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, UNIQUE(project_id,slug), UNIQUE(project_id,id));
 CREATE UNIQUE INDEX IF NOT EXISTS games_legacy ON games(project_id) WHERE legacy=1;
 CREATE TABLE IF NOT EXISTS game_schema(version INTEGER PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS game_operations(project_id TEXT NOT NULL,game_id TEXT NOT NULL, key TEXT NOT NULL, fingerprint TEXT NOT NULL, result TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY(project_id,game_id,key), FOREIGN KEY(project_id,game_id) REFERENCES games(project_id,id));
 CREATE TABLE IF NOT EXISTS game_outbox(id INTEGER PRIMARY KEY AUTOINCREMENT,project_id TEXT NOT NULL,game_id TEXT NOT NULL,topic TEXT NOT NULL,payload TEXT NOT NULL,analytics INTEGER NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', next_attempt TEXT NOT NULL,created_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS game_tombstones(project_id TEXT NOT NULL,game_id TEXT NOT NULL,auth_user_id INTEGER NOT NULL,PRIMARY KEY(project_id,game_id,auth_user_id));
 CREATE TABLE IF NOT EXISTS game_limits(project_id TEXT NOT NULL,game_id TEXT NOT NULL,bucket TEXT NOT NULL,window INTEGER NOT NULL,count INTEGER NOT NULL,PRIMARY KEY(project_id,game_id,bucket));`

// The SDK runs SQL files without a surrounding transaction. Run this rebuild
// before serving routes and keep its version marker in the same transaction.
func initializeGames(ctx *sdk.AppCtx) error {
	db := ctx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(gameTables); err != nil {
		return err
	}
	// Also tolerate an existing v0.2 development database without descriptions.
	columns, e := tx.Query(`PRAGMA table_info(games)`)
	if e != nil {
		return e
	}
	hasDescription := false
	for columns.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if e = columns.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
			columns.Close()
			return e
		}
		if name == "description" {
			hasDescription = true
		}
	}
	e = columns.Err()
	columns.Close()
	if e != nil {
		return e
	}
	if !hasDescription {
		if _, err = tx.Exec(`ALTER TABLE games ADD COLUMN description TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	var version int
	err = tx.QueryRow(`SELECT version FROM game_schema WHERE version=2`).Scan(&version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if version != 2 {
		tables := []string{"settings", "players", "player_bans", "player_audit", "player_data", "stat_defs", "player_stats", "leaderboards", "leaderboard_entries", "achievement_defs", "player_achievements"}
		projects := map[string]bool{}
		for _, table := range tables {
			rows, e := tx.Query("SELECT DISTINCT project_id FROM " + table)
			if e != nil {
				return e
			}
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					rows.Close()
					return e
				}
				projects[id] = true
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		if id := envProject(); id != "" {
			projects[id] = true
		}
		for id := range projects {
			if _, err = tx.Exec(`INSERT INTO games(id,project_id,slug,name,auth_organization_slug,legacy,created_at) VALUES(?,?,'legacy','Legacy game',?,1,?)`, "legacy-"+id, id, cfgStr(ctx, "auth_organization_slug", "default"), nowRFC()); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(schemaV2); err != nil {
			return fmt.Errorf("create game schema: %w", err)
		}
		for _, table := range tables {
			rows, e := tx.Query("PRAGMA table_info(" + table + ")")
			if e != nil {
				return e
			}
			cols := []string{}
			sel := []string{}
			for rows.Next() {
				var cid, notnull, pk int
				var name, typ string
				var def any
				if e = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
					rows.Close()
					return e
				}
				cols = append(cols, name)
				sel = append(sel, "old."+name)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			query := "INSERT INTO " + table + "_v2(" + strings.Join(cols, ",") + ",game_id) SELECT " + strings.Join(sel, ",") + ",g.id FROM " + table + " old JOIN games g ON g.project_id=old.project_id AND g.legacy=1"
			if _, err = tx.Exec(query); err != nil {
				return fmt.Errorf("copy %s: %w", table, err)
			}
			var before, after int64
			if err = tx.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&before); err != nil {
				return err
			}
			if err = tx.QueryRow("SELECT COUNT(*) FROM " + table + "_v2").Scan(&after); err != nil {
				return err
			}
			if before != after {
				return fmt.Errorf("migration count mismatch: %s", table)
			}
		}
		// Children first: foreign_keys stays ON throughout the migration.
		for _, table := range []string{"player_achievements", "leaderboard_entries", "player_stats", "player_data", "player_bans", "player_audit", "settings", "achievement_defs", "stat_defs", "leaderboards", "players"} {
			if _, err = tx.Exec("DROP TABLE " + table); err != nil {
				return err
			}
		}
		for _, table := range tables {
			if _, err = tx.Exec("ALTER TABLE " + table + "_v2 RENAME TO " + table); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(`INSERT INTO game_legacy_auth_bans(project_id,game_id,auth_user_id,reason) SELECT p.project_id,p.game_id,p.auth_user_id,COALESCE(b.reason,'') FROM players p JOIN player_bans b ON b.player_id=p.id AND b.id=(SELECT MAX(id) FROM player_bans WHERE player_id=p.id AND lifted_at IS NULL) WHERE p.status='banned'`); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO game_schema(version) VALUES(2)`); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	bad := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if bad {
		return errors.New("foreign key integrity check failed")
	}
	return tx.Commit()
}

func ensureLegacyGame(ctx *sdk.AppCtx, project string) (*Game, error) {
	if project == "" {
		return nil, errors.New("project required")
	}
	if g, e := getGame(ctx.AppDB(), project, "legacy-"+project); e == nil {
		return g, nil
	}
	_, err := ctx.AppDB().Exec(`INSERT OR IGNORE INTO games(id,project_id,slug,name,auth_organization_slug,legacy,created_at) VALUES(?,?,'legacy','Legacy game',?,1,?)`, "legacy-"+project, project, cfgStr(ctx, "auth_organization_slug", "default"), nowRFC())
	if err != nil {
		return nil, err
	}
	return getGame(ctx.AppDB(), project, "legacy-"+project)
}

const gameCols = `id,project_id,slug,name,description,status,auth_organization_slug,legacy,created_at`

func scanGame(row rowScanner) (*Game, error) {
	var g Game
	err := row.Scan(&g.ID, &g.ProjectID, &g.Slug, &g.Name, &g.Description, &g.Status, &g.AuthOrganization, &g.Legacy, &g.CreatedAt)
	return &g, err
}
func getGame(db DBTX, project, id string) (*Game, error) {
	g, err := scanGame(db.QueryRow(`SELECT `+gameCols+` FROM games WHERE project_id=? AND id=?`, project, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("game not found")
	}
	return g, err
}
func listGames(db DBTX, project string) ([]Game, error) {
	rows, err := db.Query(`SELECT `+gameCols+` FROM games WHERE project_id=? ORDER BY created_at,id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Game{}
	for rows.Next() {
		g, e := scanGame(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}
func selectGame(ctx *sdk.AppCtx, project, id string, legacy bool) (GameScope, error) {
	if ctx == nil || ctx.AppDB() == nil {
		return GameScope{}, errors.New("games unavailable")
	}
	var g *Game
	var err error
	if legacy {
		g, err = ensureLegacyGame(ctx, project)
		if id != "" && err == nil && id != g.ID {
			return GameScope{}, errors.New("v1 is permanently bound to the legacy game")
		}
	} else if id != "" {
		g, err = getGame(ctx.AppDB(), project, id)
	} else {
		var all []Game
		all, err = listGames(ctx.AppDB(), project)
		if err != nil {
			return GameScope{}, err
		}
		for i := range all {
			if all[i].Status == "active" {
				if g != nil {
					return GameScope{}, errors.New("game_id required: project has multiple games")
				}
				g = &all[i]
			}
		}
		if g == nil {
			return GameScope{}, errors.New("game_id required: create or restore a game")
		}
	}
	if err != nil {
		return GameScope{}, err
	}
	if g.Status != "active" {
		return GameScope{}, errors.New("game is archived")
	}
	return g.Scope(), nil
}
func resolveGameFromArgs(ctx *sdk.AppCtx, args map[string]any) (GameScope, error) {
	p, err := resolveProjectFromArgs(args)
	if err != nil {
		return GameScope{}, err
	}
	return selectGame(ctx, p, stringArg(args, "game_id", ""), false)
}
func resolveGameFromRequest(ctx *sdk.AppCtx, r *http.Request) (GameScope, error) {
	p, err := resolveProjectFromRequest(r)
	if err != nil {
		return GameScope{}, err
	}
	id := r.PathValue("game_id")
	if id == "" {
		id = r.URL.Query().Get("game_id")
	}
	return selectGame(ctx, p, id, strings.HasPrefix(r.URL.Path, "/v1/"))
}

func gameAction(ctx *sdk.AppCtx, action string, args map[string]any) (any, error) {
	project, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if action == "list" {
		all, err := listGames(ctx.AppDB(), project)
		return map[string]any{"games": all}, err
	}
	if action == "create" {
		slug := strings.ToLower(stringArg(args, "slug", ""))
		name := stringArg(args, "name", "")
		description, err := gameDescription(args)
		if err != nil {
			return nil, err
		}
		if !slugRe.MatchString(slug) || slug == "legacy" || name == "" || len(name) > 100 {
			return nil, errors.New("valid slug and name (1-100 bytes) required; legacy is reserved")
		}
		id := randomID()
		org := "game-" + id[:24]
		if _, err = ctx.AppDB().Exec(`INSERT INTO games(id,project_id,slug,name,description,auth_organization_slug,created_at) VALUES(?,?,?,?,?,?,?)`, id, project, slug, name, description, org, nowRFC()); err != nil {
			return nil, err
		}
		g, err := getGame(ctx.AppDB(), project, id)
		return map[string]any{"game": g}, err
	}
	g, err := getGame(ctx.AppDB(), project, stringArg(args, "game_id", ""))
	if err != nil {
		return nil, err
	}
	switch action {
	case "get":
	case "update":
		name := g.Name
		description := g.Description
		if _, ok := args["name"]; ok {
			name = stringArg(args, "name", "")
		}
		if _, ok := args["description"]; ok {
			description, err = gameDescription(args)
			if err != nil {
				return nil, err
			}
		}
		if name == "" || len(name) > 100 {
			return nil, errors.New("name must be 1-100 bytes")
		}
		_, err = ctx.AppDB().Exec(`UPDATE games SET name=?,description=? WHERE project_id=? AND id=?`, name, description, project, g.ID)
	case "archive", "restore":
		status := "active"
		if action == "archive" {
			status = "archived"
		}
		_, err = ctx.AppDB().Exec(`UPDATE games SET status=? WHERE project_id=? AND id=?`, status, project, g.ID)
	default:
		return nil, errors.New("unknown game action")
	}
	if err != nil {
		return nil, err
	}
	g, err = getGame(ctx.AppDB(), project, g.ID)
	pending, failed := 0, 0
	if err == nil {
		err = ctx.AppDB().QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN attempts>=10 THEN 1 ELSE 0 END),0) FROM game_outbox WHERE project_id=? AND game_id=?`, project, g.ID).Scan(&pending, &failed)
	}
	return map[string]any{"game": g, "events_pending": pending, "events_failed": failed}, err
}
func gameTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, action := range []string{"create", "list", "get", "update", "archive", "restore"} {
		action := action
		req := []string{}
		if action != "list" && action != "create" {
			req = append(req, "game_id")
		}
		if action == "create" {
			req = append(req, "name", "slug")
		}
		out = append(out, sdk.Tool{Name: "games_" + action, Description: action + " a game within this project", InputSchema: schemaObject(map[string]any{"game_id": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "slug": map[string]any{"type": "string"}, "description": map[string]any{"type": "string", "maxLength": 4000, "description": "Optional description of the game."}}, req), Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) { return gameAction(ctx, action, args) }})
	}
	return out
}
func (a *App) handleGames(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	if r.Method != "GET" {
		if err := decodeBody(w, r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
	}
	args["game_id"] = r.PathValue("game_id")
	args["_project_id"] = r.URL.Query().Get("project_id")
	action := "list"
	switch r.Method {
	case "GET":
		if r.PathValue("game_id") != "" {
			action = "get"
		}
	case "POST":
		action = "create"
		if strings.HasSuffix(r.URL.Path, "/archive") {
			action = "archive"
		}
		if strings.HasSuffix(r.URL.Path, "/restore") {
			action = "restore"
		}
	case "PATCH":
		action = "update"
	}
	out, err := gameAction(getAppCtx(r), action, args)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, out)
}

func marshalResult(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

func gameDescription(args map[string]any) (string, error) {
	value, exists := args["description"]
	if !exists {
		return "", nil
	}
	description, ok := value.(string)
	if !ok {
		return "", errors.New("description must be text")
	}
	if len(description) > 4000 {
		return "", errors.New("description must be at most 4000 bytes")
	}
	return strings.TrimSpace(description), nil
}
