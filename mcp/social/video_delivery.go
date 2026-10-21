package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxPreparedVideoBytes int64 = 32 << 30
const maxVideoSourceAttempts = 3

var videoSpoolSlots = make(chan struct{}, 2)

type deliveryEvent struct {
	Stage      string `json:"stage"`
	Route      string `json:"route,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	At         string `json:"at"`
	Bytes      int64  `json:"bytes,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Result     string `json:"result,omitempty"`
}
type deliveryDiagnostics struct {
	CorrelationID     string          `json:"correlation_id"`
	StorageInstallID  int64           `json:"storage_install_id"`
	MediaProjectID    string          `json:"media_project_id"`
	SourceFileID      int64           `json:"source_file_id"`
	SourceSHA256      string          `json:"source_sha256,omitempty"`
	ExpectedBytes     int64           `json:"expected_bytes,omitempty"`
	SourceHTTPStatus  int             `json:"source_http_status,omitempty"`
	SourceRoute       string          `json:"source_route,omitempty"`
	SourceBytes       int64           `json:"source_bytes,omitempty"`
	SourceContentType string          `json:"source_content_type,omitempty"`
	Method            string          `json:"method,omitempty"`
	Stage             string          `json:"stage"`
	OperationState    string          `json:"operation_state"`
	OperationID       string          `json:"operation_id,omitempty"`
	ProviderStatus    string          `json:"provider_status,omitempty"`
	FailReason        string          `json:"fail_reason,omitempty"`
	ErrorCode         string          `json:"error_code,omitempty"`
	ProviderRequestID string          `json:"provider_request_id,omitempty"`
	TransferredBytes  int64           `json:"transferred_bytes"`
	Reconciliation    string          `json:"reconciliation,omitempty"`
	RetryEligibility  string          `json:"retry_eligibility,omitempty"`
	Events            []deliveryEvent `json:"events"`
}

func randomDeliveryID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

var deliveryURLRE = regexp.MustCompile(`https?://[^\s"<>]+`)
var deliverySecretRE = regexp.MustCompile(`(?i)((?:upload_id|upload_token|access_token|refresh_token|sig|signature|authorization|api_key|session_url)["']?\s*[=:]\s*["']?)[^\s,&"}]+`)

