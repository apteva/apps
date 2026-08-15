package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// REST surface — mirror of the MCP tools.
//
//   /api/deployments                          collection
//   /api/deployments/<id-or-name>             one deployment + sub-actions
//   /api/builds/<id>                          build detail / log / artifact
//   /api/releases/<id>                        release detail / log

func (a *App) handleDeploymentsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListDeployments(w, r)
	case http.MethodPost:
		a.httpCreateDeployment(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleDeploymentItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/deployments/")
	if rest == "" {
		httpErr(w, http.StatusBadRequest, "id or name required")
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	key := parts[0]
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	d, err := lookupDeploymentByKey(pid, key)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}
	d, err = deploymentWithEnvironmentFromRequest(d, r)
	if err != nil {
		httpErr(w, http.StatusNotFound, err.Error())
		return
	}

	switch {
	case tail == "":
		a.httpDeploymentDetail(w, r, d)
	case tail == "build":
		a.httpDeploymentBuild(w, r, d)
	case tail == "release":
		a.httpDeploymentRelease(w, r, d)
	case tail == "promote":
		a.httpDeploymentPromote(w, r, d)
	case tail == "stop":
		a.httpDeploymentStop(w, r, d)
	case tail == "restart":
		a.httpDeploymentRestart(w, r, d)
	case tail == "mobile-signing":
		a.httpDeploymentMobileSigning(w, r, d)
	case tail == "mobile-signing/setup":
		a.httpDeploymentMobileSigningSetup(w, r, d)
	case tail == "mobile-signing/import":
		a.httpDeploymentMobileSigningImport(w, r, d)
	case tail == "mobile-signing/recovery":
		a.httpDeploymentMobileSigningRecovery(w, r, d)
	case tail == "store-config":
		a.httpDeploymentStoreConfig(w, r, d)
	case tail == "store-plan":
		a.httpDeploymentStorePlan(w, r, d)
	case tail == "store-preflight":
		a.httpDeploymentStorePreflight(w, r, d)
	case tail == "store-apply":
		a.httpDeploymentStoreApply(w, r, d)
	case tail == "store-sync":
		a.httpDeploymentStoreSync(w, r, d)
	case tail == "store-assets":
		a.httpDeploymentStoreAssets(w, r, d)
	case tail == "distribution":
		a.httpDeploymentDistribution(w, r, d)
	case tail == "cloud-backend/setup":
		a.httpDeploymentCloudBackendSetup(w, r, d)
	case tail == "logs":
		a.httpDeploymentLogs(w, r, d)
	case tail == "attach-domain":
		a.httpDeploymentAttachDomain(w, r, d)
	case tail == "detach-domain":
		a.httpDeploymentDetachDomain(w, r, d)
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

func (a *App) httpDeploymentStoreConfig(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if mobileStoreProvider(d.TargetKind) == "" {
		httpErr(w, http.StatusBadRequest, "store listing is only available for mobile deployments")
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, doc, err := a.mobileStoreConfig(d)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		preflight := validateStoreDocument(a.dataDir, d, nil, cfg, doc, false)
		appendProviderReadinessFindings(&preflight, d, cfg)
		httpJSON(w, map[string]any{"config": cfg, "desired": doc, "preflight": preflight})
	case http.MethodPut:
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		desired := body
		if nested, ok := body["desired"].(map[string]any); ok {
			desired = nested
		}
		raw, _ := json.Marshal(desired)
		doc, err := parseStoreDocument(string(raw), d.TargetKind)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg, err := dbUpsertMobileStoreConfig(globalCtx.AppDB(), d, doc)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		a.pruneUnreferencedStoreAssets(d, doc)
		preflight := validateStoreDocument(a.dataDir, d, nil, cfg, doc, false)
		appendProviderReadinessFindings(&preflight, d, cfg)
		_ = dbUpdateMobileStoreState(globalCtx.AppDB(), cfg.ID, cfg.Status, "", mustJSON(preflight), "", cfg.LastError)
		cfg, _ = dbGetMobileStoreConfig(globalCtx.AppDB(), d.ID, d.EnvironmentID, d.TargetKind)
		emit("deploy.store.updated", map[string]any{"deployment_id": d.ID, "environment_id": d.EnvironmentID, "provider": cfg.Provider})
		httpJSON(w, map[string]any{"config": cfg, "desired": doc, "preflight": preflight})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or PUT")
	}
}

func (a *App) httpDeploymentStorePlan(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	build, err := storeBuildFromRequest(d, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	plan, err := a.storePlan(d, build, true)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, plan)
}

func (a *App) httpDeploymentStorePreflight(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	build, err := storeBuildFromRequest(d, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg, doc, err := a.mobileStoreConfig(d)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	preflight := validateStoreDocument(a.dataDir, d, build, cfg, doc, true)
	appendProviderReadinessFindings(&preflight, d, cfg)
	if err := persistStorePreflightState(globalCtx.AppDB(), cfg, preflight); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, preflight)
}

func (a *App) httpDeploymentStoreApply(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body StoreApplyRequest
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	}
	build, err := storeBuildFromRequest(d, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := a.applyStoreConfigScoped(d, build, true, body)
	if err != nil {
		httpStoreErr(w, err, http.StatusBadRequest)
		return
	}
	emit("deploy.store.applied", map[string]any{"deployment_id": d.ID, "environment_id": d.EnvironmentID, "provider": result.Config.Provider, "status": result.Status})
	httpJSON(w, result)
}

func (a *App) httpDeploymentStoreSync(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	cfg, observed, err := a.observeStoreConfig(d)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, map[string]any{"config": cfg, "observed": observed})
}

func (a *App) httpDeploymentStoreAssets(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 101<<20)
	if err := r.ParseMultipartForm(101 << 20); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid multipart upload: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpErr(w, http.StatusBadRequest, "file required")
		return
	}
	defer file.Close()
	asset, err := a.saveStoreAsset(d, header.Filename, file)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	asset.Locale = defaultStr(strings.TrimSpace(r.FormValue("locale")), "en-US")
	asset.Kind = defaultStr(strings.TrimSpace(r.FormValue("kind")), "phone_screenshot")
	asset.DisplayTarget = strings.TrimSpace(r.FormValue("display_target"))
	asset.Order, _ = strconv.Atoi(r.FormValue("order"))
	httpJSON(w, map[string]any{"asset": asset})
}

