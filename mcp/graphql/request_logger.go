package main

import "database/sql"

const (
	requestLogQueueSize = 2048
	requestLogBatchSize = 64
)

type requestLogEntry struct {
	projectID          string
	operationName      string
	operationType      string
	status             int
	durationMS         int64
	errorMessage       string
	createdAt          string
	operationHash      string
	apiRelease         int
	responseBytes      int
	rowCount           int
	resolverCount      int
	sourceTimings      string
	errorCodes         string
	authorizationScope string
	requestID          string
}

func (a *App) startRequestLogger() {
	a.logQueue = make(chan requestLogEntry, requestLogQueueSize)
	a.logStop = make(chan struct{})
	a.logDone = make(chan struct{})
	go a.runRequestLogger()
}

func (a *App) stopRequestLogger() {
	if a.logStop == nil || a.logDone == nil {
		return
	}
	a.logStopOnce.Do(func() { close(a.logStop) })
	<-a.logDone
}

func (a *App) enqueueRequestLog(entry requestLogEntry) {
	if a.logQueue == nil {
		return
	}
	select {
	case a.logQueue <- entry:
	default:
		// Request logging must never add backpressure to GraphQL execution.
		// The queue is deliberately large; reaching this branch means the
		// storage writer is unhealthy or traffic is far above its capacity.
		if a.ctx != nil {
			a.ctx.Logger().Warn("graphql request log queue full; dropping entry")
		}
	}
}

func (a *App) runRequestLogger() {
	defer close(a.logDone)
	batch := make([]requestLogEntry, 0, requestLogBatchSize)
	for {
		select {
		case entry := <-a.logQueue:
			batch = append(batch[:0], entry)
			batch = a.drainRequestLogs(batch)
			a.writeRequestLogs(batch)
		case <-a.logStop:
			for {
				select {
				case entry := <-a.logQueue:
					batch = append(batch[:0], entry)
					batch = a.drainRequestLogs(batch)
					a.writeRequestLogs(batch)
				default:
					return
				}
			}
		}
	}
}

func (a *App) drainRequestLogs(batch []requestLogEntry) []requestLogEntry {
	for len(batch) < requestLogBatchSize {
		select {
		case entry := <-a.logQueue:
			batch = append(batch, entry)
		default:
			return batch
		}
	}
	return batch
}

func (a *App) writeRequestLogs(batch []requestLogEntry) {
	if len(batch) == 0 || a.ctx == nil || a.ctx.AppDB() == nil {
		return
	}
	db := a.ctx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		a.logRequestWriterError(err)
		return
	}
	stmt, err := tx.Prepare(`INSERT INTO graphql_request_logs(project_id,operation_name,operation_type,status_code,duration_ms,error,created_at,operation_hash,api_release,response_bytes,row_count,resolver_count,source_timings_json,error_codes_json,authorization_scope,request_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		a.logRequestWriterError(err)
		return
	}
	for _, entry := range batch {
		if _, err = stmt.Exec(entry.projectID, entry.operationName, entry.operationType, entry.status, entry.durationMS, entry.errorMessage, entry.createdAt, entry.operationHash, entry.apiRelease, entry.responseBytes, entry.rowCount, entry.resolverCount, entry.sourceTimings, entry.errorCodes, entry.authorizationScope, entry.requestID); err != nil {
			break
		}
	}
	_ = stmt.Close()
	if err != nil {
		_ = tx.Rollback()
		a.logRequestWriterError(err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.logRequestWriterError(err)
	}
}

func (a *App) logRequestWriterError(err error) {
	if err == nil || err == sql.ErrTxDone || a.ctx == nil {
		return
	}
	a.ctx.Logger().Error("graphql request log write failed", "error", err)
}