var deliveryBearerRE = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]+=*`)

func redactDeliverySecrets(s string) string {
	s = deliveryBearerRE.ReplaceAllString(s, "Bearer [redacted]")
	s = deliveryURLRE.ReplaceAllStringFunc(s, func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[redacted URL]"
		}
		u.RawQuery = ""
		u.Fragment = ""
		u.User = nil
		if strings.Contains(u.Path, "/api/relay/") {
			u.Path = "/api/relay/[redacted]"
		}
		if strings.Contains(u.Path, "/upload/") {
			return u.Scheme + "://" + u.Host + "/[redacted upload session]"
		}
		return u.String()
	})
	return deliverySecretRE.ReplaceAllString(s, "${1}[redacted]")
}
func safeRequestID(s string) string {
	s = redactDeliverySecrets(s)
	if len(s) > 160 {
		s = s[:160]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, s)
}
func responseRequestID(h http.Header) string {
	for _, k := range []string{"X-Request-Id", "X-Correlation-Id", "X-Amz-Request-Id", "Cf-Ray"} {
		if v := h.Get(k); v != "" {
			return safeRequestID(v)
		}
	}
	return ""
}
func loadDeliveryDiagnostics(ctx *sdk.AppCtx, target int64) deliveryDiagnostics {
	var raw string
	_ = ctx.AppDB().QueryRow(`SELECT t.delivery_diagnostics FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE t.id=? AND p.project_id=?`, target, projectScope(ctx)).Scan(&raw)
	var d deliveryDiagnostics
	_ = json.Unmarshal([]byte(raw), &d)
	return d
}
func (d *deliveryDiagnostics) save(ctx *sdk.AppCtx, target int64) error {
	if target <= 0 {
		return nil
	}
	if len(d.Events) > 32 {
		d.Events = d.Events[len(d.Events)-32:]
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	res, err := ctx.AppDB().Exec(`UPDATE post_targets SET delivery_diagnostics=? WHERE id=? AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`, string(raw), target, projectScope(ctx))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("delivery target not found in this project")
	}
	return nil
}
func (d *deliveryDiagnostics) event(ctx *sdk.AppCtx, target int64, e deliveryEvent) error {
	e.At = time.Now().UTC().Format(time.RFC3339Nano)
	d.Stage = e.Stage
	d.Events = append(d.Events, e)
	saveErr := d.save(ctx, target)
	ctx.Logger().Info("media delivery", "correlation_id", d.CorrelationID, "target_id", target, "storage_install_id", d.StorageInstallID, "media_project_id", d.MediaProjectID, "file_id", d.SourceFileID, "stage", e.Stage, "route", e.Route, "status", e.HTTPStatus, "attempt", e.Attempt, "bytes", e.Bytes, "request_id", e.RequestID, "result", e.Result)
	return saveErr
}

type sourceDeliveryError struct {
	Reason    string
	Transient bool
}

func (e *sourceDeliveryError) Error() string { return "source delivery: " + e.Reason }

type preparedVideo struct {
	File        *os.File
	URL         string
	Size        int64
	SHA256      string
	MIME        string
	Diagnostics deliveryDiagnostics
	release     func()
}

func (p *preparedVideo) Close() {
	if p == nil {
		return
	}
	if p.File != nil {
		name := p.File.Name()
		_ = p.File.Close()
		_ = os.Remove(name)
		p.File = nil
	}
	if p.release != nil {
		p.release()
		p.release = nil
	}
}
func freshVideoURL(ctx *sdk.AppCtx, item mediaItem, project, route string) (string, error) {
	if item.ID <= 0 {
		return "", errors.New("video source needs a Storage file identity")
	}
	args := map[string]any{"id": item.ID, "_project_id": project, "ttl_seconds": 7200, "delivery": route}
	var signed struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", args, &signed); err != nil {
		return "", errors.New("Storage could not mint a delivery URL")
	}
	if signed.URL == "" {
		return "", errors.New("Storage returned no delivery URL")
	}
	full := absoluteStorageURL(signed.URL)
	u, err := url.Parse(full)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("Storage returned invalid delivery URL")
	}
	if install := ctx.IntegrationFor("storage"); install != nil && install.InstallID > 0 {
		if got := u.Query().Get("install_id"); got != "" && got != strconv.FormatInt(install.InstallID, 10) {
			return "", errors.New("Storage URL installation does not match binding")
		}
	}
	if got := u.Query().Get("project_id"); got != "" && got != project {
		return "", errors.New("Storage URL project does not match source")
	}
	if exp, _ := strconv.ParseInt(u.Query().Get("exp"), 10, 64); exp > 0 && exp <= time.Now().Unix() {
		return "", &sourceDeliveryError{"Storage returned an expired delivery URL", true}
	}
	return full, nil
}
func sourceStatusRecoverable(status int) bool {
	return status == 401 || status == 403 || status == 410 || status == 429 || status >= 500 || status >= 300 && status < 400
}
func videoContainerMIME(head []byte, total int64) string {
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		box := int64(binary.BigEndian.Uint32(head[:4]))
		if box < 16 || box > total || box%4 != 0 {
			return ""
		}
		if string(head[8:12]) == "qt  " {
			return "video/quicktime"
		}
		return "video/mp4"
	}
	detected := http.DetectContentType(head)
	if strings.HasPrefix(detected, "video/") {
		return detected
	}
	return ""
}
func canonicalVideoMIME(value string) string {
	switch value {
	case "video/x-msvideo":
		return "video/avi"
	case "video/x-m4v":
		return "video/mp4"
	case "video/mov":
		return "video/quicktime"
	}
	return value
}

func (a *App) prepareVideoSource(ctx *sdk.AppCtx, j publishJob, item mediaItem, method string) (*preparedVideo, error) {
	project := j.mediaProjectID
	if project == "" {
		project = projectScope(ctx)
	}
	previous := loadDeliveryDiagnostics(ctx, j.targetID)
	d := deliveryDiagnostics{CorrelationID: randomDeliveryID(), SourceFileID: item.ID, MediaProjectID: project, ExpectedBytes: item.Bytes, Method: method, Stage: "source_fetch", OperationState: "not_started", Events: []deliveryEvent{}}
	if b := ctx.IntegrationFor("storage"); b != nil {
		d.StorageInstallID = b.InstallID
	}
	if method == "RECOVERY_VALIDATION" {
		d.Method = previous.Method
	}
	// Existing operations are never erased by a new source failure.
	if previous.OperationID != "" {
		d.OperationID = previous.OperationID
		d.OperationState = previous.OperationState
		d.ProviderStatus, d.FailReason, d.ErrorCode, d.ProviderRequestID = previous.ProviderStatus, previous.FailReason, previous.ErrorCode, previous.ProviderRequestID
		d.RetryEligibility = previous.RetryEligibility
	}
	if item.MediaProjectID != "" && item.MediaProjectID != project {
		return nil, &sourceDeliveryError{"source project differs from stored media project", false}
	}
	if item.StorageInstallID > 0 && d.StorageInstallID != item.StorageInstallID {
		return nil, &sourceDeliveryError{"Storage binding changed during preparation", false}
	}
	if item.Bytes < 0 || item.Bytes > maxPreparedVideoBytes {
		return nil, &sourceDeliveryError{"video exceeds temporary source limit", false}
	}
	select {
	case videoSpoolSlots <- struct{}{}:
	case <-time.After(30 * time.Second):
		return nil, &sourceDeliveryError{"temporary video capacity is busy", true}
	}
	p := &preparedVideo{Diagnostics: d, release: func() { <-videoSpoolSlots }}
	success := false
	defer func() {
		if !success {
			p.Close()
		}
	}()
	var last error
	for attempt := 1; attempt <= maxVideoSourceAttempts; attempt++ {
		if attempt > 1 {
			time.Sleep(time.Duration(attempt-1) * 100 * time.Millisecond)
		}
		route := "apteva"
		if attempt > 1 {
			route = "proxy"
		}
		raw, err := freshVideoURL(ctx, item, project, route)
		if err != nil {
			last = err
			d.event(ctx, j.targetID, deliveryEvent{Stage: "source_fetch", Route: route, Attempt: attempt, Result: "url_unavailable"})
			continue
		}
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			return nil, &sourceDeliveryError{"invalid source URL", false}
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Apteva-VideoDelivery/1.0)")
		req.Header.Set("X-Correlation-ID", d.CorrelationID)
		client := *mediaHTTPClient
		if route != "apteva" {
			client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		}
		response, err := client.Do(req)
		if err != nil {
			last = &sourceDeliveryError{"source request failed before provider submission", true}
			d.event(ctx, j.targetID, deliveryEvent{Stage: "source_fetch", Route: route, Attempt: attempt, Result: "transport_failure"})
			continue
		}
		d.SourceHTTPStatus = response.StatusCode
		d.SourceRoute = route
		event := deliveryEvent{Stage: "source_fetch", Route: route, HTTPStatus: response.StatusCode, Attempt: attempt, RequestID: responseRequestID(response.Header)}
		if response.StatusCode != 200 {
			response.Body.Close()
			event.Result = "http_rejected"
			d.event(ctx, j.targetID, event)
			last = &sourceDeliveryError{fmt.Sprintf("Storage returned %d", response.StatusCode), sourceStatusRecoverable(response.StatusCode)}
			if !sourceStatusRecoverable(response.StatusCode) {
				break
			}
			continue
		}
		mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		d.SourceContentType = mediaType
		if !strings.HasPrefix(mediaType, "video/") {
			response.Body.Close()
			event.Result = "invalid_content_type"
			d.event(ctx, j.targetID, event)
			last = &sourceDeliveryError{"source response is not a video", false}
			break
		}
		if response.ContentLength > maxPreparedVideoBytes || item.Bytes > 0 && response.ContentLength >= 0 && response.ContentLength != item.Bytes {
			response.Body.Close()
			event.Result = "length_mismatch"
			d.event(ctx, j.targetID, event)
			last = &sourceDeliveryError{"source response size differs from Storage metadata", true}
			continue
		}
		file, err := os.CreateTemp("", "social-video-*.spool")
		if err != nil {
			response.Body.Close()
			return nil, &sourceDeliveryError{"temporary source file could not be created", true}
		}
		digest := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, maxPreparedVideoBytes+1))
		response.Body.Close()
		event.Bytes = n
		d.SourceBytes = n
		cleanup := func() { file.Close(); os.Remove(file.Name()) }
		if copyErr != nil || n == 0 || n > maxPreparedVideoBytes || item.Bytes > 0 && n != item.Bytes || response.ContentLength >= 0 && n != response.ContentLength {
			cleanup()
			event.Result = "incomplete_source"
			d.event(ctx, j.targetID, event)
			last = &sourceDeliveryError{"source download is incomplete or exceeds limit", true}
			continue
		}
		hash := hex.EncodeToString(digest.Sum(nil))
		if item.SHA256 != "" && !strings.EqualFold(hash, item.SHA256) {
			cleanup()
			event.Result = "checksum_mismatch"
			d.event(ctx, j.targetID, event)
			last = &sourceDeliveryError{"source checksum differs from Storage metadata", false}
			break
		}
		head := make([]byte, 512)
		count, _ := file.ReadAt(head, 0)
		actual := videoContainerMIME(head[:count], n)
		if actual == "" || canonicalVideoMIME(actual) != canonicalVideoMIME(mediaType) {
			cleanup()
			event.Result = "invalid_container"
			d.event(ctx, j.targetID, event)
			last = &sourceDeliveryError{"source bytes do not match declared video media type", false}
			break
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			cleanup()
			return nil, err
		}
		d.SourceSHA256 = hash
		if d.OperationState == "not_started" {
			d.RetryEligibility = "source_verified"
		}
		event.Result = "complete_source_verified"
		d.event(ctx, j.targetID, event)
		p.File = file
		p.URL = raw
		p.Size = n
		p.MIME = mediaType
		p.SHA256 = hash
		p.Diagnostics = d
		success = true
		return p, nil
	}
	d.RetryEligibility = "source_not_ready"
	_ = d.save(ctx, j.targetID)
	if last == nil {
		last = &sourceDeliveryError{"source could not be prepared", true}
	}
	return nil, last
}

func executeStatus(res *sdk.ExecuteResult) int {
	if res == nil {
		return 0
	}
	return res.Status
}

// Capture provider-native state without retaining provider bodies or session URLs.
func (a *App) recordTikTokStatus(ctx *sdk.AppCtx, conn int64, operation, status, reason, code, logID string, res *sdk.ExecuteResult, callErr error) {
	rows, err := ctx.AppDB().Query(`SELECT t.id FROM post_targets t JOIN posts p ON p.id=t.post_id JOIN social_accounts a ON a.id=t.social_account_id WHERE p.project_id=? AND a.connection_id=? AND a.platform='tiktok' AND t.publish_operation_id=?`, projectScope(ctx), conn, operation)
	if err != nil {
		return
	}
	var targets []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			targets = append(targets, id)
		}
	}
	rows.Close()
	for _, id := range targets {
		d := loadDeliveryDiagnostics(ctx, id)
		d.OperationID = safeRequestID(operation)
		d.ProviderStatus = safeRequestID(status)
		d.FailReason = safeRequestID(reason)
		d.ErrorCode = safeRequestID(code)
		d.ProviderRequestID = safeRequestID(logID)
		d.Reconciliation = "unknown"
		d.OperationState = "unknown"
		d.RetryEligibility = "blocked_unknown_outcome"
		stage := "polling"
		if callErr == nil && res != nil && res.Success && (code == "" || code == "ok") {
			switch status {
			case "FAILED":
				d.OperationState = "confirmed_failure"
				d.RetryEligibility = "content_or_configuration_review"
				switch reason {
				case "internal", "video_pull_failed", "picture_size_check_failed":
					d.RetryEligibility = "requires_revalidation"
				}
				d.Reconciliation = "not_published"
				stage = "processing"
			case "PUBLISH_COMPLETE":
				d.OperationState = "published"
				d.RetryEligibility = "never_resubmit"
				d.Reconciliation = "published"
				stage = "processing"
			case "PROCESSING_UPLOAD", "PROCESSING_DOWNLOAD", "SEND_TO_USER_INBOX":
				d.OperationState = "pending"
				d.RetryEligibility = "reconcile_only"
				d.Reconciliation = "pending"
			}
		}
		d.event(ctx, id, deliveryEvent{Stage: stage, HTTPStatus: executeStatus(res), RequestID: safeRequestID(logID), Result: d.Reconciliation})
	}
}

func captureProviderDiagnostics(d *deliveryDiagnostics, res *sdk.ExecuteResult) {
	if res == nil {
		return
	}
	var body struct {
		Error struct {
			Code    any    `json:"code"`
			LogID   string `json:"log_id"`
			TraceID string `json:"fbtrace_id"`
		} `json:"error"`
	}
	if json.Unmarshal(res.Data, &body) == nil {
		if body.Error.Code != nil {
			d.ErrorCode = safeRequestID(fmt.Sprint(body.Error.Code))
		}
		d.ProviderRequestID = safeRequestID(body.Error.LogID)
		if d.ProviderRequestID == "" {
			d.ProviderRequestID = safeRequestID(body.Error.TraceID)
		}
	}
	if d.ProviderRequestID == "" {
		for _, key := range []string{"X-Request-ID", "X-Correlation-ID", "X-Log-ID"} {
			if id := integrationHeader(res, key); id != "" {
				d.ProviderRequestID = safeRequestID(id)
				break
			}
		}
	}
}

// Recheck mutable account/approval state after the potentially long download,
// immediately before creating a provider operation.
func validatePreparedDestination(ctx *sdk.AppCtx, j publishJob) error {
	if j.targetID <= 0 {
		return nil
	}
	var eligible int
	err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets t JOIN posts p ON p.id=t.post_id JOIN social_accounts a ON a.id=t.social_account_id WHERE t.id=? AND p.project_id=? AND a.project_id=p.project_id AND a.connection_id=? AND a.status='active' AND t.status='publishing' AND t.delivery_revision=p.revision AND (p.approval_required=0 OR p.approved_revision=p.revision)`, j.targetID, projectScope(ctx), j.connID).Scan(&eligible)
	if err != nil {
		return err
	}
	if eligible != 1 {
		return errors.New("video destination or approved revision is not active")
	}
	return nil
}
