package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var workspaceNameRE = regexp.MustCompile(`^[[:alnum:]][[:alnum:] _.-]{0,79}$`)

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

func newID(prefix string) string {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}

func projectIDFrom(callCtx context.Context, app *sdk.AppCtx) (string, error) {
	if caller := sdk.CallerFrom(callCtx); caller != nil && strings.TrimSpace(caller.ProjectID) != "" {
		return strings.TrimSpace(caller.ProjectID), nil
	}
	if app != nil && strings.TrimSpace(app.CurrentProject()) != "" {
		return strings.TrimSpace(app.CurrentProject()), nil
	}
	return "", errors.New("trusted project context required")
}

func actorFrom(callCtx context.Context, app *sdk.AppCtx) (Actor, error) {
	projectID, err := projectIDFrom(callCtx, app)
	if err != nil {
		return Actor{}, err
	}
	actor := Actor{Kind: "system", ID: "workspaces", Label: "Workspaces", ProjectID: projectID}
	caller := sdk.CallerFrom(callCtx)
	if caller == nil {
		return actor, nil
	}
	actor.AgentID = caller.AgentID
	actor.ThreadID = strings.TrimSpace(caller.ThreadID)
	actor.AppName = strings.TrimSpace(caller.AppName)
	actor.InstallID = caller.AppInstallID
	switch {
	case caller.AppInstallID > 0 && actor.AppName != "":
		actor.Kind = "app"
		actor.ID = strconv.FormatInt(caller.AppInstallID, 10)
		actor.Label = actor.AppName
	case caller.AgentID > 0:
		actor.Kind = "agent"
		actor.ID = strconv.FormatInt(caller.AgentID, 10)
		actor.Label = "Agent " + actor.ID
	case caller.SubjectType != "" && caller.SubjectID != "":
		actor.Kind = caller.SubjectType
		actor.ID = caller.SubjectID
		actor.Label = firstNonEmpty(caller.SubjectEmail, caller.SubjectID)
	}
	return actor, nil
}

func callerIdempotencyKey(callCtx context.Context, actor Actor) string {
	caller := sdk.CallerFrom(callCtx)
	if caller == nil || strings.TrimSpace(caller.ToolCallID) == "" {
		return ""
	}
	return actor.Kind + ":" + actor.ID + ":" + strings.TrimSpace(caller.ToolCallID)
}

func operatorActor(app *sdk.AppCtx) Actor {
	project := ""
	if app != nil {
		project = strings.TrimSpace(app.CurrentProject())
	}
	return Actor{Kind: "user", ID: "operator", Label: "Operator", ProjectID: project}
}

func canAccess(actor Actor, w *Workspace) bool {
	if w == nil || actor.ProjectID == "" || actor.ProjectID != w.ProjectID {
		return false
	}
	if actor.Kind == "user" || actor.Kind == "system" {
		return true
	}
	if actor.InstallID > 0 {
		return w.ConsumerInstallID > 0 && actor.InstallID == w.ConsumerInstallID
	}
	if actor.AgentID > 0 && w.OwnerAgentID > 0 {
		if actor.AgentID != w.OwnerAgentID {
			return false
		}
		return w.OwnerThreadID == "" || actor.ThreadID == w.OwnerThreadID
	}
	return actor.AgentID > 0 && w.OwnerAgentID == 0
}

func normalizeWorkspaceName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !workspaceNameRE.MatchString(value) {
		return "", errors.New("name must be 1-80 characters and use letters, numbers, spaces, dots, dashes, or underscores")
	}
	return value, nil
}

func normalizeOriginHref(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("origin_href must be a same-origin absolute path beginning with /")
	}
	return value, nil
}

func normalizeWorkingDirectory(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("working_directory must be an absolute path under /workspace")
	}
	clean := path.Clean(value)
	if clean != "/workspace" && !strings.HasPrefix(clean, "/workspace/") {
		return "", errors.New("working_directory must be /workspace or one of its children")
	}
	return clean, nil
}

func normalizeCommand(args map[string]any, defaultTimeout int) ([]string, string, string, int, error) {
	var argv []string
	if raw, ok := args["argv"].([]any); ok {
		for _, value := range raw {
			s, ok := value.(string)
			if !ok {
				return nil, "", "", 0, errors.New("argv must contain strings")
			}
			argv = append(argv, s)
		}
	} else if raw, ok := args["argv"].([]string); ok {
		argv = append(argv, raw...)
	}
	shell := strings.TrimSpace(strArg(args, "shell_command"))
	if len(argv) > 0 && shell != "" {
		return nil, "", "", 0, errors.New("set either argv or shell_command, not both")
	}
	display := ""
	if shell != "" {
		if len(shell) > 64*1024 {
			return nil, "", "", 0, errors.New("shell_command exceeds 65536 bytes")
		}
		argv = []string{"/bin/sh", "-c", shell}
		display = shell
	}
	if len(argv) == 0 || len(argv) > 256 || strings.TrimSpace(argv[0]) == "" {
		return nil, "", "", 0, errors.New("argv or shell_command is required")
	}
	for i, arg := range argv {
		if strings.IndexByte(arg, 0) >= 0 || len(arg) > 64*1024 {
			return nil, "", "", 0, fmt.Errorf("argv[%d] is invalid", i)
		}
	}
	if display == "" {
		display = displayArgv(argv)
	}
	workingDirectory, err := normalizeWorkingDirectory(strArg(args, "working_directory"))
	if err != nil {
		return nil, "", "", 0, err
	}
	timeout := intArg(args, "timeout_s", defaultTimeout)
	if timeout < 1 || timeout > 86400 {
		return nil, "", "", 0, errors.New("timeout_s must be between 1 and 86400")
	}
	return argv, display, workingDirectory, timeout, nil
}

func displayArgv(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\"'") {
			parts = append(parts, arg)
			continue
		}
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func configString(app *sdk.AppCtx, key, fallback string) string {
	if app == nil {
		return fallback
	}
	if value := strings.TrimSpace(app.Config()[key]); value != "" {
		return value
	}
	return fallback
}

func configInt(app *sdk.AppCtx, key string, fallback int) int {
	value := configString(app, key, "")
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func configFloat(app *sdk.AppCtx, key string, fallback float64) float64 {
	value := configString(app, key, "")
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func resolveProfile(app *sdk.AppCtx, requested string) (string, string, error) {
	profile := strings.ToLower(strings.TrimSpace(requested))
	if profile == "" {
		profile = strings.ToLower(configString(app, "default_profile", "go"))
	}
	var image string
	switch profile {
	case "go":
		image = configString(app, "go_image", "golang:1.25-bookworm")
	case "bun":
		image = configString(app, "bun_image", "oven/bun:1-debian")
	case "python":
		image = configString(app, "python_image", "python:3.13-bookworm")
	case "apteva":
		image = configString(app, "apteva_image", "")
	default:
		return "", "", fmt.Errorf("unsupported profile %q; use go, bun, python, or apteva", profile)
	}
	if image == "" {
		return "", "", fmt.Errorf("profile %q is not configured with an image", profile)
	}
	return profile, image, nil
}

func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func strArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func intArg(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func int64Arg(args map[string]any, key string) int64 {
	return int64(intArg(args, key, 0))
}

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func schemaObject(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func strSchema() map[string]any  { return map[string]any{"type": "string"} }
func intSchema() map[string]any  { return map[string]any{"type": "integer"} }
func boolSchema() map[string]any { return map[string]any{"type": "boolean"} }
