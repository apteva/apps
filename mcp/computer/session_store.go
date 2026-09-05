package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	backends "github.com/apteva/apps/mcp/computer/internal/browser"
)

type ComputerSession struct {
	ID                  string                      `json:"session_id"`
	Backend             string                      `json:"backend"`
	BackendSessionID    string                      `json:"backend_session_id,omitempty"`
	AppContextID        string                      `json:"app_context_id,omitempty"`
	ContextName         string                      `json:"context_name,omitempty"`
	InitialURL          string                      `json:"initial_url,omitempty"`
	CurrentURL          string                      `json:"current_url,omitempty"`
	Width               int                         `json:"width,omitempty"`
	Height              int                         `json:"height,omitempty"`
	Status              string                      `json:"status"`
	CloseReason         string                      `json:"close_reason,omitempty"`
	RecordingStatus     string                      `json:"recording_status"`
	OpenedAt            string                      `json:"opened_at"`
	ClosedAt            *string                     `json:"closed_at,omitempty"`
	UpdatedAt           string                      `json:"updated_at"`
	FinalScreenshot     []byte                      `json:"-"`
	FinalScreenshotMIME string                      `json:"final_screenshot_mime,omitempty"`
	ProxyMode           string                      `json:"proxy_mode,omitempty"`
	ProxyProvider       string                      `json:"proxy_provider,omitempty"`
	ProxyProfileID      string                      `json:"proxy_profile_id,omitempty"`
	ProxyProfileName    string                      `json:"proxy_profile_name,omitempty"`
	ProxyCountry        string                      `json:"proxy_country,omitempty"`
	ProxyStickyScope    string                      `json:"proxy_sticky_scope,omitempty"`
	Environment         backends.EnvironmentOptions `json:"environment,omitempty"`
	ProxyBytes          *int64                      `json:"proxy_bytes,omitempty"`
	UsageStatus         string                      `json:"usage_status,omitempty"`
	UsageMeasuredAt     *string                     `json:"usage_measured_at,omitempty"`
}

type NavigationStep struct {
	URL       string `json:"url"`
	Title     string `json:"title,omitempty"`
	VisitedAt string `json:"visited_at"`
}

func dbPutSession(db *sql.DB, row *ComputerSession) error {
	if db == nil {
		return errors.New("computer session store is unavailable")
	}
	_, err := db.Exec(`
		INSERT INTO computer_sessions (
			id, backend, backend_session_id, app_context_id, context_name,
			initial_url, current_url, width, height, status, close_reason,
			recording_status, opened_at, closed_at, updated_at,
			final_screenshot, final_screenshot_mime, proxy_mode, proxy_provider,
			proxy_profile_id, proxy_profile_name, proxy_country, proxy_sticky_scope,
			environment_json, proxy_bytes, usage_status, usage_measured_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			backend=excluded.backend,
			backend_session_id=excluded.backend_session_id,
			app_context_id=excluded.app_context_id,
			context_name=excluded.context_name,
			initial_url=excluded.initial_url,
			current_url=excluded.current_url,
			width=excluded.width,
			height=excluded.height,
			status=excluded.status,
			close_reason=excluded.close_reason,
			recording_status=excluded.recording_status,
			opened_at=excluded.opened_at,
			closed_at=excluded.closed_at,
			updated_at=excluded.updated_at,
			proxy_mode=excluded.proxy_mode,
			proxy_provider=excluded.proxy_provider,
			proxy_profile_id=excluded.proxy_profile_id,
			proxy_profile_name=excluded.proxy_profile_name,
			proxy_country=excluded.proxy_country,
			proxy_sticky_scope=excluded.proxy_sticky_scope,
			environment_json=excluded.environment_json,
			proxy_bytes=CASE WHEN excluded.usage_status <> '' THEN excluded.proxy_bytes ELSE computer_sessions.proxy_bytes END,
			usage_status=CASE WHEN excluded.usage_status <> '' THEN excluded.usage_status ELSE computer_sessions.usage_status END,
			usage_measured_at=CASE WHEN excluded.usage_status <> '' THEN excluded.usage_measured_at ELSE computer_sessions.usage_measured_at END,
			final_screenshot=COALESCE(excluded.final_screenshot, computer_sessions.final_screenshot),
			final_screenshot_mime=CASE
				WHEN excluded.final_screenshot IS NOT NULL THEN excluded.final_screenshot_mime
				ELSE computer_sessions.final_screenshot_mime
			END`,
		row.ID, row.Backend, row.BackendSessionID, nullableText(row.AppContextID), nullableText(row.ContextName),
		nullableText(row.InitialURL), nullableText(row.CurrentURL), row.Width, row.Height, row.Status,
		nullableText(row.CloseReason), row.RecordingStatus, row.OpenedAt, row.ClosedAt, row.UpdatedAt,
		nullableBytes(row.FinalScreenshot), row.FinalScreenshotMIME, row.ProxyMode, row.ProxyProvider,
		row.ProxyProfileID, row.ProxyProfileName, row.ProxyCountry, row.ProxyStickyScope,
		encodeEnvironment(row.Environment), nullableInt64(row.ProxyBytes), row.UsageStatus, nullableStringPointer(row.UsageMeasuredAt),
	)
	if err != nil {
		return fmt.Errorf("persist computer session %s: %w", row.ID, err)
	}
	return nil
}

