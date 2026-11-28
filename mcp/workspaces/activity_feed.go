package main

// Project-wide activity for the Home overview widget. Workspace activity is
// recorded per event; the feed folds it into short lines: bursts of file
// writes by one actor in one workspace become one line, and a command's
// "started" line is dropped once its outcome is in the feed.
import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	fileBurstWindow   = 2 * time.Minute
	activityBuckets   = 12
	activityBucketLen = 5 * time.Minute
)

type ActivityLine struct {
	WorkspaceID   string          `json:"workspace_id"`
	WorkspaceName string          `json:"workspace_name"`
	EventType     string          `json:"event_type"`
	Category      string          `json:"category"`
	Severity      string          `json:"severity,omitempty"`
	ActorKind     string          `json:"actor_kind"`
	ActorLabel    string          `json:"actor_label"`
	Summary       string          `json:"summary"`
	Count         int             `json:"count"`
	Data          json.RawMessage `json:"data,omitempty"`
	FirstAt       string          `json:"first_at"`
	CreatedAt     string          `json:"created_at"`
}

type activityQuery struct {
	Limit       int
	Kinds       string // all | commands | changes
	IncludeTest bool
	WorkspaceID string
}

func activityCategory(eventType string) string {
	switch {
	case strings.HasPrefix(eventType, "command."):
		return "command"
	case strings.HasPrefix(eventType, "file."):
		return "file"
	case strings.HasPrefix(eventType, "source."), strings.HasPrefix(eventType, "results."):
		return "source"
	case strings.HasPrefix(eventType, "preview."):
		return "preview"
	default:
		return "lifecycle"
	}
}

func activitySeverity(eventType string) string {
	switch eventType {
	case "command.failed", "command.timed_out", "workspace.create_failed", "preview.crashed", "source.import_failed":
		return "error"
	case "command.cancelled", "workspace.expired":
		return "warning"
	}
	return ""
}

func listProjectActivity(db *sql.DB, projectID string, q activityQuery) ([]*ActivityLine, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	where := []string{"a.project_id=?"}
	args := []any{projectID}
	switch q.Kinds {
	case "", "all":
	case "commands":
		where = append(where, "a.event_type LIKE 'command.%'")
	case "changes":
		where = append(where, "(a.event_type LIKE 'command.%' OR a.event_type LIKE 'file.%' OR a.event_type LIKE 'source.%' OR a.event_type LIKE 'results.%')")
	default:
		return nil, errors.New("kinds must be all, commands or changes")
	}
	if !q.IncludeTest {
		where = append(where, "w.kind='dev'")
	}
	if q.WorkspaceID != "" {
		where = append(where, "a.workspace_id=?")
		args = append(args, q.WorkspaceID)
	}
	// File bursts collapse, so read well past the line limit.
	args = append(args, limit*25)
	rows, err := db.Query(`SELECT a.workspace_id, w.name, a.event_type, a.actor_kind, a.actor_id, a.actor_label,
		a.summary, a.data_json, a.created_at
		FROM workspace_activity a JOIN workspaces w ON w.id=a.workspace_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY a.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*ActivityLine, 0, limit)
	openBurst := map[string]*ActivityLine{} // workspace+actor -> newest file line
	finished := map[string]bool{}           // command ids whose outcome is listed
	for rows.Next() {
		var line ActivityLine
		var actorID, data string
		if err := rows.Scan(&line.WorkspaceID, &line.WorkspaceName, &line.EventType, &line.ActorKind, &actorID, &line.ActorLabel, &line.Summary, &data, &line.CreatedAt); err != nil {
			return nil, err
		}
		if line.EventType == "workspace.provisioning" {
			continue // "created" or "create_failed" follows and says more
		}
		line.Category = activityCategory(line.EventType)
		line.Severity = activitySeverity(line.EventType)
		line.Count = 1
		line.FirstAt = line.CreatedAt
		if data != "" && data != "{}" {
			line.Data = json.RawMessage(data)
		}
		key := line.WorkspaceID + "\x00" + line.ActorKind + "\x00" + actorID
		if line.Category == "file" {
			if burst := openBurst[key]; burst != nil && parseTime(burst.FirstAt).Sub(parseTime(line.CreatedAt)) <= fileBurstWindow {
				burst.Count++
				burst.FirstAt = line.CreatedAt
				burst.Summary = fmt.Sprintf("Changed %d files", burst.Count)
				burst.Data = nil
				continue
			}
		} else {
			delete(openBurst, key)
		}
		if line.Category == "command" {
			var payload struct {
				CommandID string `json:"command_id"`
			}
			_ = json.Unmarshal([]byte(data), &payload)
			if payload.CommandID != "" {
				if line.EventType == "command.started" && finished[payload.CommandID] {
					continue
				}
				if line.EventType != "command.started" && line.EventType != "command.cancel_requested" {
					finished[payload.CommandID] = true
				}
			}
		}
		if len(out) >= limit {
			// Keep scanning only while the oldest line can still absorb files.
			if line.Category != "file" {
				break
			}
			continue
		}
		copyLine := line
		out = append(out, &copyLine)
		if line.Category == "file" {
			openBurst[key] = &copyLine
		}
	}
	return out, rows.Err()
}

// activityBucketsFor counts each workspace's activity in the last hour, in
// twelve 5-minute buckets, oldest first.
func activityBucketsFor(db *sql.DB, projectID string, now time.Time) (map[string][]int, error) {
	since := now.Add(-activityBuckets * activityBucketLen).UTC().Format(time.RFC3339)
	rows, err := db.Query(`SELECT workspace_id, created_at FROM workspace_activity WHERE project_id=? AND created_at>=?`, projectID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int{}
	for rows.Next() {
		var id, created string
		if err := rows.Scan(&id, &created); err != nil {
			return nil, err
		}
		age := now.Sub(parseTime(created))
		if age < 0 {
			age = 0
		}
		bucket := activityBuckets - 1 - int(age/activityBucketLen)
		if bucket < 0 || bucket >= activityBuckets {
			continue
		}
		if out[id] == nil {
			out[id] = make([]int, activityBuckets)
		}
		out[id][bucket]++
	}
	return out, rows.Err()
}

func listProjectPreviews(db *sql.DB, projectID string) (map[string]*Preview, error) {
	rows, err := db.Query(`SELECT p.workspace_id,p.execution_id,p.container_port,p.host_port,p.instance_id,p.tunnel_port,p.status,p.error,p.started_at,p.access_token
		FROM workspace_previews p JOIN workspaces w ON w.id=p.workspace_id WHERE w.project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Preview{}
	for rows.Next() {
		p := &Preview{}
		if err := rows.Scan(&p.WorkspaceID, &p.ExecutionID, &p.ContainerPort, &p.HostPort, &p.InstanceID, &p.TunnelPort, &p.Status, &p.Error, &p.StartedAt, &p.AccessToken); err != nil {
			return nil, err
		}
		p.setURL()
		out[p.WorkspaceID] = p
	}
	return out, rows.Err()
}
