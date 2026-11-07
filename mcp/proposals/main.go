package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	app     *sdk.AppCtx
	project string
	store   store
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic(err)
	}
	return *m
}

func (a *App) OnMount(app *sdk.AppCtx) error {
	if app.AppDB() == nil || app.CurrentProject() == "" {
		return errors.New("proposals requires a project-scoped installation with a database")
	}
	db := app.AppDB()
	key := make([]byte, 32)
	rand.Read(key)
	if _, err := db.Exec("INSERT OR IGNORE INTO app_secrets(name, secret) VALUES ('link', ?)", key); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT secret FROM app_secrets WHERE name = 'link'").Scan(&key); err != nil {
		return err
	}
	a.app, a.project, a.store = app, app.CurrentProject(), store{db, key}
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{Name: "proposal-events", Schedule: "@every 10s", Run: a.publishEvents}}
}

func (a *App) publishEvents(ctx context.Context, app *sdk.AppCtx) error {
	bus := app.EventBusAPI()
	if bus == nil {
		return errors.New("app event bus unavailable")
	}
	for range 25 {
		var id int64
		var topic string
		var data string
		err := a.store.db.QueryRowContext(ctx, `
SELECT id, topic, payload FROM outbox WHERE project_id = ? AND delivered_at IS NULL ORDER BY id LIMIT 1`, a.project).Scan(&id, &topic, &data)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := bus.PublishAppEvent(a.project, "proposals:"+strconv.FormatInt(id, 10), topic, json.RawMessage(data)); err != nil {
			return fmt.Errorf("publish %s: %w", topic, err)
		}
		if _, err := a.store.db.ExecContext(ctx, "UPDATE outbox SET delivered_at = ? WHERE id = ?", time.Now().Unix(), id); err != nil {
			return err
		}
	}
	return nil
}