func dbGetSession(db *sql.DB, id string) (*ComputerSession, error) {
	if db == nil {
		return nil, errors.New("computer session store is unavailable")
	}
	return scanComputerSession(db.QueryRow(`
		SELECT id, backend, backend_session_id, app_context_id, context_name,
		       initial_url, current_url, width, height, status, close_reason,
		       recording_status, opened_at, closed_at, updated_at,
		       final_screenshot, final_screenshot_mime, proxy_mode, proxy_provider,
		       proxy_profile_id, proxy_profile_name, proxy_country, proxy_sticky_scope,
		       environment_json, proxy_bytes, usage_status, usage_measured_at
		FROM computer_sessions WHERE id=?`, id))
}

func dbGetSessionMetadata(db *sql.DB, id string) (*ComputerSession, error) {
	if db == nil {
		return nil, errors.New("computer session store is unavailable")
	}
	return scanComputerSession(db.QueryRow(`
		SELECT id, backend, backend_session_id, app_context_id, context_name,
		       initial_url, current_url, width, height, status, close_reason,
		       recording_status, opened_at, closed_at, updated_at,
		       NULL, final_screenshot_mime, proxy_mode, proxy_provider,
		       proxy_profile_id, proxy_profile_name, proxy_country, proxy_sticky_scope,
		       environment_json, proxy_bytes, usage_status, usage_measured_at
		FROM computer_sessions WHERE id=?`, id))
}

func dbListSessions(db *sql.DB, limit int) ([]*ComputerSession, error) {
	return dbListSessionsPage(db, limit, 0)
}