func storeBuildFromRequest(d *Deployment, r *http.Request) (*Build, error) {
	id := queryInt(r, "build_id", 0)
	if id == 0 {
		return nil, nil
	}
	build, err := dbGetBuild(globalCtx.AppDB(), int64(id))
	if err != nil || build == nil || build.DeploymentID != d.ID || (d.EnvironmentID > 0 && build.EnvironmentID != d.EnvironmentID) {
		return nil, fmt.Errorf("build %d not found for deployment environment", id)
	}
	return build, nil
}

func (a *App) httpDeploymentDistribution(w http.ResponseWriter, r *http.Request, d *Deployment) {
	args := map[string]any{}
	switch r.Method {
	case http.MethodGet:
		args["channel"] = r.URL.Query().Get("channel")
		args["beta_group_id"] = r.URL.Query().Get("beta_group_id")
		args["group_name"] = r.URL.Query().Get("group_name")
		if releaseID := queryInt(r, "release_id", 0); releaseID > 0 {
			args["release_id"] = releaseID
		}
		state, err := a.mobileDistributionStatus(d, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, state)
	case http.MethodPost, http.MethodPut:
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		state, err := a.updateMobileDistribution(d, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, state)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, POST or PUT")
	}
}

func (a *App) httpDeploymentCloudBackendSetup(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	result, err := a.setupCloudBackend(r.Context(), d, cloudBackendSetupInput{
		Provider: strArg(body, "provider"), RepositoryURL: strArg(body, "repository_url"),
		TeamID: strArg(body, "team_id"), WorkflowID: strArg(body, "workflow_id"),
		Branch: strArg(body, "branch"), ArtifactMode: strArg(body, "artifact_mode"),
	})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, result)
}

