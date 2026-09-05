package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type logEntry struct {
	row     *RequestLog
	barrier chan struct{}
}
type logSink struct {
	mu      sync.RWMutex
	closed  bool
	queue   chan logEntry
	done    chan struct{}
	dropped atomic.Uint64
	failed  atomic.Uint64
	db      *sql.DB
	report  func(error)
}

var logSinks sync.Map

func newRequestID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

var credentialPattern = regexp.MustCompile(`(?i)(api_key|authorization|token|access_token|refresh_token)=([^&\s"']+)`)

func redactErrorText(s string) string {
	s = credentialPattern.ReplaceAllString(s, "$1=[redacted]")
	if len(s) > 2048 {
		s = s[:2048]
	}
	return s
}
func safeUpstreamError(err error) string {
	if err == nil {
		return ""
	}
	var u *url.Error
	if errors.As(err, &u) {
		return u.Op + ": " + redactErrorText(u.Err.Error())
	}
	return redactErrorText(err.Error())
}
func startLogSink(db *sql.DB, report func(error)) *logSink {
	s := &logSink{queue: make(chan logEntry, 512), done: make(chan struct{}), db: db, report: report}
	logSinks.Store(db, s)
	go func() {
		defer close(s.done)
		for item := range s.queue {
			if item.barrier != nil {
				close(item.barrier)
				continue
			}
			batch := []*RequestLog{item.row}
			var barrier chan struct{}
			// Briefly collect a batch even under sequential traffic. A reader's
			// barrier and shutdown bypass this delay and flush immediately.
			collect := time.NewTimer(2 * time.Millisecond)
		drain:
			for len(batch) < 64 {
				select {
				case next, ok := <-s.queue:
					if !ok {
						break drain
					}
					if next.barrier != nil {
						barrier = next.barrier
						break drain
					}
					batch = append(batch, next.row)
				case <-collect.C:
					break drain
				}
			}
			collect.Stop()
			if err := s.write(batch); err != nil {
				s.failed.Add(uint64(len(batch)))
				report(err)
			}
			if barrier != nil {
				close(barrier)
			}
		}
	}()
	return s
}
func (s *logSink) write(rows []*RequestLog) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		// A completed request must not resurrect logs for a deleted API.
		_, err = tx.Exec(`INSERT INTO api_request_logs(project_id,api_id,route_id,hostname,method,path,status_code,target_kind,target_ref,auth_kind,subject,duration_ms,error,request_id)
   SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM apis WHERE project_id=? AND id=?)`, r.ProjectID, nullableID(r.APIID), nullableID(r.RouteID), r.Hostname, r.Method, r.Path, r.StatusCode, r.TargetKind, r.TargetRef, r.AuthKind, r.Subject, r.DurationMS, r.Error, r.RequestID, r.ProjectID, r.APIID)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM api_request_logs WHERE id IN(SELECT id FROM api_request_logs WHERE id<=(SELECT MAX(id)-100000 FROM api_request_logs) ORDER BY id LIMIT 256)`); err != nil {
		return err
	}
	return tx.Commit()
}
func enqueueRequestLog(db *sql.DB, row RequestLog) {
	row.Error = redactErrorText(row.Error)
	row.TargetRef = redactErrorText(row.TargetRef)
	if target, err := url.Parse(row.TargetRef); err == nil && target.Host != "" {
		target.User, target.RawQuery, target.Fragment = nil, "", ""
		row.TargetRef = target.String()
	}
	if value, ok := logSinks.Load(db); ok {
		s := value.(*logSink)
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.closed {
			return
		}
		select {
		case s.queue <- logEntry{row: &row}:
		default:
			s.dropped.Add(1)
		}
		return
	}
	dbInsertLog(db, row)
}
func flushRequestLogs(db *sql.DB) {
	if value, ok := logSinks.Load(db); ok {
		s := value.(*logSink)
		s.mu.RLock()
		defer s.mu.RUnlock()
		if s.closed {
			return
		}
		barrier := make(chan struct{})
		select {
		case s.queue <- logEntry{barrier: barrier}:
			select {
			case <-barrier:
			case <-s.done:
			}
		case <-s.done:
		}
	}
}
func stopLogSink(db *sql.DB) {
	if value, ok := logSinks.LoadAndDelete(db); ok {
		s := value.(*logSink)
		s.mu.Lock()
		s.closed = true
		close(s.queue)
		s.mu.Unlock()
		<-s.done
	}
}
func pruneRequestLogs(db *sql.DB) error {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	// Both deletion paths are bounded. The 100k cap preserves recent logs.
	_, err := db.Exec(`DELETE FROM api_request_logs WHERE id IN(SELECT id FROM api_request_logs WHERE created_at<? ORDER BY created_at LIMIT 2000)`, cutoff)
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM api_request_logs WHERE id IN(SELECT id FROM api_request_logs WHERE id<(SELECT id FROM api_request_logs ORDER BY id DESC LIMIT 1 OFFSET 99999) ORDER BY id LIMIT 2000)`)
	return err
}
func sanitizeLogPath(path string) string {
	if i := strings.IndexByte(path, '?'); i >= 0 {
		return path[:i]
	}
	return path
}
