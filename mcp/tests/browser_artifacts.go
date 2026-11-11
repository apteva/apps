package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type browserArtifact struct {
	MIME   string `json:"mime"`
	SHA256 string `json:"sha256"`
	Base64 string `json:"base64"`
}

func saveBrowserArtifact(app *sdk.AppCtx, run, check int64, step string, artifact browserArtifact) (int64, error) {
	bytes, err := base64.StdEncoding.DecodeString(artifact.Base64)
	if err != nil || len(bytes) == 0 || len(bytes) > 768<<10 || artifact.MIME != "image/png" || http.DetectContentType(bytes) != "image/png" {
		return 0, errors.New("invalid browser screenshot")
	}
	sum := sha256.Sum256(bytes)
	if hex.EncodeToString(sum[:]) != artifact.SHA256 {
		return 0, errors.New("screenshot integrity check failed")
	}
	var count int
	if err = app.AppDB().QueryRow(`SELECT count(*) FROM test_artifacts WHERE project_id=? AND run_id=?`, app.CurrentProject(), run).Scan(&count); err != nil {
		return 0, err
	}
	if count >= 10 {
		return 0, errors.New("run screenshot limit reached")
	}
	result, err := app.AppDB().Exec(`INSERT INTO test_artifacts(project_id,run_id,check_id,step_id,mime,digest,bytes,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		app.CurrentProject(), run, check, step, artifact.MIME, artifact.SHA256, bytes, now())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
func (a *App) httpBrowserArtifact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	ctx := r.Context()
	if project := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); project != "" {
		ctx = sdk.WithCaller(ctx, &sdk.Caller{ProjectID: project})
	}
	app, err := scoped(ctx, a.ctx, r.URL.Query().Get("project_id"))
	if err != nil {
		http.Error(w, "project access denied", 403)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/artifacts/"), 10, 64)
	if err != nil || id < 1 {
		http.NotFound(w, r)
		return
	}
	var bytes []byte
	var mime, digest string
	if err = app.AppDB().QueryRow(`SELECT mime,digest,bytes FROM test_artifacts WHERE project_id=? AND id=?`, app.CurrentProject(), id).Scan(&mime, &digest, &bytes); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("ETag", `"`+digest+`"`)
	_, _ = w.Write(bytes)
}
func pruneBrowserArtifacts(app *sdk.AppCtx) error {
	days, err := strconv.Atoi(app.Config().Get("artifact_retention_days"))
	if err != nil || days < 1 || days > 90 {
		days = 7
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err = app.AppDB().Exec(`DELETE FROM test_artifacts WHERE project_id=? AND created_at<? AND run_id IN (SELECT id FROM test_runs WHERE project_id=? AND status IN ('passed','failed'))`, app.CurrentProject(), cutoff, app.CurrentProject())
	return err
}