func (a *App) httpDeploymentMobileSigning(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	setups, err := dbListMobileSigningSetups(globalCtx.AppDB(), d.ID, d.EnvironmentID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	identities := make([]MobileSigningIdentity, 0, len(setups))
	seen := map[int64]bool{}
	for i := range setups {
		if setups[i].IdentityID <= 0 || seen[setups[i].IdentityID] {
			continue
		}
		identity, getErr := dbGetMobileSigningIdentityByID(globalCtx.AppDB(), setups[i].IdentityID)
		if getErr != nil {
			httpErr(w, http.StatusInternalServerError, getErr.Error())
			return
		}
		if identity != nil {
			identities = append(identities, *identity)
			seen[identity.ID] = true
		}
	}
	if len(identities) == 0 && d.TargetKind == "android" {
		identity, getErr := a.mobileSigningIdentityForDeployment(d)
		if getErr != nil {
			httpErr(w, http.StatusInternalServerError, getErr.Error())
			return
		}
		if identity != nil {
			identities = append(identities, *identity)
		}
	}
	httpJSON(w, map[string]any{
		"deployment_id":  d.ID,
		"environment_id": d.EnvironmentID,
		"environment":    d.EnvironmentName,
		"setups":         setups,
		"identities":     identities,
		"signing":        mobileSigningStatusSummary(d, setups, identities),
		"count":          len(setups),
	})
}

func (a *App) httpDeploymentMobileSigningSetup(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Provider string `json:"provider"`
		Rotate   bool   `json:"rotate"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
	}
	result, err := a.setupMobileSigning(r.Context(), d, body.Provider, body.Rotate)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, result)
}

func (a *App) httpDeploymentPromote(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	body["id"] = float64(d.ID)
	body["_project_id"] = d.ProjectID
	out, err := a.toolPromote(globalCtx, body)
	if err != nil {
		httpStoreErr(w, err, http.StatusBadRequest)
		return
	}
	httpJSON(w, out)
}

func (a *App) handleBuildItem(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/builds/")
	tail := ""
	if i := strings.Index(idStr, "/"); i >= 0 {
		tail = idStr[i+1:]
		idStr = idStr[:i]
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "build id required")
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), id)
	if err != nil || build == nil {
		httpErr(w, http.StatusNotFound, "build not found")
		return
	}
	switch tail {
	case "":
		pid, err := resolveProjectFromRequest(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if d, getErr := dbGetDeployment(globalCtx.AppDB(), pid, build.DeploymentID); getErr != nil || d == nil {
			httpErr(w, http.StatusNotFound, "build not found")
			return
		}
		httpJSON(w, map[string]any{"build": buildWithArtifactDownloadURL(build, pid)})
	case "log":
		body, _ := tailFile(build.LogPath, queryInt(r, "tail", 200))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	case "cancel":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		cancelled, err := a.cancelCloudBuild(r.Context(), build)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"build": cancelled, "cancelled": cancelled.Status == "cancelled"})
	case "artifact":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			httpErr(w, http.StatusMethodNotAllowed, "GET or HEAD")
			return
		}
		pid, err := resolveProjectFromRequest(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		deployment, err := dbGetDeployment(globalCtx.AppDB(), pid, build.DeploymentID)
		if err != nil || deployment == nil {
			httpErr(w, http.StatusNotFound, "build not found")
			return
		}
		artifact, err := resolveBuildArtifactDownload(deployment, build)
		if errors.Is(err, errBuildArtifactNotReady) {
			httpErr(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, errBuildArtifactPruned) {
			httpErr(w, http.StatusGone, err.Error())
			return
		}
		if err != nil {
			httpErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if err := serveBuildArtifact(w, r, artifact); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				_, _ = fmt.Fprintf(os.Stderr, "deploy: stream build %d artifact: %v\n", build.ID, err)
			}
		}
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

func (a *App) handleReleaseItem(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/releases/")
	tail := ""
	if i := strings.Index(idStr, "/"); i >= 0 {
		tail = idStr[i+1:]
		idStr = idStr[:i]
	}
	id, _ := strconv.ParseInt(idStr, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "release id required")
		return
	}
	rel, err := dbGetRelease(globalCtx.AppDB(), id)
	if err != nil || rel == nil {
		httpErr(w, http.StatusNotFound, "release not found")
		return
	}
	switch tail {
	case "":
		httpJSON(w, map[string]any{"release": rel})
	case "log":
		body, _ := tailFile(rel.LogPath, queryInt(r, "tail", 200))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	case "sync":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		out, err := a.toolReleaseSync(globalCtx, map[string]any{"release_id": float64(rel.ID)})
		if err != nil {
			httpStoreErr(w, err, http.StatusBadRequest)
			return
		}
		httpJSON(w, out)
	case "rollout":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body struct {
			Fraction float64 `json:"fraction"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		out, err := a.toolRollout(globalCtx, map[string]any{"release_id": float64(rel.ID), "fraction": body.Fraction})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "halt":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		out, err := a.toolHalt(globalCtx, map[string]any{"release_id": float64(rel.ID)})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "release-approved":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		updated, err := a.releaseApprovedMobileVersion(rel)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"release": updated, "release_requested": true})
	default:
		httpErr(w, http.StatusNotFound, "no such resource")
	}
}

