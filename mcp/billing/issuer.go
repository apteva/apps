package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"os"
	"strings"
)

func emptyIssuer() *Issuer {
	return &Issuer{
		Address:    json.RawMessage("{}"),
		TaxIDs:     json.RawMessage("[]"),
		Bank:       json.RawMessage("{}"),
		Metadata:   json.RawMessage("{}"),
		Configured: false,
	}
}

func dbIssuerGet(db *sql.DB, pid string) (*Issuer, error) {
	var iss Issuer
	var addr, taxes, bank, meta string
	load := func(projectID string) error {
		return db.QueryRow(
			`SELECT display_name, COALESCE(legal_name,''), COALESCE(email,''),
		        COALESCE(phone,''), COALESCE(website,''), COALESCE(brand_color,''),
		        address, tax_ids, bank,
		        COALESCE(footer_text,''), COALESCE(default_terms,''), metadata,
		        created_at, updated_at
		 FROM issuer_settings WHERE project_id = ?`, projectID).Scan(
			&iss.DisplayName, &iss.LegalName, &iss.Email,
			&iss.Phone, &iss.Website, &iss.BrandColor,
			&addr, &taxes, &bank,
			&iss.FooterText, &iss.DefaultTerms, &meta,
			&iss.CreatedAt, &iss.UpdatedAt,
		)
	}
	err := load(pid)
	// Migration 005 stores the old singleton under the empty key. Only a
	// project-scoped process may inherit it; a global install must never expose
	// one project's legal identity or bank details to another project.
	if errors.Is(err, sql.ErrNoRows) && strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")) != "" {
		err = load("")
	}
	if err == sql.ErrNoRows {
		return emptyIssuer(), nil
	}
	if err != nil {
		return nil, err
	}
	iss.Address = json.RawMessage(addr)
	iss.TaxIDs = json.RawMessage(taxes)
	iss.Bank = json.RawMessage(bank)
	iss.Metadata = json.RawMessage(meta)
	iss.Configured = true
	return &iss, nil
}

func dbIssuerSet(db *sql.DB, pid string, patch map[string]any) (*Issuer, error) {
	if strings.TrimSpace(pid) == "" {
		return nil, errors.New("project_id required")
	}
	display := strings.TrimSpace(strArg(patch, "display_name"))
	if display == "" {
		return nil, errors.New("display_name required")
	}
	addr := jsonOrEmpty(patch["address"], "{}")
	taxes := jsonOrEmpty(patch["tax_ids"], "[]")
	bank := jsonOrEmpty(patch["bank"], "{}")
	meta := jsonOrEmpty(patch["metadata"], "{}")
	now := nowRFC3339()
	if _, err := db.Exec(
		`INSERT INTO issuer_settings
		     (project_id, display_name, legal_name, email, phone, website, brand_color,
		      address, tax_ids, bank, footer_text, default_terms, metadata,
		      created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id) DO UPDATE SET
		     display_name  = excluded.display_name,
		     legal_name    = excluded.legal_name,
		     email         = excluded.email,
		     phone         = excluded.phone,
		     website       = excluded.website,
		     brand_color   = excluded.brand_color,
		     address       = excluded.address,
		     tax_ids       = excluded.tax_ids,
		     bank          = excluded.bank,
		     footer_text   = excluded.footer_text,
		     default_terms = excluded.default_terms,
		     metadata      = excluded.metadata,
		     updated_at    = excluded.updated_at`,
		pid, display,
		strArg(patch, "legal_name"),
		strArg(patch, "email"),
		strArg(patch, "phone"),
		strArg(patch, "website"),
		strArg(patch, "brand_color"),
		addr, taxes, bank,
		strArg(patch, "footer_text"),
		strArg(patch, "default_terms"),
		meta,
		now, now,
	); err != nil {
		return nil, err
	}
	return dbIssuerGet(db, pid)
}

func (a *App) handleHTTPIssuer(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		iss, err := dbIssuerGet(ctx.AppReadDB(), pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"issuer": iss})
	case http.MethodPut, http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		iss, err := dbIssuerSet(ctx.AppDB(), pid, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"issuer": iss})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) toolIssuerGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	iss, err := dbIssuerGet(ctx.AppReadDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"issuer": iss}, nil
}

func (a *App) toolIssuerSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	iss, err := dbIssuerSet(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"issuer": iss}, nil
}
