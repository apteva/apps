package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxMobileSigningImportBytes = 16 << 20

func (a *App) httpDeploymentMobileSigningImport(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if d.TargetKind != "android" {
		httpErr(w, http.StatusBadRequest, "PKCS#12 import currently applies to Android deployments")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMobileSigningImportBytes)
	if err := r.ParseMultipartForm(maxMobileSigningImportBytes); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid or oversized multipart form")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, _, err := r.FormFile("keystore")
	if err != nil {
		httpErr(w, http.StatusBadRequest, "keystore file is required")
		return
	}
	defer file.Close()
	pfx, err := io.ReadAll(io.LimitReader(file, maxMobileSigningImportBytes+1))
	if err != nil || len(pfx) > maxMobileSigningImportBytes {
		httpErr(w, http.StatusBadRequest, "read keystore")
		return
	}
	if strings.EqualFold(r.FormValue("inspect_only"), "true") {
		target, targetErr := parseMobileTargetConfig(d.TargetConfigJSON)
		if targetErr != nil {
			httpErr(w, http.StatusBadRequest, targetErr.Error())
			return
		}
		input, _, inspectErr := androidSigningIdentityFromPKCS12(
			d.ProjectID, strings.TrimSpace(target.PackageName), pfx,
			r.FormValue("store_password"), r.FormValue("key_password"), r.FormValue("key_alias"),
		)
		if inspectErr != nil {
			httpErr(w, http.StatusBadRequest, inspectErr.Error())
			return
		}
		existing, getErr := dbGetMobileSigningIdentity(globalCtx.AppDB(), d.ProjectID, "android", "", strings.TrimSpace(target.PackageName))
		if getErr != nil {
			httpErr(w, http.StatusInternalServerError, getErr.Error())
			return
		}
		revision := 1
		if existing != nil {
			revision = existing.Revision + 1
		}
		httpJSON(w, map[string]any{
			"identity": MobileSigningIdentity{
				ProjectID: input.ProjectID, Platform: input.Platform,
				ApplicationIdentifier: input.ApplicationIdentifier, Format: input.Format,
				Revision: revision, Source: input.Source, KeyAlias: input.KeyAlias,
				CertificatePEM: input.CertificatePEM, CertificateSHA1: input.CertificateSHA1,
				CertificateSHA256: input.CertificateSHA256, ExpiresAt: input.ExpiresAt,
			},
			"replacement_required": existing != nil,
		})
		return
	}
	identity, err := a.importAndroidSigningIdentity(
		d, pfx, r.FormValue("store_password"), r.FormValue("key_password"),
		r.FormValue("key_alias"), strings.EqualFold(r.FormValue("confirm_replace"), "true"),
	)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := a.setupMobileSigning(r.Context(), d, d.BuildBackend, false)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emit("deploy.mobile_signing.imported", map[string]any{
		"deployment_id": d.ID, "environment_id": d.EnvironmentID,
		"platform": "android", "identity_id": identity.ID, "revision": identity.Revision,
	})
	httpJSON(w, result)
}

func (a *App) httpDeploymentMobileSigningRecovery(w http.ResponseWriter, r *http.Request, d *Deployment) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !body.Confirm {
		httpErr(w, http.StatusBadRequest, "confirm=true is required for a sensitive recovery export")
		return
	}
	archive, filename, err := a.exportMobileSigningRecovery(d)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	emit("deploy.mobile_signing.recovery_exported", map[string]any{
		"deployment_id": d.ID, "environment_id": d.EnvironmentID, "platform": d.TargetKind,
	})
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(archive)
}
