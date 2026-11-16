package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const commandCursorTailBytes = 256
const agentOutputWindowBytes = 64 << 10

type commandCursor struct {
	CommandID string `json:"command_id"`
	Length    int    `json:"length"`
	Digest    string `json:"digest"`
	Tail      string `json:"tail"`
}

func encodeCommandCursor(commandID, output string) string {
	tail := output
	if len(tail) > commandCursorTailBytes {
		tail = tail[len(tail)-commandCursorTailBytes:]
	}
	digest := sha256.Sum256([]byte(output))
	raw, _ := json.Marshal(commandCursor{
		CommandID: commandID, Length: len(output), Digest: hex.EncodeToString(digest[:]), Tail: tail,
	})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCommandCursor(value, commandID string) (*commandCursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 1024 {
		return nil, errors.New("cursor is too long")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("invalid command output cursor")
	}
	var cursor commandCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.CommandID != commandID || cursor.Length < 0 || len(cursor.Tail) > commandCursorTailBytes || len(cursor.Digest) != 64 {
		return nil, errors.New("invalid command output cursor")
	}
	return &cursor, nil
}

// The Containers API exposes a bounded tail rather than byte offsets. A cursor
// identifies the previous visible window; when it no longer overlaps the new
// window, gap=true tells the caller that some output was missed.
func incrementalCommandOutput(commandID, output, previous string, truncated bool) (string, string, bool, error) {
	cursor, err := decodeCommandCursor(previous, commandID)
	if err != nil {
		return "", "", false, err
	}
	next := encodeCommandCursor(commandID, output)
	if cursor == nil {
		return output, next, truncated, nil
	}
	digest := sha256.Sum256([]byte(output))
	if cursor.Length == len(output) && cursor.Digest == hex.EncodeToString(digest[:]) {
		return "", next, false, nil
	}
	if cursor.Length == 0 {
		return output, next, truncated, nil
	}
	if cursor.Tail != "" {
		if at := strings.LastIndex(output, cursor.Tail); at >= 0 {
			return output[at+len(cursor.Tail):], next, false, nil
		}
	}
	return output, next, true, nil
}

func (a *App) commandOutput(app *sdk.AppCtx, command *Command, cursor string) (map[string]any, error) {
	logs, err := a.commandLogs(app, command, 2000)
	if err != nil {
		return nil, err
	}
	visible, _ := logs["logs"].(string)
	truncated, _ := logs["output_truncated"].(bool)
	if len(visible) > agentOutputWindowBytes {
		visible = visible[len(visible)-agentOutputWindowBytes:]
		truncated = true
	}
	output, next, gap, err := incrementalCommandOutput(command.ID, visible, cursor, truncated)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id": command.WorkspaceID, "command": command,
		"output": output, "cursor": next, "gap": gap,
		"output_bytes": logs["output_bytes"], "output_truncated": truncated,
	}, nil
}

func (a *App) waitCommand(ctx context.Context, app *sdk.AppCtx, command *Command, cursor string, waitMS int) (map[string]any, error) {
	if waitMS < 0 || waitMS > 20000 {
		return nil, errors.New("wait_ms must be between 0 and 20000")
	}
	if _, err := decodeCommandCursor(cursor, command.ID); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(time.Duration(waitMS) * time.Millisecond)
	defer deadline.Stop()
	for {
		current, err := a.refreshCommand(app, command)
		if err != nil {
			return nil, err
		}
		command = current
		if commandTerminal(command.Status) || waitMS == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return a.commandOutput(app, command, cursor)
		case <-time.After(250 * time.Millisecond):
		}
	}
	return a.commandOutput(app, command, cursor)
}
