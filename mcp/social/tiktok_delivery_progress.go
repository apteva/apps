package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type deliveryOutcome struct {
	StartedAt       string `json:"started_at,omitempty"`
	At              string `json:"at,omitempty"`
	HTTPStatus      int    `json:"http_status,omitempty"`
	ExpectedStatus  int    `json:"expected_status,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	Result          string `json:"result"`
	ResponseExcerpt string `json:"response_excerpt,omitempty"`
	Chunk           int    `json:"chunk,omitempty"`
	ContentRange    string `json:"content_range,omitempty"`
	ResponseRange   string `json:"response_range,omitempty"`
	URLSHA256       string `json:"url_sha256,omitempty"`
	Evidence        string `json:"evidence,omitempty"`
}
type tikTokProgressObservation struct {
	At              string `json:"observed_at"`
	UploadedBytes   *int64 `json:"uploaded_bytes"`
	DownloadedBytes *int64 `json:"downloaded_bytes"`
	Status          string `json:"status"`
	HTTPStatus      int    `json:"http_status"`
	QuerySucceeded  bool   `json:"query_succeeded"`
	ErrorCode       string `json:"error_code,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
}

func bytePointer(n int64) *int64 { return &n }
func deliveryExcerpt(raw []byte) string {
	// Redact before truncating so a token/URL spanning the boundary cannot leak.
	s := strings.ToValidUTF8(redactDeliverySecrets(string(raw)), "�")
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
	runes := []rune(s)
	if len(runes) > 1024 {
		s = string(runes[:1024]) + "…"
	}
	return s
}
func uploadURLHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
func (d *deliveryDiagnostics) uploadOutcome(ctx *sdk.AppCtx, target int64, outcome deliveryOutcome) error {
	if outcome.At == "" {
		outcome.At = time.Now().UTC().Format(time.RFC3339Nano)
	}
	d.LastUpload = &outcome
	if outcome.Result != "acknowledged" && outcome.Result != "request_started" {
		if d.FirstUploadError == nil {
			first := outcome
			d.FirstUploadError = &first
		}
		latest := outcome
		d.LatestUploadError = &latest
	}
	d.updateTikTokStall(time.Now().UTC())
	return d.event(ctx, target, deliveryEvent{Stage: "upload", HTTPStatus: outcome.HTTPStatus, Bytes: d.TransferredBytes, RequestID: outcome.RequestID, Result: outcome.Result})
}

var legacyTikTokUploadError = regexp.MustCompile(`TikTok chunk upload returned HTTP ([0-9]{3})`)

func decodeDeliveryDiagnostics(raw, platform, lastError string) deliveryDiagnostics {
	var d deliveryDiagnostics
	_ = json.Unmarshal([]byte(raw), &d)
	if platform == "tiktok" {
		if d.ProgressVersion == 0 && d.Method == "FILE_UPLOAD" {
			if d.LegacyTransferredBytes == nil {
				d.LegacyTransferredBytes = bytePointer(d.TransferredBytes)
			}
			d.TransferredBytes = 0 // Old counters advanced before acknowledgement; never present them as confirmed.
			d.AcknowledgedBytes = nil
		}
		if d.VerifiedSourceBytes == nil && d.SourceSHA256 != "" {
			d.VerifiedSourceBytes = bytePointer(d.SourceBytes)
		}
		if d.LatestUploadError == nil {
			if match := legacyTikTokUploadError.FindStringSubmatch(lastError); len(match) == 2 {
				status, _ := strconv.Atoi(match[1])
				out := deliveryOutcome{HTTPStatus: status, Result: "legacy_upload_error", ResponseExcerpt: deliveryExcerpt([]byte(lastError)), Evidence: "legacy_target_error; original response and timestamp unavailable"}
				d.FirstUploadError = &out
				latest := out
				d.LatestUploadError = &latest
			}
		}
		d.reconcileTikTokAcknowledgement()
		d.updateTikTokStall(time.Now().UTC())
	}
	return d
}
func (d *deliveryDiagnostics) updateTikTokStall(now time.Time) {
	d.LocalDiagnostic = ""
	if d.LatestUploadError == nil || d.UploadAcknowledgementResolution != nil || d.ProviderStatus != "PROCESSING_UPLOAD" {
		return
	}
	baseline := d.LastProgressAt
	if baseline == "" || newerDeliveryTime(d.LatestUploadError.At, baseline) {
		baseline = d.LatestUploadError.At
	}
	if baseline == "" && len(d.Events) > 0 {
		baseline = d.Events[0].At
	}
	at, err := time.Parse(time.RFC3339Nano, baseline)
	if err == nil && now.Sub(at) >= 5*time.Minute {
		d.LocalDiagnostic = "stalled_upload"
	}
}