// ─── Collection ────────────────────────────────────────────────────

func (a *App) httpListDeployments(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	include := r.URL.Query().Get("archived") == "1"
	rows, err := dbListDeployments(globalCtx.AppDB(), pid, include)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"deployments": rows, "count": len(rows)})
}

func (a *App) httpCreateDeployment(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Name             string `json:"name"`
		TargetKind       string `json:"target_kind"`
		Description      string `json:"description"`
		SourceKind       string `json:"source_kind"`
		SourceRef        string `json:"source_ref"`
		Framework        string `json:"framework"`
		BuildCmd         string `json:"build_cmd"`
		BuildBackend     string `json:"build_backend"`
		BuildBackendJSON string `json:"build_backend_config_json"`
		StartCmd         string `json:"start_cmd"`
		PortHint         int    `json:"port_hint"`
		EnvJSON          string `json:"env_json"`
		TargetConfigJSON string `json:"target_config_json"`
		Domain           string `json:"domain"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := validateName(body.Name); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	domainArg := strings.TrimSpace(body.Domain)
	domainsOn := domainArg != "" && a.domainsAvailable(globalCtx)
	in := CreateDeploymentInput{
		Name: body.Name, TargetKind: normalizeTargetKind(body.TargetKind), Description: body.Description,
		SourceKind: body.SourceKind, SourceRef: body.SourceRef,
		Framework: body.Framework,
		BuildCmd:  body.BuildCmd, BuildBackend: normalizeBuildBackend(body.BuildBackend),
		BuildBackendJSON: body.BuildBackendJSON, StartCmd: body.StartCmd,
		PortHint: body.PortHint, EnvJSON: body.EnvJSON, TargetConfigJSON: body.TargetConfigJSON,
	}
	if err := validateBuildBackendSelection(in.BuildBackend, defaultStr(in.BuildBackendJSON, "{}")); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.TargetKind != "service" && in.TargetKind != "android" && in.TargetKind != "ios" {
		httpErr(w, http.StatusBadRequest, "target_kind must be service, android, or ios")
		return
	}
	if in.TargetKind == "android" || in.TargetKind == "ios" {
		if in.Framework == "" {
			in.Framework = in.TargetKind
		}
		if in.Framework != in.TargetKind {
			httpErr(w, http.StatusBadRequest, "mobile target_kind must match framework")
			return
		}
		if domainArg != "" {
			httpErr(w, http.StatusBadRequest, "domains apply to service deployments")
			return
		}
	}
	if !domainsOn {
		in.Domain = domainArg
	}
	d, err := dbCreateDeployment(globalCtx.AppDB(), pid, in)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	env, err := dbEnsureProductionEnvironment(globalCtx.AppDB(), d)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	effective := effectiveDeploymentForEnvironment(d, env)
	emit("deploy.created", map[string]any{"deployment_id": d.ID, "name": d.Name, "source_kind": d.SourceKind})
	resp := map[string]any{"deployment": d}
	if domainsOn {
		attachRes, err := a.attachDomain(globalCtx, effective, attachDomainSpec{FQDN: domainArg})
		if err != nil {
			resp["domain_error"] = err.Error()
		} else {
			d, _ = dbGetDeployment(globalCtx.AppDB(), pid, d.ID)
			resp["deployment"] = d
			resp["attach"] = attachRes
		}
	}
	httpJSON(w, resp)
}

// ─── Item ──────────────────────────────────────────────────────────

func (a *App) httpDeploymentDetail(w http.ResponseWriter, r *http.Request, d *Deployment) {
	switch r.Method {
	case http.MethodGet:
		builds, _ := dbListBuildsForEnv(globalCtx.AppDB(), d.ID, d.EnvironmentID, 10)
		releases, _ := dbListReleasesForEnv(globalCtx.AppDB(), d.ID, d.EnvironmentID, 10)
		var current *Release
		if d.CurrentReleaseID != nil {
			current, _ = dbGetRelease(globalCtx.AppDB(), *d.CurrentReleaseID)
		}
		httpJSON(w, map[string]any{
			"deployment":      d,
			"builds":          buildsWithArtifactDownloadURLs(builds, d.ProjectID),
			"releases":        releases,
			"current_release": current,
			"url":             a.deploymentURL(d, current),
		})
	case http.MethodDelete:
		if d.EnvironmentID > 0 && d.EnvironmentName != defaultEnvironmentName {
			if d.Domain != "" || d.DomainRecordID != "" {
				_ = a.detachDomain(globalCtx, d)
			}
			if err := a.stopRunningReleasesForDeployment(d.ID, d.EnvironmentID, 5*time.Second); err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if err := dbUpdateEnvironment(globalCtx.AppDB(), d.EnvironmentID, map[string]any{"archived_at": nowUTC(), "current_release_id": nil}); err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			_ = os.RemoveAll(filepath.Join(a.dataDir, "store-assets", strconv.FormatInt(d.ID, 10), strconv.FormatInt(d.EnvironmentID, 10)))
			emit("deploy.environment.destroyed", map[string]any{
				"deployment_id": d.ID, "environment_id": d.EnvironmentID, "environment": d.EnvironmentName,
			})
			httpJSON(w, map[string]any{"destroyed": true, "environment": d.EnvironmentName, "environment_id": d.EnvironmentID})
			return
		}
		if err := a.stopRunningReleasesForDeployment(d.ID, 0, 5*time.Second); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		builds, _ := dbListBuilds(globalCtx.AppDB(), d.ID, 100000)
		_ = dbDeleteDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
		a.removeBuildDirs(builds)
		_ = os.RemoveAll(filepath.Join(a.dataDir, "store-assets", strconv.FormatInt(d.ID, 10)))
		emit("deploy.destroyed", map[string]any{"deployment_id": d.ID, "name": d.Name})
		httpJSON(w, map[string]any{"destroyed": true, "id": d.ID})
	case http.MethodPatch:
		a.httpDeploymentPatch(w, r, d)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH or DELETE")
	}
}

// httpDeploymentPatch mutates an allowlist of deployment fields
// without delete+recreate. The fields are exactly the ones in
// dbUpdateDeployment; unknown keys are ignored (silently — clearer
// API than rejecting). New values take effect on the NEXT release
// build/restart; the live process keeps its env until restarted.
// Use POST /restart (or deploy_restart) to apply config without a
// fresh build.
func (a *App) httpDeploymentPatch(w http.ResponseWriter, r *http.Request, d *Deployment) {
	body, err := patchBodyFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body) == 0 {
		httpErr(w, http.StatusBadRequest, "no mutable fields in body")
		return
	}
	nextBackend := d.BuildBackend
	nextBackendJSON := d.BuildBackendJSON
	if value, ok := body["build_backend"].(string); ok {
		nextBackend = normalizeBuildBackend(value)
		body["build_backend"] = nextBackend
	}
	if value, ok := body["build_backend_config_json"].(string); ok {
		nextBackendJSON = value
	}
	if err := validateBuildBackendSelection(nextBackend, nextBackendJSON); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if d.EnvironmentID > 0 {
		if err := dbUpdateEnvironment(globalCtx.AppDB(), d.EnvironmentID, body); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if d.EnvironmentName == defaultEnvironmentName {
			_ = dbUpdateDeployment(globalCtx.AppDB(), d.ProjectID, d.ID, body)
		}
	} else if err := dbUpdateDeployment(globalCtx.AppDB(), d.ProjectID, d.ID, body); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emit("deploy.updated", map[string]any{
		"deployment_id": d.ID, "name": d.Name,
		"fields": keysOf(body),
	})
	fresh, _ := deploymentWithEnvironmentFromRequest(d, r)
	httpJSON(w, map[string]any{
		"deployment": fresh,
		"applied":    keysOf(body),
		"note":       "new values take effect on the next build/release. Call POST /restart to apply now without rebuilding.",
	})
}

// httpDeploymentRestart re-spawns the current release with whatever
// config the deployment row now holds. Stops the live release
// authoritatively (port-free guarantee), then runs runRelease with
// the same build_id, so a config-only change (env_json, port_hint,
// start_cmd) takes effect without a rebuild. Falls back to error if
// there's nothing to restart.
func (a *App) httpDeploymentRestart(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if d.CurrentReleaseID == nil {
		httpErr(w, http.StatusBadRequest, "no current release to restart — run /build or /release first")
		return
	}
	rel, err := dbGetRelease(globalCtx.AppDB(), *d.CurrentReleaseID)
	if err != nil || rel == nil {
		httpErr(w, http.StatusInternalServerError, "current release missing")
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), rel.BuildID)
	if err != nil || build == nil {
		httpErr(w, http.StatusInternalServerError, "build for current release missing")
		return
	}
	// Authoritative stop so the port is genuinely free before the
	// respawn binds — same guarantee operator-driven stop has.
	if err := a.stopReleaseAuthoritative(rel, 5*time.Second); err != nil {
		httpErr(w, http.StatusInternalServerError, "stop: "+err.Error())
		return
	}
	a.markStopped(rel.ID)
	// Re-fetch the deployment so runRelease sees the latest env_json /
	// port_hint / start_cmd / etc. — that's the whole point of restart.
	fresh, _ := deploymentWithEnvironmentFromRequest(d, r)
	if fresh == nil {
		fresh = d
	}
	newRel, err := a.runRelease(fresh, build)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "release: "+err.Error())
		return
	}
	emit("deploy.restarted", map[string]any{
		"deployment_id": d.ID, "release_id": newRel.ID, "build_id": build.ID,
	})
	httpJSON(w, map[string]any{
		"release": newRel,
		"url":     a.deploymentURL(fresh, newRel),
	})
}

func patchBodyFromRequest(r *http.Request) (map[string]any, error) {
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	// Filter to the allowlist. PortHint comes in as float64 from JSON;
	// the SQL driver doesn't mind, but coerce to int for clarity.
	allow := map[string]bool{
		"description": true, "framework": true,
		"build_cmd": true, "start_cmd": true,
		"build_backend": true, "build_backend_config_json": true,
		"port_hint": true, "env_json": true, "source_ref": true,
		"source_extra_json":  true,
		"target_config_json": true,
	}
	out := map[string]any{}
	for k, v := range raw {
		if !allow[k] {
			continue
		}
		if k == "port_hint" {
			if f, ok := v.(float64); ok {
				out[k] = int(f)
				continue
			}
		}
		out[k] = v
	}
	return out, nil
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func (a *App) httpDeploymentBuild(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	var releaseOpts *releaseOptions
	if boolArg(body, "release") {
		opts := releaseOptionsFromArgs(body)
		releaseOpts = &opts
	}
	build, err := a.runBuildWithOptions(d, releaseOpts)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	res := map[string]any{"build": buildWithArtifactDownloadURL(build, d.ProjectID)}
	if boolArg(body, "release") {
		opts := *releaseOpts
		if build.Status == "succeeded" {
			rel, err := a.runReleaseWithOptions(d, build, opts)
			if err != nil {
				res["release_error"] = err.Error()
			} else {
				res["release"] = rel
				res["url"] = a.deploymentURL(d, rel)
			}
		} else if normalizeBuildBackend(build.BuildBackend) != buildBackendLocal {
			optsJSON, _ := json.Marshal(opts)
			_ = dbUpdateBuild(globalCtx.AppDB(), build.ID, map[string]any{
				"release_requested": true, "release_options_json": string(optsJSON),
			})
			build, _ = dbGetBuild(globalCtx.AppDB(), build.ID)
			res["build"] = buildWithArtifactDownloadURL(build, d.ProjectID)
			res["release_requested"] = true
		}
	}
	httpJSON(w, res)
}

func (a *App) httpDeploymentRelease(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	buildID := int64(intArg(body, "build_id"))
	if buildID == 0 {
		httpErr(w, http.StatusBadRequest, "build_id required")
		return
	}
	build, err := dbGetBuild(globalCtx.AppDB(), buildID)
	if err != nil || build == nil || build.DeploymentID != d.ID {
		httpErr(w, http.StatusBadRequest, "build does not belong to deployment")
		return
	}
	rel, err := a.runReleaseWithOptions(d, build, releaseOptionsFromArgs(body))
	if err != nil {
		httpStoreErr(w, err, http.StatusInternalServerError)
		return
	}
	httpJSON(w, map[string]any{"release": rel, "url": a.deploymentURL(d, rel)})
}

func (a *App) httpDeploymentStop(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if d.CurrentReleaseID == nil {
		httpJSON(w, map[string]any{"stopped": false, "reason": "no live release"})
		return
	}
	rid := *d.CurrentReleaseID
	rel, _ := dbGetRelease(globalCtx.AppDB(), rid)
	// Authoritative stop: don't return until the port is actually free
	// (or report the failure). Fixes the orphan class where runtime.Stop
	// was a no-op (registry handle missing) and the process kept serving.
	if err := a.stopReleaseAuthoritative(rel, 5*time.Second); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.markStopped(rid)
	if d.EnvironmentID > 0 {
		_ = dbSetEnvironmentCurrentRelease(globalCtx.AppDB(), d.EnvironmentID, nil)
	} else {
		_ = dbSetCurrentRelease(globalCtx.AppDB(), d.ID, nil)
	}
	httpJSON(w, map[string]any{"stopped": true, "release_id": rid})
}

func (a *App) httpDeploymentLogs(w http.ResponseWriter, r *http.Request, d *Deployment) {
	// Default: tail current release's log; ?build_id= or ?release_id= overrides.
	tail := queryInt(r, "tail", 200)
	if bid := queryInt(r, "build_id", 0); bid != 0 {
		b, err := dbGetBuild(globalCtx.AppDB(), int64(bid))
		if err != nil || b == nil || b.DeploymentID != d.ID {
			httpErr(w, http.StatusNotFound, "build not found")
			return
		}
		body, _ := tailFile(b.LogPath, tail)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
		return
	}
	rid := queryInt(r, "release_id", 0)
	if rid == 0 && d.CurrentReleaseID != nil {
		rid = int(*d.CurrentReleaseID)
	}
	if rid == 0 {
		httpErr(w, http.StatusNotFound, "no release to read logs from")
		return
	}
	rel, err := dbGetRelease(globalCtx.AppDB(), int64(rid))
	if err != nil || rel == nil || rel.DeploymentID != d.ID {
		httpErr(w, http.StatusNotFound, "release not found")
		return
	}
	body, _ := tailFile(rel.LogPath, tail)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(body))
}

func (a *App) httpDeploymentAttachDomain(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if d.TargetKind != "service" {
		httpErr(w, http.StatusBadRequest, "domains apply to service deployments, not mobile binaries")
		return
	}
	var body struct {
		FQDN   string `json:"fqdn"`
		Target string `json:"target"`
		Type   string `json:"type"`
		TTL    int    `json:"ttl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	attachRes, err := a.attachDomain(globalCtx, d, attachDomainSpec{
		FQDN: body.FQDN, Target: body.Target, Type: body.Type, TTL: body.TTL,
	})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, _ := dbGetDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
	httpJSON(w, map[string]any{"deployment": out, "attach": attachRes})
}

