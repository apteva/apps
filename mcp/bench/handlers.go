package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handlePacks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		packs, err := a.svc.db.listPacks()
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		category := normalizeTaxonomyValue(r.URL.Query().Get("category"))
		if category != "" {
			filtered := make([]Pack, 0, len(packs))
			for _, pack := range packs {
				if pack.Category == category {
					filtered = append(filtered, pack)
				}
			}
			packs = filtered
		}
		writeJSON(w, http.StatusOK, packs)
	case http.MethodPost:
		var pack Pack
		if err := json.NewDecoder(r.Body).Decode(&pack); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := a.svc.savePack(&pack, true)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, saved)
	default:
		httpError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

// handlePack serves /api/packs/<id> and its sub-actions: seal, fork, scenarios.
func (a *App) handlePack(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/packs/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id := parts[0]
	if id == "" {
		httpError(w, http.StatusBadRequest, errors.New("pack id required"))
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "seal" && r.Method == http.MethodPost:
		var body struct {
			Version string `json:"version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sealed, err := a.svc.seal(id, body.Version)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, sealed)

	case action == "fork" && r.Method == http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		draft, err := a.svc.fork(id, body.Name)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, draft)

	case action == "leaderboard" && r.Method == http.MethodGet:
		pack, err := a.svc.db.getPack(id)
		if err != nil || pack == nil {
			httpError(w, http.StatusNotFound, errors.New("pack not found"))
			return
		}
		board, err := a.svc.leaderboard(pack.Digest)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, board)

	case action == "scenarios" && r.Method == http.MethodPut:
		var scenario Scenario
		if err := json.NewDecoder(r.Body).Decode(&scenario); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		pack, err := a.svc.putScenario(id, scenario)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, pack)

	case action == "scenarios" && r.Method == http.MethodDelete:
		if len(parts) < 3 {
			httpError(w, http.StatusBadRequest, errors.New("scenario id required"))
			return
		}
		pack, err := a.svc.deleteScenario(id, parts[2])
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, pack)

	case action == "" && r.Method == http.MethodGet:
		pack, err := a.svc.db.getPack(id)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		if pack == nil {
			httpError(w, http.StatusNotFound, errors.New("pack not found"))
			return
		}
		writeJSON(w, http.StatusOK, pack)

	case action == "" && r.Method == http.MethodPut:
		var pack Pack
		if err := json.NewDecoder(r.Body).Decode(&pack); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		pack.ID = id
		saved, err := a.svc.savePack(&pack, false)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, saved)

	case action == "" && r.Method == http.MethodDelete:
		if err := a.svc.deletePack(id); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})

	default:
		httpError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		runs, err := a.svc.db.listRuns(limit)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, runs)
	case http.MethodPost:
		var input runInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		run, err := a.svc.createRun(input.PackID, input.Name, input.Targets, input.Trials)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, run)
	default:
		httpError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handleRun(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id := parts[0]
	if id == "" {
		httpError(w, http.StatusBadRequest, errors.New("run id required"))
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch {
	case action == "cancel" && r.Method == http.MethodPost:
		run, err := a.svc.cancelRun(id)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, run)

	case action == "evidence" && r.Method == http.MethodGet:
		bundle, err := a.svc.evidence(id)
		if err != nil {
			httpError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, bundle)

	case action == "baselines" && r.Method == http.MethodGet:
		comparison, err := a.svc.compareToBaselines(id)
		if err != nil {
			httpError(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, comparison)

	case action == "baselines" && r.Method == http.MethodPost:
		var body struct {
			ScenarioID  string `json:"scenario_id"`
			TargetIndex int    `json:"target_index"`
			Label       string `json:"label"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		baseline, err := a.svc.setBaseline(id, body.ScenarioID, body.TargetIndex, body.Label)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, baseline)

	case action == "" && r.Method == http.MethodGet:
		run, err := a.svc.db.getRun(id)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		if run == nil {
			httpError(w, http.StatusNotFound, errors.New("run not found"))
			return
		}
		writeJSON(w, http.StatusOK, run)

	default:
		httpError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handleProfiles(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		profiles, err := a.svc.db.listProfiles()
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, profiles)
	case http.MethodPost:
		var p Profile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := a.svc.saveProfile(&p, true)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, saved)
	default:
		httpError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

// handleProfile serves /api/profiles/<id> plus seal, fork and preview.
func (a *App) handleProfile(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/profiles/"), "/"), "/")
	id := parts[0]
	if id == "" {
		httpError(w, http.StatusBadRequest, errors.New("profile id required"))
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "seal" && r.Method == http.MethodPost:
		var body struct {
			Version string `json:"version"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sealed, err := a.svc.sealProfile(id, body.Version)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, sealed)
	case action == "fork" && r.Method == http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		draft, err := a.svc.forkProfile(id, body.Name)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusCreated, draft)
	case action == "preview" && r.Method == http.MethodPost:
		var body struct {
			PackDigest string `json:"pack_digest"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		candidate, err := a.svc.db.getProfile(id)
		if err != nil || candidate == nil {
			httpError(w, http.StatusNotFound, errors.New("profile not found"))
			return
		}
		preview, err := a.svc.previewProfile(body.PackDigest, candidate)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, preview)
	case action == "" && r.Method == http.MethodGet:
		p, err := a.svc.db.getProfile(id)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err)
			return
		}
		if p == nil {
			httpError(w, http.StatusNotFound, errors.New("profile not found"))
			return
		}
		writeJSON(w, http.StatusOK, p)
	case action == "" && r.Method == http.MethodPut:
		var p Profile
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		p.ID = id
		saved, err := a.svc.saveProfile(&p, false)
		if err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, saved)
	case action == "" && r.Method == http.MethodDelete:
		if err := a.svc.deleteProfile(id); err != nil {
			httpError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		httpError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (a *App) handleGlobalLeaderboard(w http.ResponseWriter, r *http.Request) {
	board, err := a.svc.globalLeaderboard(r.URL.Query().Get("profile_digest"), r.URL.Query().Get("category"))
	if err != nil {
		httpError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

func (a *App) handleCatalog(w http.ResponseWriter, r *http.Request) {
	catalog, err := a.svc.catalog()
	if err != nil {
		httpError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (a *App) handleScoring(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"scoring_version": ScoringVersion,
		"formula":         ScoringFormula,
		"weights":         ScoreWeights,
	})
}