func dbListSessionsPage(db *sql.DB, limit, offset int) ([]*ComputerSession, error) {
	if offset < 0 {
		return nil, errors.New("history offset must not be negative")
	}
	if db == nil {
		return nil, errors.New("computer session store is unavailable")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`
		SELECT id, backend, backend_session_id, app_context_id, context_name,
		       initial_url, current_url, width, height, status, close_reason,
		       recording_status, opened_at, closed_at, updated_at,
		       NULL, final_screenshot_mime, proxy_mode, proxy_provider,
		       proxy_profile_id, proxy_profile_name, proxy_country, proxy_sticky_scope,
		       environment_json, proxy_bytes, usage_status, usage_measured_at
		FROM computer_sessions
		ORDER BY opened_at DESC, id DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]*ComputerSession, 0, limit)
	for rows.Next() {
		row, err := scanComputerSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type computerSessionScanner interface {
	Scan(dest ...any) error
}

func scanComputerSession(scanner computerSessionScanner) (*ComputerSession, error) {
	row := &ComputerSession{}
	var appContextID, contextName, initialURL, currentURL, closeReason, environmentJSON, usageMeasuredAt sql.NullString
	var proxyBytes sql.NullInt64
	var closedAt sql.NullString
	err := scanner.Scan(
		&row.ID, &row.Backend, &row.BackendSessionID, &appContextID, &contextName,
		&initialURL, &currentURL, &row.Width, &row.Height, &row.Status, &closeReason,
		&row.RecordingStatus, &row.OpenedAt, &closedAt, &row.UpdatedAt,
		&row.FinalScreenshot, &row.FinalScreenshotMIME,
		&row.ProxyMode, &row.ProxyProvider, &row.ProxyProfileID, &row.ProxyProfileName,
		&row.ProxyCountry, &row.ProxyStickyScope, &environmentJSON,
		&proxyBytes, &row.UsageStatus, &usageMeasuredAt,
	)
	if err != nil {
		return nil, err
	}
	row.AppContextID = appContextID.String
	row.ContextName = contextName.String
	row.InitialURL = initialURL.String
	row.CurrentURL = currentURL.String
	row.CloseReason = closeReason.String
	if closedAt.Valid {
		value := closedAt.String
		row.ClosedAt = &value
	}
	if environmentJSON.Valid && environmentJSON.String != "" {
		if err := json.Unmarshal([]byte(environmentJSON.String), &row.Environment); err != nil {
			return nil, fmt.Errorf("decode computer session environment: %w", err)
		}
	}
	if proxyBytes.Valid {
		value := proxyBytes.Int64
		row.ProxyBytes = &value
	}
	if usageMeasuredAt.Valid {
		value := usageMeasuredAt.String
		row.UsageMeasuredAt = &value
	}
	return row, nil
}

func encodeEnvironment(value backends.EnvironmentOptions) string {
	if value.IsEmpty() {
		return ""
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func dbAppendNavigation(db *sql.DB, sessionID, rawURL, title string, visitedAt time.Time) error {
	if db == nil {
		return errors.New("computer session store is unavailable")
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	_, err := db.Exec(`
		INSERT INTO computer_session_navigation (session_id, position, url, title, visited_at)
		SELECT ?, COALESCE((
		    SELECT MAX(position) FROM computer_session_navigation WHERE session_id=?
		  ), -1) + 1, ?, ?, ?
		WHERE COALESCE((
		    SELECT url FROM computer_session_navigation
		    WHERE session_id=?
		    ORDER BY position DESC LIMIT 1
		  ), '') <> ?`,
		sessionID, sessionID, rawURL, strings.TrimSpace(title), visitedAt.UTC().Format(time.RFC3339Nano),
		sessionID, rawURL,
	)
	if err != nil {
		return fmt.Errorf("persist computer navigation %s: %w", sessionID, err)
	}
	return nil
}

func dbListNavigation(db *sql.DB, sessionID string) ([]NavigationStep, error) {
	if db == nil {
		return nil, errors.New("computer session store is unavailable")
	}
	rows, err := db.Query(`
		SELECT url, title, visited_at
		FROM computer_session_navigation
		WHERE session_id=?
		ORDER BY position`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NavigationStep, 0)
	for rows.Next() {
		var step NavigationStep
		if err := rows.Scan(&step.URL, &step.Title, &step.VisitedAt); err != nil {
			return nil, err
		}
		out = append(out, step)
	}
	return out, rows.Err()
}

func dbUpdateRecordingStatus(db *sql.DB, id, status string, now time.Time) error {
	if db == nil {
		return errors.New("computer session store is unavailable")
	}
	_, err := db.Exec(`UPDATE computer_sessions SET recording_status=?, updated_at=? WHERE id=?`,
		status, now.UTC().Format(time.RFC3339Nano), id)
	return err
}

func dbInterruptActiveSessions(db *sql.DB, now time.Time) (int64, error) {
	if db == nil {
		return 0, errors.New("computer session store is unavailable")
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := db.Exec(`
		UPDATE computer_sessions
		SET status='interrupted', close_reason='app_restart', closed_at=?, updated_at=?,
		    recording_status=CASE
		      WHEN backend IN ('browserbase', 'steel') THEN 'processing'
		      ELSE 'unsupported'
		    END
		WHERE status='active'`, stamp, stamp)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableStringPointer(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	return *value
}

// Retention is opt-in; never delete active sessions or pending provider cleanup.
func dbPruneSessionHistory(db *sql.DB, before time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const expired = `SELECT id FROM computer_sessions WHERE status NOT IN ('active','cleanup_pending') AND closed_at < ? AND NOT EXISTS (SELECT 1 FROM computer_provider_leases l WHERE l.session_id=computer_sessions.id AND l.terminal_status<>'released')`
	for _, q := range []string{`DELETE FROM computer_session_navigation WHERE session_id IN (` + expired + `)`, `DELETE FROM computer_provider_leases WHERE session_id IN (` + expired + `)`, `DELETE FROM computer_sessions WHERE id IN (` + expired + `)`} {
		if _, err := tx.Exec(q, before.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
