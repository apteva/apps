package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ComputerSession struct {
	ID                  string  `json:"session_id"`
	Backend             string  `json:"backend"`
	BackendSessionID    string  `json:"backend_session_id,omitempty"`
	AppContextID        string  `json:"app_context_id,omitempty"`
	ContextName         string  `json:"context_name,omitempty"`
	InitialURL          string  `json:"initial_url,omitempty"`
	CurrentURL          string  `json:"current_url,omitempty"`
	Width               int     `json:"width,omitempty"`
	Height              int     `json:"height,omitempty"`
	Status              string  `json:"status"`
	CloseReason         string  `json:"close_reason,omitempty"`
	RecordingStatus     string  `json:"recording_status"`
	OpenedAt            string  `json:"opened_at"`
	ClosedAt            *string `json:"closed_at,omitempty"`
	UpdatedAt           string  `json:"updated_at"`
	FinalScreenshot     []byte  `json:"-"`
	FinalScreenshotMIME string  `json:"final_screenshot_mime,omitempty"`
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
			final_screenshot, final_screenshot_mime
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			final_screenshot=COALESCE(excluded.final_screenshot, computer_sessions.final_screenshot),
			final_screenshot_mime=CASE
				WHEN excluded.final_screenshot IS NOT NULL THEN excluded.final_screenshot_mime
				ELSE computer_sessions.final_screenshot_mime
			END`,
		row.ID, row.Backend, row.BackendSessionID, nullableText(row.AppContextID), nullableText(row.ContextName),
		nullableText(row.InitialURL), nullableText(row.CurrentURL), row.Width, row.Height, row.Status,
		nullableText(row.CloseReason), row.RecordingStatus, row.OpenedAt, row.ClosedAt, row.UpdatedAt,
		nullableBytes(row.FinalScreenshot), row.FinalScreenshotMIME,
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
		       final_screenshot, final_screenshot_mime
		FROM computer_sessions WHERE id=?`, id))
}

func dbListSessions(db *sql.DB, limit int) ([]*ComputerSession, error) {
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
		       final_screenshot, final_screenshot_mime
		FROM computer_sessions
		ORDER BY opened_at DESC
		LIMIT ?`, limit)
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
	var appContextID, contextName, initialURL, currentURL, closeReason sql.NullString
	var closedAt sql.NullString
	err := scanner.Scan(
		&row.ID, &row.Backend, &row.BackendSessionID, &appContextID, &contextName,
		&initialURL, &currentURL, &row.Width, &row.Height, &row.Status, &closeReason,
		&row.RecordingStatus, &row.OpenedAt, &closedAt, &row.UpdatedAt,
		&row.FinalScreenshot, &row.FinalScreenshotMIME,
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
	return row, nil
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