var contentRangeACK = regexp.MustCompile(`(?i)^bytes[ =]+(\d+)\s*-\s*(\d+)\s*/\s*(\d+)$`)
var rangeACK = regexp.MustCompile(`(?i)^bytes[ =]+(\d+)\s*-\s*(\d+)$`)

// TikTok has returned both inclusive byte endpoints and an exclusive end
// offset (including bytes 0-N/N on final HTTP 201). Normalize those two forms;
// under-acknowledgements, different totals and unexpected statuses stay errors.
func validateTikTokAcknowledgement(headers http.Header, first, last, total int64) error {
	if first < 0 || last < first || total <= last {
		return errors.New("invalid attempted upload range")
	}
	for _, field := range []string{"Content-Range", "Range"} {
		for _, value := range headers.Values(field) {
			raw := strings.TrimSpace(value)
			if raw == "" {
				continue
			}
			expression := rangeACK
			if field == "Content-Range" {
				expression = contentRangeACK
			}
			match := expression.FindStringSubmatch(raw)
			if match == nil {
				return fmt.Errorf("invalid %s acknowledgement", field)
			}
			start, e1 := strconv.ParseInt(match[1], 10, 64)
			end, e2 := strconv.ParseInt(match[2], 10, 64)
			if e1 != nil || e2 != nil || (start != 0 && start != first) || (end != last && end != last+1) || end < start {
				return fmt.Errorf("%s acknowledgement does not match the attempted chunk", field)
			}
			if len(match) == 4 {
				size, e := strconv.ParseInt(match[3], 10, 64)
				if e != nil || size != total {
					return errors.New("Content-Range acknowledgement has a different total size")
				}
			}
		}
	}
	return nil
}

func (d *deliveryDiagnostics) observeTikTokProgress(observation tikTokProgressObservation) {
	advanced := false
	if observation.QuerySucceeded {
		for i, value := range []*int64{observation.UploadedBytes, observation.DownloadedBytes} {
			if value == nil || *value < 0 {
				continue
			}
			water := &d.ProviderUploadedHighWater
			if i == 1 {
				water = &d.ProviderDownloadedHighWater
			}
			floor := int64(0)
			if *water != nil {
				floor = **water
			}
			if i == 0 && d.AcknowledgedBytes != nil && *d.AcknowledgedBytes > floor {
				floor = *d.AcknowledgedBytes
			}
			if *value > floor {
				advanced = true
			}
			if *water == nil || *value > **water {
				*water = bytePointer(*value)
			}
		}
	}
	if advanced {
		d.LastProgressAt = observation.At
		d.AutomaticPollAttempts = 0
		d.AutomaticPollingStopped = false
	}
	d.ProviderObservation = &observation
	d.updateTikTokStall(time.Now().UTC())
}
func newerDeliveryTime(a, b string) bool {
	aa, ea := time.Parse(time.RFC3339Nano, a)
	bb, eb := time.Parse(time.RFC3339Nano, b)
	return ea == nil && (eb != nil || aa.After(bb))
}

