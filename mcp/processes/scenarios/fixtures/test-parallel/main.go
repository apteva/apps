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
	"time"
)

//go:embed apteva.yaml
var manifest string

const approval = "Operator approved: exact parallel artifacts and session checkpoints."

type App struct {
	db        *sql.DB
	mu        sync.Mutex
	workDelay time.Duration
}

func (*App) Manifest() sdk.Manifest {
	m, e := sdk.ParseManifest([]byte(manifest))
	if e != nil {
		panic(e)
	}
	return *m
}
func (a *App) OnMount(ctx *sdk.AppCtx) error  { a.db = ctx.AppDB(); return nil }
func (*App) OnUnmount(*sdk.AppCtx) error      { return nil }
func value(m map[string]any, k string) string { s, _ := m[k].(string); return s }
func token() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (a *App) MCPTools() []sdk.Tool {
	out := []sdk.Tool{}
	for _, n := range []string{"prepare", "work", "session", "validate", "publish"} {
		name := n
		props := map[string]any{}
		required := []string{}
		if name != "prepare" {
			props["context_id"] = map[string]any{"type": "string"}
			required = append(required, "context_id")
		}
		if name == "work" {
			props["artifact_id"] = map[string]any{"type": "string", "enum": []string{"alpha.png", "beta.png"}}
			required = append(required, "artifact_id")
		}
		if name == "session" {
			props["phase"] = map[string]any{"type": "string", "enum": []string{"a", "b"}}
			required = append(required, "phase")
		}
		if name == "publish" {
			props["approval_receipt"] = map[string]any{"type": "string"}
			required = append(required, "approval_receipt")
		}
		out = append(out, sdk.Tool{Name: name, Description: map[string]string{"prepare": "Prepare exactly once; source reference is transferable, session remains bound to this worker.", "work": "Independent 45-second artifact operation. Context is transferable to this agent's children in this project. No shared mutable session.", "session": "Owner-thread-only operation; b requires saved a. Do not delegate this session.", "validate": "Owner validates both exact artifact digests and session a/b checkpoints.", "publish": "Owner records local publication only after validation and exact operator approval."}[name], InputSchema: map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, HandlerCtx: func(ctx context.Context, _ *sdk.AppCtx, args map[string]any) (any, error) {
			return a.call(ctx, name, args)
		}})
	}
	return out
}
func (a *App) begin(ctx context.Context, name string, args map[string]any) (string, string, int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := sdk.CallerFrom(ctx)
	if c == nil || c.ProjectID == "" || c.AgentID <= 0 || c.ThreadID == "" || c.ThreadID == "main" {
		return "", "", 0, errors.New("trusted worker required")
	}
	id, source := value(args, "context_id"), ""
	if name == "prepare" {
		var n int
		if e := a.db.QueryRow(`SELECT count(*) FROM contexts`).Scan(&n); e != nil {
			return "", "", 0, e
		}
		if n != 0 {
			return "", "", 0, errors.New("already prepared")
		}
		id, source = "context-"+token(), "source-"+token()
		if _, e := a.db.Exec(`INSERT INTO contexts VALUES(?,?,?,?,?)`, id, source, c.ProjectID, c.AgentID, c.ThreadID); e != nil {
			return "", "", 0, e
		}
	} else {
		var project, owner string
		var agent int64
		if e := a.db.QueryRow(`SELECT source_id,project_id,agent_id,thread_id FROM contexts WHERE id=?`, id).Scan(&source, &project, &agent, &owner); e != nil {
			return "", "", 0, e
		}
		if c.ProjectID != project || c.AgentID != agent || (name != "work" && c.ThreadID != owner) {
			return "", "", 0, errors.New("wrong project, executor or session owner")
		}
	}
	artifact := value(args, "artifact_id")
	if name == "work" && artifact != "alpha.png" && artifact != "beta.png" {
		return "", "", 0, errors.New("invalid artifact")
	}
	if name == "session" {
		artifact = value(args, "phase")
		if artifact != "a" && artifact != "b" {
			return "", "", 0, errors.New("invalid session phase")
		}
		if artifact == "b" {
			var n int
			a.db.QueryRow(`SELECT count(*) FROM operations WHERE context_id=? AND name='session' AND artifact_id='a' AND completed_at>0`, id).Scan(&n)
			if n != 1 {
				return "", "", 0, errors.New("phase a checkpoint required")
			}
		}
	}
	if name == "validate" || name == "publish" {
		for _, required := range [][2]string{{"work", "alpha.png"}, {"work", "beta.png"}, {"session", "a"}, {"session", "b"}} {
			var raw string
			if e := a.db.QueryRow(`SELECT receipt_json FROM operations WHERE context_id=? AND name=? AND artifact_id=? AND completed_at>0`, id, required[0], required[1]).Scan(&raw); e != nil {
				return "", "", 0, errors.New("required completed evidence missing")
			}
			if required[0] == "work" {
				var saved map[string]any
				if e := json.Unmarshal([]byte(raw), &saved); e != nil || saved["digest"] != digest(source, required[1]) {
					return "", "", 0, errors.New("digest mismatch")
				}
			}
		}
	}
	if name == "publish" {
		var n int
		a.db.QueryRow(`SELECT count(*) FROM operations WHERE context_id=? AND name='validate' AND completed_at>0`, id).Scan(&n)
		if n != 1 || value(args, "approval_receipt") != approval {
			return "", "", 0, errors.New("validation and operator approval required")
		}
	}
	res, e := a.db.Exec(`INSERT INTO operations(context_id,name,artifact_id,thread_id,tool_call_id,started_at) VALUES(?,?,?,?,?,?)`, id, name, artifact, c.ThreadID, c.ToolCallID, time.Now().UnixMilli())
	if e != nil {
		return "", "", 0, fmt.Errorf("duplicate operation: %w", e)
	}
	op, e := res.LastInsertId()
	return id, source, op, e
}
func digest(source, artifact string) string {
	s := sha256.Sum256([]byte(source + "|" + artifact))
	return hex.EncodeToString(s[:])
}
func (a *App) call(ctx context.Context, name string, args map[string]any) (any, error) {
	id, source, op, e := a.begin(ctx, name, args)
	if e != nil {
		return nil, e
	}
	if name == "work" {
		delay := a.workDelay
		if delay == 0 {
			delay = 45 * time.Second
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	receipt := map[string]any{"context_id": id, "source_id": source, "operation_id": op}
	switch name {
	case "work":
		artifact := value(args, "artifact_id")
		receipt["artifact_id"], receipt["digest"] = artifact, digest(source, artifact)
	case "session":
		receipt["phase"], receipt["checkpoint"] = value(args, "phase"), "saved"
	case "validate":
		receipt["validated"] = []string{"alpha.png", "beta.png", "session-a", "session-b"}
	case "publish":
		receipt["receipt"], receipt["artifact_ids"] = "accepted", []string{"alpha.png", "beta.png"}
	}
	raw, _ := json.Marshal(receipt)
	_, e = a.db.Exec(`UPDATE operations SET completed_at=?,receipt_json=? WHERE id=?`, time.Now().UnixMilli(), string(raw), op)
	return receipt, e
}
func (*App) HTTPRoutes() []sdk.Route           { return nil }
func (*App) Channels() []sdk.ChannelFactory    { return nil }
func (*App) Workers() []sdk.Worker             { return nil }
func (*App) EventHandlers() []sdk.EventHandler { return nil }
func main()                                    { sdk.Run(&App{}) }