func (a *App) httpDeploymentDetachDomain(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	res := map[string]any{"detached": true, "id": d.ID, "fqdn": d.Domain}
	if err := a.detachDomain(globalCtx, d); err != nil {
		res["registrar_error"] = err.Error()
	}
	out, _ := dbGetDeployment(globalCtx.AppDB(), d.ProjectID, d.ID)
	res["deployment"] = out
	httpJSON(w, res)
}

// ─── helpers ──────────────────────────────────────────────────────

func lookupDeploymentByKey(projectID, key string) (*Deployment, error) {
	if id, err := strconv.ParseInt(key, 10, 64); err == nil && id > 0 {
		d, err := dbGetDeployment(globalCtx.AppDB(), projectID, id)
		if err != nil {
			return nil, err
		}
		if d == nil {
			return nil, errNotFound("deployment", key)
		}
		return d, nil
	}
	d, err := dbGetDeploymentByName(globalCtx.AppDB(), projectID, key)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, errNotFound("deployment", key)
	}
	return d, nil
}

func deploymentWithEnvironmentFromRequest(d *Deployment, r *http.Request) (*Deployment, error) {
	if d == nil {
		return nil, errNotFound("deployment", "")
	}
	envName := normalizeEnvironmentName(r.URL.Query().Get("environment"))
	env, err := dbGetEnvironmentByName(globalCtx.AppDB(), d.ID, envName)
	if err != nil {
		return nil, err
	}
	if env == nil && envName == defaultEnvironmentName {
		env, err = dbEnsureProductionEnvironment(globalCtx.AppDB(), d)
	}
	if err != nil {
		return nil, err
	}
	if env == nil {
		return nil, errNotFound("environment", envName)
	}
	return effectiveDeploymentForEnvironment(d, env), nil
}

type notFoundErr struct{ kind, key string }

func (e *notFoundErr) Error() string     { return e.kind + " " + e.key + " not found" }
func errNotFound(kind, key string) error { return &notFoundErr{kind: kind, key: key} }

func queryInt(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func httpStoreErr(w http.ResponseWriter, err error, fallbackStatus int) {
	var preflightErr *storePreflightError
	if errors.As(err, &preflightErr) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": err.Error(), "code": "store_preflight_failed", "preflight": preflightErr.Preflight,
		})
		return
	}
	var validationErr *providerValidationError
	if errors.As(err, &validationErr) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(providerValidationErrorPayload(validationErr))
		return
	}
	httpErr(w, fallbackStatus, err.Error())
}