// A status poll may have started before an upload response. Merge durable
// evidence inside the save transaction so a stale observer cannot erase it.
func (d *deliveryDiagnostics) mergeDeliveryEvidence(old deliveryDiagnostics) {
	if old.FirstUploadError != nil && (d.FirstUploadError == nil || newerDeliveryTime(d.FirstUploadError.At, old.FirstUploadError.At)) {
		d.FirstUploadError = old.FirstUploadError
	}
	if old.LatestUploadError != nil && (d.LatestUploadError == nil || newerDeliveryTime(old.LatestUploadError.At, d.LatestUploadError.At)) {
		d.LatestUploadError = old.LatestUploadError
	}
	if old.LastUpload != nil && (d.LastUpload == nil || newerDeliveryTime(old.LastUpload.At, d.LastUpload.At)) {
		d.LastUpload = old.LastUpload
	}
	if old.Initialization != nil && (d.Initialization == nil || newerDeliveryTime(old.Initialization.At, d.Initialization.At)) {
		d.Initialization = old.Initialization
	}
	for _, pair := range [][2]**int64{{&d.AcknowledgedBytes, &old.AcknowledgedBytes}, {&d.AttemptedBytes, &old.AttemptedBytes}, {&d.ProviderUploadedHighWater, &old.ProviderUploadedHighWater}, {&d.ProviderDownloadedHighWater, &old.ProviderDownloadedHighWater}} {
		if *pair[1] != nil && (*pair[0] == nil || **pair[1] > **pair[0]) {
			*pair[0] = bytePointer(**pair[1])
		}
	}
	if d.AcknowledgedBytes != nil {
		d.TransferredBytes = *d.AcknowledgedBytes
	}
	if newerDeliveryTime(old.LastProgressAt, d.LastProgressAt) {
		d.LastProgressAt = old.LastProgressAt
	}
	if old.ProviderObservation != nil && (d.ProviderObservation == nil || newerDeliveryTime(old.ProviderObservation.At, d.ProviderObservation.At)) {
		d.ProviderObservation = old.ProviderObservation
		d.ProviderStatus = old.ProviderStatus
		d.OperationState = old.OperationState
		d.Reconciliation = old.Reconciliation
		d.RetryEligibility = old.RetryEligibility
	}
	if !newerDeliveryTime(d.LastProgressAt, old.LastProgressAt) {
		if old.AutomaticPollAttempts > d.AutomaticPollAttempts {
			d.AutomaticPollAttempts = old.AutomaticPollAttempts
		}
		d.AutomaticPollingStopped = d.AutomaticPollingStopped || old.AutomaticPollingStopped
	}
	d.reconcileTikTokAcknowledgement()
	d.updateTikTokStall(time.Now().UTC())
}

func tikTokBackgroundPollDelay(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 5 * time.Minute
	case attempt == 2:
		return 15 * time.Minute
	case attempt == 3:
		return 30 * time.Minute
	case attempt == 4:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}
