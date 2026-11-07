package main

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"strconv"
)

func (s *Service) physicalEvidence(revisionID int64) ([]PhysicalTest, error) {
	rows, err := s.store.db.Query(`SELECT payload_json FROM verification_evidence WHERE project_id=? AND revision_id=? ORDER BY id`, s.project, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PhysicalTest{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		var test PhysicalTest
		if err = json.Unmarshal(raw, &test); err != nil {
			return nil, err
		}
		out = append(out, test)
	}
	return out, rows.Err()
}
func (a *App) toolVerificationRecord(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	revision, err := s.store.GetRevision(s.project, int64Arg(args, "revision_id"))
	if err != nil {
		return nil, err
	}
	if stringArg(args, "source_sha256") != revision.SourceSHA256 {
		return nil, errors.New("evidence source_sha256 must match the tested immutable revision")
	}
	t := PhysicalTest{Kind: stringArg(args, "kind"), Status: stringArg(args, "status"), RevisionID: revision.ID, SourceSHA256: revision.SourceSHA256, EvidenceURL: stringArg(args, "evidence_url"), Notes: stringArg(args, "notes")}
	if (t.Kind != "physical_fit" && t.Kind != "enclosed_thermal") || (t.Status != "pass" && t.Status != "fail") || t.EvidenceURL == "" {
		return nil, errors.New("kind physical_fit/enclosed_thermal, status pass/fail and evidence_url required")
	}
	raw, _ := json.Marshal(t)
	_, err = s.store.db.Exec(`INSERT INTO verification_evidence(project_id,revision_id,payload_json) VALUES(?,?,?)`, s.project, revision.ID, string(raw))
	return map[string]any{"evidence": t}, err
}
func (a *App) handleVerification(w http.ResponseWriter, r *http.Request) {
	app := a.ctx
	if p := r.Header.Get("X-Apteva-Project-ID"); p != "" && app != nil {
		app = app.WithProject(p)
	}
	if r.Method == http.MethodGet {
		s, err := a.service(app)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		id, parseErr := strconv.ParseInt(r.URL.Query().Get("revision_id"), 10, 64)
		if parseErr != nil || id <= 0 {
			writeJSONError(w, 400, "positive revision_id required")
			return
		}
		if _, err := s.store.GetRevision(s.project, id); err != nil {
			writeHTTPError(w, err)
			return
		}
		e, err := s.physicalEvidence(id)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"evidence": e})
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, "GET", "POST")
		return
	}
	var args map[string]any
	if err := decodeBody(r, &args); err != nil {
		writeHTTPError(w, err)
		return
	}
	out, err := a.toolVerificationRecord(r.Context(), app, args)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
