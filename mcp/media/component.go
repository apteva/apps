package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"strings"
	"time"
)

type componentCallOwnerKey struct{}
type componentFolderKey struct{}
type componentPlan struct {
	JobID   string `json:"job_id"`
	Command string `json:"command"`
	Timeout int    `json:"timeout_s"`
	Weight  int    `json:"weight"`
	Control bool   `json:"control"`
}
type componentReceipt struct {
	JobID   string `json:"job_id"`
	Status  string `json:"status"`
	Output  string `json:"output"`
	Exit    int    `json:"exit_code"`
	Error   string `json:"error"`
	Stopped bool   `json:"stopped_confirmed"`
}

func randomComponentID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func componentExecutionID(app *sdk.AppCtx) int64 {
	if app.Config().Get("execution_mode") == "component" {
		return 1
	}
	// Previously configured offloading must never silently consume local CPU.
	if old := app.Config().Get("render_host_id"); old != "" && old != "0" {
		return 1
	}
	return 0
}
func componentCall(ctx context.Context, app *sdk.AppCtx, tool string, input map[string]any, out any) error {
	return app.Components().Call(ctx, "executor", tool, input, out)
}
func runComponentPlan(ctx context.Context, app *sdk.AppCtx, command string, timeout int) (string, int, error) {
	if ctx.Err() != nil {
		return "", -1, ctx.Err()
	}
	if timeout < 1 {
		timeout = 30
	}
	if timeout > 14400 {
		timeout = 14400
	}
	ctx, stopCall := context.WithTimeout(ctx, time.Duration(timeout+15)*time.Second)
	defer stopCall()
	owner, _ := ctx.Value(componentCallOwnerKey{}).(string)
	var plan componentPlan
	var saved sql.NullString
	if owner != "" {
		var raw string
		err := app.AppDB().QueryRow(`SELECT request_json,receipt_json FROM media_executor_calls WHERE project_id=? AND owner_key=?`, app.CurrentProject(), owner).Scan(&raw, &saved)
		if err == nil {
			if json.Unmarshal([]byte(raw), &plan) != nil {
				return "", -1, errors.New("invalid persisted executor request")
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return "", -1, err
		}
	}
	if saved.Valid {
		var r componentReceipt
		if json.Unmarshal([]byte(saved.String), &r) != nil {
			return "", -1, errors.New("invalid persisted executor receipt")
		}
		return componentResult(r)
	}
	if plan.JobID == "" {
		jobID := randomComponentID()
		grantHash := ""
		// Replace the parent's credential with a bounded upload capability before
		// persisting or transmitting a plan. Signed source URLs need no app token.
		token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
		if token == "" {
			token = os.Getenv("APTEVA_APP_TOKEN")
		}
		if token != "" && strings.Contains(command, token) {
			base, err := resolvePublicURL(app)
			if err != nil {
				return "", -1, err
			}
			relay, grant, err := newExecutorGrant(ctx, app, base, timeout)
			if err != nil {
				return "", -1, err
			}
			command = strings.ReplaceAll(command, base+boundStorageProxyPath, relay+boundStorageProxyPath)
			command = strings.ReplaceAll(command, token, grant)
			// Public gateways can reject arbitrary binary request bodies before
			// they reach the relay. Only the scoped relay decodes this encoding;
			// direct Storage/S3 requests retain their existing wire format.
			command = strings.ReplaceAll(command, "export STORAGE_TOKEN=", "export APTEVA_TRANSFER_ENCODING=base64\nexport STORAGE_TOKEN=")
			sum := sha256.Sum256([]byte(grant))
			grantHash = hex.EncodeToString(sum[:])
		}
		weight, control := classifyComponentPlan(command)
		plan = componentPlan{jobID, command, timeout, weight, control}
		raw, _ := json.Marshal(plan)
		var ownerValue any
		if owner != "" {
			ownerValue = owner
		}
		if _, err := app.AppDB().Exec(`INSERT INTO media_executor_calls(job_id,project_id,owner_key,request_json,created_at,grant_hash) VALUES(?,?,?,?,?,?)`, jobID, app.CurrentProject(), ownerValue, string(raw), time.Now().Unix(), grantHash); err != nil {
			return "", -1, err
		}
	}
	raw, _ := json.Marshal(plan)
	input := map[string]any{}
	_ = json.Unmarshal(raw, &input)
	var r componentReceipt
	// Retrying submit uses the persisted exact plan and ID. An ambiguous response
	// is never permission to mint a second attempt.
	err := componentCall(ctx, app, "submit", input, &r)
	if err != nil {
		// A response may be lost after acceptance. Inspect the exact job before
		// surfacing an unresolved outcome; never create another attempt.
		if inspectErr := componentCall(ctx, app, "get", map[string]any{"job_id": plan.JobID}, &r); inspectErr != nil {
			return "", -1, fmt.Errorf("%w: executor submission unresolved (job_id=%s): %v", errRemoteCancellation, plan.JobID, err)
		}
	}
	if r.JobID != plan.JobID {
		return "", -1, errors.New("executor receipt identity mismatch")
	}
	for r.Status != "succeeded" && r.Status != "failed" && r.Status != "cancelled" {
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := componentCall(stopCtx, app, "cancel", map[string]any{"job_id": plan.JobID}, &r); err != nil || r.JobID != plan.JobID || !r.Stopped {
				return "", -1, fmt.Errorf("%w: component job_id=%s", errRemoteCancellation, plan.JobID)
			}
			if err := persistComponentReceipt(app, plan.JobID, r); err != nil {
				return "", -1, err
			}
			return r.Output, r.Exit, ctx.Err()
		case <-timer.C:
		}
		var next componentReceipt
		if err := componentCall(ctx, app, "get", map[string]any{"job_id": plan.JobID}, &next); err != nil {
			continue // Polling is read-only and remains bounded by the job deadline.
		}
		if next.JobID != plan.JobID {
			return "", -1, errors.New("executor receipt identity mismatch")
		}
		r = next
	}
	if err := persistComponentReceipt(app, plan.JobID, r); err != nil {
		return "", -1, err
	}
	return componentResult(r)
}
func classifyComponentPlan(command string) (int, bool) {
	control := strings.Contains(command, "REMOTE_CANCEL_CONFIRMED_GUARDED_NOT_STARTED") || strings.HasPrefix(command, "tail -n ") || strings.Contains(command, "echo CACHE_READY")
	if control {
		return 0, true
	}
	if strings.Contains(command, "APTEVA_RESULT:") || strings.Contains(command, "APTEVA_POSE:") || strings.Contains(command, "APTEVA_POSE_STAGE:") {
		return 2, false
	}
	return 1, false
}
func persistComponentReceipt(app *sdk.AppCtx, id string, r componentReceipt) error {
	raw, _ := json.Marshal(r)
	tx, err := app.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM media_executor_uploads WHERE token_hash=(SELECT grant_hash FROM media_executor_calls WHERE job_id=?)`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM media_executor_grants WHERE token_hash=(SELECT grant_hash FROM media_executor_calls WHERE job_id=?)`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE media_executor_calls SET receipt_json=?,request_json='{}' WHERE job_id=?`, string(raw), id); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	ackCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Acknowledgement happens strictly after the parent commit. A failed ack
	// retains the full worker receipt and is harmless to result correctness.
	var ack map[string]any
	_ = componentCall(ackCtx, app, "acknowledge", map[string]any{"job_id": id}, &ack)
	return nil
}
func componentResult(r componentReceipt) (string, int, error) {
	if !r.Stopped {
		return r.Output, r.Exit, fmt.Errorf("%w: component job_id=%s", errRemoteCancellation, r.JobID)
	}
	if r.Status != "succeeded" {
		return r.Output, r.Exit, fmt.Errorf("executor job %s: %s", r.JobID, r.Error)
	}
	return r.Output, r.Exit, nil
}
func newExecutorGrant(ctx context.Context, app *sdk.AppCtx, base string, timeout int) (string, string, error) {
	token := randomComponentID()
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	folders := []string{"/.media/thumbnail/", "/.media/waveform/", "/.media/keyframe/", transcriptAudioFolder}
	folder := app.Config().Get("render_output_folder")
	if folder == "" {
		folder = "/renders/"
	}
	folders = append(folders, folder)
	// An explicit per-render output folder is added by the render adapter.
	if folder, ok := ctx.Value(componentFolderKey{}).(string); ok {
		folders = append(folders, folder)
	}
	raw, _ := json.Marshal(folders)
	max := int64(parseConfigIntFallback(app.Config().Get("render_max_output_size_mb"), 4096)) * 1024 * 1024 * 2
	if _, err := app.AppDB().Exec(`INSERT INTO media_executor_grants(token_hash,project_id,expires_at,max_bytes,folders) VALUES(?,?,?,?,?)`, hash, app.CurrentProject(), time.Now().Add(time.Duration(timeout+60)*time.Second).Unix(), max, string(raw)); err != nil {
		return "", "", err
	}
	id := os.Getenv("APTEVA_INSTALL_ID")
	if id == "" {
		return "", "", errors.New("executor transfers require APTEVA_INSTALL_ID")
	}
	return strings.TrimRight(base, "/") + "/api/apps/media/_install/" + id + "/executor-io", token, nil
}