func (d *deliveryDiagnostics) scheduleTikTokPoll(ctx *sdk.AppCtx, target int64) {
	if d.AutomaticPollAttempts >= 12 {
		d.AutomaticPollingStopped = true
		d.NextPollAt = ""
	} else {
		d.NextPollAt = time.Now().UTC().Add(tikTokBackgroundPollDelay(d.AutomaticPollAttempts)).Format(time.RFC3339Nano)
	}
	_ = d.save(ctx, target)
	if d.NextPollAt != "" {
		_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET identity_resolve_after=? WHERE id=? AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?) AND attempts=? AND COALESCE(publish_operation_id,'')=?`, d.NextPollAt, target, projectScope(ctx), d.boundAttempt, d.boundOperation)
	}
}

// The supplied URL is retained byte-for-byte; only its SHA-256 is persisted.
func uploadTikTokChunks(ctx *sdk.AppCtx, target int64, prepared *preparedVideo, d *deliveryDiagnostics, uploadURL string, chunkSize int64, totalChunks int) error {
	client := &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	for chunk := 0; chunk < totalChunks; chunk++ {
		first := int64(chunk) * chunkSize
		count := chunkSize
		if chunk == totalChunks-1 {
			count = prepared.Size - first
		}
		last := first + count - 1
		requestCtx, cancelRequest := context.WithCancel(context.Background())
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPut, uploadURL, io.NewSectionReader(prepared.File, first, count))
		if err != nil {
			cancelRequest()
			return err
		}
		request.ContentLength = count
		request.Header.Set("Content-Type", prepared.MIME)
		request.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, prepared.Size))
		expected := http.StatusPartialContent
		if chunk == totalChunks-1 {
			expected = http.StatusCreated
		}
		outcome := deliveryOutcome{StartedAt: time.Now().UTC().Format(time.RFC3339Nano), ExpectedStatus: expected, Chunk: chunk + 1, ContentRange: request.Header.Get("Content-Range"), URLSHA256: uploadURLHash(request.URL.String()), Result: "request_started"}
		if outcome.URLSHA256 != d.UploadURLSHA256 {
			cancelRequest()
			outcome.Result = "url_binding_mismatch"
			_ = d.uploadOutcome(ctx, target, outcome)
			return errors.New("upload URL changed while preparing the request")
		}
		*d.AttemptedBytes += count
		if err := d.uploadOutcome(ctx, target, outcome); err != nil {
			cancelRequest()
			return errors.New("could not persist upload attempt")
		}
		response, err := client.Do(request)
		outcome.At = time.Now().UTC().Format(time.RFC3339Nano)
		if err != nil {
			cancelRequest()
			outcome.Result = "transport_error"
			outcome.ResponseExcerpt = deliveryExcerpt([]byte(err.Error()))
			_ = d.uploadOutcome(ctx, target, outcome)
			return errors.New("TikTok upload interrupted")
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		response.Body.Close()
		cancelRequest()
		outcome.HTTPStatus = response.StatusCode
		outcome.RequestID = responseRequestID(response.Header)
		outcome.ResponseExcerpt = deliveryExcerpt(raw)
		outcome.ResponseRange = deliveryExcerpt([]byte(strings.TrimSpace(response.Header.Get("Content-Range") + " " + response.Header.Get("Range"))))
		outcome.Result = "http_rejected"
		if response.StatusCode == expected {
			if err := validateTikTokAcknowledgement(response.Header, first, last, prepared.Size); err != nil {
				outcome.Result = "invalid_acknowledgement"
				outcome.ResponseExcerpt = deliveryExcerpt([]byte(err.Error() + ": " + outcome.ResponseExcerpt))
			} else {
				outcome.Result = "acknowledged"
				d.TransferredBytes = last + 1
				d.AcknowledgedBytes = bytePointer(last + 1)
				d.LastProgressAt = outcome.At
				if readErr != nil {
					outcome.ResponseExcerpt = "Upload acknowledged; response body could not be fully read."
				}
			}
		}
		if err := d.uploadOutcome(ctx, target, outcome); err != nil {
			return errors.New("upload response received but diagnostics could not be persisted")
		}
		if outcome.Result != "acknowledged" {
			return fmt.Errorf("TikTok chunk upload returned HTTP %d (%s)", response.StatusCode, outcome.Result)
		}
	}
	return nil
}

func (a *App) handleTargetDeliveryAction(w http.ResponseWriter, r *http.Request, post int64, targetText, action string) {
	target, err := strconv.ParseInt(targetText, 10, 64)
	if err != nil || target <= 0 {
		http.Error(w, "invalid target", 400)
		return
	}
	args := scopedQueryArgs(r, "social_account_id", "history_after_id", "history_limit")
	var handler func(*sdk.AppCtx, map[string]any) (any, error)
	switch action {
	case "inspect":
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", 405)
			return
		}
		handler = a.toolPostDeliveryInspect
	case "native-refresh":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		handler = a.toolPostTargetNativeRefresh
	case "revalidate", "recover":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		var body struct {
			RecoveryID      string `json:"recovery_id"`
			Confirm         bool   `json:"confirm"`
			ReviewExpired   bool   `json:"review_expired_upload"`
			SocialAccountID int64  `json:"social_account_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			http.Error(w, "invalid JSON", 400)
			return
		}
		args["recovery_id"] = body.RecoveryID
		args["confirm"] = body.Confirm
		args["review_expired_upload"] = body.ReviewExpired
		if body.SocialAccountID != 0 {
			args["social_account_id"] = body.SocialAccountID
		}
		if action == "revalidate" {
			handler = a.toolPostDeliveryRevalidate
		} else {
			handler = a.toolPostDeliveryRecover
		}
	default:
		http.Error(w, "not found", 404)
		return
	}
	args["post_id"] = post
	args["target_id"] = target
	out, err := handler(globalCtx, args)
	if err != nil {
		http.Error(w, redactDeliverySecrets(err.Error()), 500)
		return
	}
	writeJSON(w, out)
}
