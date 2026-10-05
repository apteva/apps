package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sync"
)

//go:embed apteva.yaml
var manifest string

type App struct {
	db *sql.DB
	mu sync.Mutex
}

func (*App) Manifest() sdk.Manifest {
	m, e := sdk.ParseManifest([]byte(manifest))
	if e != nil {
		panic(e)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error { a.db = ctx.AppDB(); return nil }
func (*App) OnUnmount(*sdk.AppCtx) error     { return nil }
func token() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func value(args map[string]any, key string) string { s, _ := args[key].(string); return s }
func (a *App) MCPTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, name := range []string{"prepare", "render", "validate", "publish"} {
		name := name
		props := map[string]any{}
		required := []string{}
		if name != "prepare" {
			props["context_id"] = map[string]any{"type": "string"}
			required = append(required, "context_id")
		}
		if name == "render" {
			props["artifact_id"] = map[string]any{"type": "string", "enum": []string{"portrait-3.png", "portrait-4.png"}}
			required = append(required, "artifact_id")
		}
		if name == "publish" {
			props["approval_receipt"] = map[string]any{"type": "string"}
			required = append(required, "approval_receipt")
		}
		out = append(out, sdk.Tool{Name: name, Description: "Test-only durable " + name + " using one thread-owned prepared context; no external Media or publishing calls.", InputSchema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, HandlerCtx: func(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
			return a.call(ctx, name, args)
		}})
	}
	return out
}
func (a *App) call(ctx context.Context, name string, args map[string]any) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	caller := sdk.CallerFrom(ctx)
	if caller == nil || caller.AgentID <= 0 || caller.ProjectID == "" || caller.ThreadID == "" || caller.ThreadID == "main" {
		return nil, errors.New("fixture requires a trusted worker thread")
	}
	id, source := value(args, "context_id"), ""
	if name == "prepare" {
		var count int
		if err := a.db.QueryRow(`SELECT count(*) FROM prepared_contexts`).Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, errors.New("prepared context already exists; reuse it")
		}
		id, source = "context-"+token(), "source-"+token()
		if _, err := a.db.Exec(`INSERT INTO prepared_contexts VALUES(?,?,?,?,?)`, id, source, caller.ProjectID, caller.AgentID, caller.ThreadID); err != nil {
			return nil, err
		}
	} else {
		var project, thread string
		var agent int64
		if err := a.db.QueryRow(`SELECT source_id,project_id,agent_id,thread_id FROM prepared_contexts WHERE id=?`, id).Scan(&source, &project, &agent, &thread); err != nil {
			return nil, err
		}
		if project != caller.ProjectID || agent != caller.AgentID || thread != caller.ThreadID {
			return nil, errors.New("prepared context belongs to another worker")
		}
	}
	receipt := map[string]any{"context_id": id, "source_id": source}
	artifact, digest := value(args, "artifact_id"), ""
	switch name {
	case "render":
		if artifact != "portrait-3.png" && artifact != "portrait-4.png" {
			return nil, errors.New("invalid artifact identity")
		}
		if artifact == "portrait-4.png" {
			var n int
			if err := a.db.QueryRow(`SELECT count(*) FROM operations WHERE context_id=? AND name='render' AND artifact_id='portrait-3.png'`, id).Scan(&n); err != nil {
				return nil, err
			}
			if n != 1 {
				return nil, errors.New("prepared portrait-3 checkpoint must exist first")
			}
		}
		sum := sha256.Sum256([]byte(source + "|" + artifact))
		digest = hex.EncodeToString(sum[:])
		receipt["artifact_id"], receipt["digest"] = artifact, digest
	case "validate":
		for _, key := range []string{"portrait-3.png", "portrait-4.png"} {
			var saved string
			if err := a.db.QueryRow(`SELECT digest FROM operations WHERE context_id=? AND name='render' AND artifact_id=?`, id, key).Scan(&saved); err != nil {
				return nil, err
			}
			sum := sha256.Sum256([]byte(source + "|" + key))
			if saved != hex.EncodeToString(sum[:]) {
				return nil, errors.New("artifact digest mismatch")
			}
		}
		receipt["validated"] = []string{"portrait-3.png", "portrait-4.png"}
	case "publish":
		var n int
		if err := a.db.QueryRow(`SELECT count(*) FROM operations WHERE context_id=? AND name='validate'`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, errors.New("validation required")
		}
		if value(args, "approval_receipt") != "Operator approved: portrait-3.png and portrait-4.png after validation." {
			return nil, errors.New("operator receipt required")
		}
		receipt["receipt"], receipt["artifact_ids"] = "accepted", []string{"portrait-3.png", "portrait-4.png"}
	}
	raw, _ := json.Marshal(receipt)
	if _, err := a.db.Exec(`INSERT INTO operations(context_id,name,artifact_id,digest,receipt_json,tool_call_id) VALUES(?,?,?,?,?,?)`, id, name, artifact, digest, string(raw), caller.ToolCallID); err != nil {
		return nil, fmt.Errorf("duplicate operation: %w", err)
	}
	return receipt, nil
}
func (*App) HTTPRoutes() []sdk.Route           { return nil }
func (*App) Channels() []sdk.ChannelFactory    { return nil }
func (*App) Workers() []sdk.Worker             { return nil }
func (*App) EventHandlers() []sdk.EventHandler { return nil }
func main()                                    { sdk.Run(&App{}) }
