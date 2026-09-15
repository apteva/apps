package main

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

func (a *App) handleBankAuthorization(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		conn, err := financeConnection(globalCtx, "enable-banking", int64(intArg(queryArgs(r), "connection_id", 0)))
		if err != nil {
			writeOrErr(w, nil, err)
			return
		}
		rows, err := globalCtx.AppDB().Query(`SELECT id,bank_name,country,CASE WHEN status IN ('pending','starting') AND expires_at < strftime('%s','now') THEN 'expired' ELSE status END,session_id FROM bank_authorizations WHERE project_id=? AND connection_id=? ORDER BY created_at DESC`, projectID(globalCtx), conn.ID)
		if err != nil {
			writeOrErr(w, nil, err)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, bank, country, status, session string
			if err = rows.Scan(&id, &bank, &country, &status, &session); err != nil {
				break
			}
			out = append(out, map[string]string{"id": id, "bank_name": bank, "country": country, "status": status, "session_id": session})
		}
		if err == nil {
			err = rows.Err()
		}
		writeOrErr(w, map[string]any{"authorizations": out}, err)
		return
	}
	postBody(w, r, a.startBankAuthorization)
}

func queryArgs(r *http.Request) map[string]any {
	out := map[string]any{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func (a *App) handleAuthorizationBanks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	out, err := a.authorizationBanks(globalCtx, queryArgs(r))
	writeOrErr(w, out, err)
}
func (a *App) authorizationBanks(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	conn, err := financeConnection(ctx, "enable-banking", int64(intArg(args, "connection_id", 0)))
	if err != nil {
		return nil, err
	}
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country", "ES")))
	psu := strArg(args, "psu_type", "personal")
	if psu != "personal" && psu != "business" {
		return nil, errors.New("choose personal or business")
	}
	if len(country) != 2 {
		return nil, errors.New("choose a two-letter country code")
	}
	var raw map[string]any
	err = executeIntegrationJSON(ctx, conn.ID, "list_banks", map[string]any{"country": country, "service": "AIS", "psu_type": psu}, &raw)
	return raw, err
}
func (a *App) startBankAuthorization(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	conn, err := financeConnection(ctx, "enable-banking", int64(intArg(args, "connection_id", 0)))
	if err != nil {
		return nil, err
	}
	redirect, err := url.Parse(strArg(args, "redirect_url", ""))
	if err != nil || redirect.User != nil || redirect.Fragment != "" || redirect.Host == "" || (redirect.Scheme != "https" && !(redirect.Scheme == "http" && (redirect.Hostname() == "localhost" || redirect.Hostname() == "127.0.0.1" || redirect.Hostname() == "::1"))) {
		return nil, errors.New("use HTTPS for the callback, or HTTP on localhost for local development")
	}
	// The callback must return to Finance, never to a third-party code collector.
	if redirect.Path != "/api/apps/finance/banking/enable/callback" {
		return nil, errors.New("callback URL must use Finance's /api/apps/finance/banking/enable/callback path")
	}
	if redirect.Query().Get("project_id") != projectID(ctx) {
		return nil, errors.New("callback project does not match Finance")
	}
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.InstallID <= 0 || redirect.Query().Get("install_id") != strconv.FormatInt(identity.InstallID, 10) {
		return nil, errors.New("callback installation does not match Finance")
	}
	if redirect.Query().Has("code") || redirect.Query().Has("state") || redirect.Query().Has("error") {
		return nil, errors.New("callback URL must not contain authorization parameters")
	}
	var application map[string]any
	if err = executeIntegrationJSON(ctx, conn.ID, "get_application", nil, &application); err != nil {
		return nil, err
	}
	registered := false
	for _, v := range arrayAny(application["redirect_urls"]) {
		if stringAny(v) == redirect.String() {
			registered = true
			break
		}
	}
	if !registered {
		return nil, errors.New("register the exact Finance callback URL shown in Callback setup with your Enable Banking application")
	}
	raw, err := a.authorizationBanks(ctx, args)
	if err != nil {
		return nil, err
	}
	bank := strArg(args, "bank_name", "")
	country := strings.ToUpper(strArg(args, "country", "ES"))
	found := false
	seconds := float64(86400)
	for _, b := range mapsFromArray(arrayAny(asMap(raw)["aspsps"])) {
		if firstString(b, "name") == bank && firstString(b, "country") == country {
			found = true
			if n, ok := b["maximum_consent_validity"].(float64); ok && n > 0 {
				seconds = n
				if seconds > 90*86400 {
					seconds = 90 * 86400
				}
			}
			break
		}
	}
	if !found {
		return nil, errors.New("choose a bank from the current bank list")
	}
	id, state := uuid.NewString(), uuid.NewString()
	_, err = ctx.AppDB().Exec(`INSERT INTO bank_authorizations(id,project_id,connection_id,bank_name,country,state,status,expires_at) VALUES(?,?,?,?,?,?,'starting',?)`, id, projectID(ctx), conn.ID, bank, country, state, time.Now().Add(30*time.Minute).Unix())
	if err != nil {
		return nil, err
	}
	var response map[string]any
	err = executeIntegrationJSON(ctx, conn.ID, "start_authorization", map[string]any{"aspsp": map[string]any{"name": bank, "country": country}, "access": map[string]any{"valid_until": time.Now().Add(time.Duration(seconds) * time.Second).UTC().Format(time.RFC3339), "balances": true, "transactions": true}, "state": state, "redirect_url": redirect.String(), "psu_type": strArg(args, "psu_type", "personal")}, &response)
	if err != nil {
		ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='failed' WHERE id=?`, id)
		return nil, err
	}
	target, err := httpsURL(firstString(response, "url"))
	if err != nil || !(target.Hostname() == "enablebanking.com" || strings.HasSuffix(target.Hostname(), ".enablebanking.com")) {
		ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='failed' WHERE id=?`, id)
		return nil, errors.New("Enable Banking returned an invalid authorization URL")
	}
	_, err = ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='pending' WHERE id=?`, id)
	return map[string]any{"id": id, "url": target.String()}, err
}

func (a *App) completeBankAuthorization(ctx *sdk.AppCtx, state, code, providerError string) error {
	var id, status string
	var connID, expires int64
	err := ctx.AppDB().QueryRow(`SELECT id,connection_id,status,expires_at FROM bank_authorizations WHERE project_id=? AND state=?`, projectID(ctx), state).Scan(&id, &connID, &status, &expires)
	if err != nil || state == "" {
		return errors.New("this bank authorization was not found")
	}
	if status == "authorized" {
		return nil
	}
	if status != "pending" || expires < time.Now().Unix() {
		return errors.New("authorization expired or already processed; start Connect bank again")
	}
	if _, err = financeConnection(ctx, "enable-banking", connID); err != nil {
		return err
	}
	if providerError != "" {
		ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='denied' WHERE id=? AND status='pending'`, id)
		return errors.New("bank authorization was cancelled or declined; return to Finance to try again")
	}
	if code == "" {
		return errors.New("bank returned no authorization code")
	}
	res, err := ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='exchanging' WHERE id=? AND status='pending'`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("authorization is already being processed")
	}
	var response map[string]any
	if err = executeIntegrationJSON(ctx, connID, "authorize_session", map[string]any{"code": code}, &response); err != nil {
		ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='failed' WHERE id=?`, id)
		return errors.New("bank session could not be confirmed; return to Finance and reconnect")
	}
	session := firstString(response, "session_id")
	if session == "" {
		ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='failed' WHERE id=?`, id)
		return errors.New("bank returned no session; reconnect in Finance")
	}
	_, err = ctx.AppDB().Exec(`UPDATE bank_authorizations SET status='authorized',session_id=? WHERE id=?`, session, id)
	return err
}
func (a *App) handleBankAuthorizationCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	if err := a.completeBankAuthorization(globalCtx, r.URL.Query().Get("state"), r.URL.Query().Get("code"), r.URL.Query().Get("error")); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Write([]byte("Bank connected. You can close this tab and return to Finance. Your bank accounts are ready to discover and import."))
}

func (a *App) handleBankAuthorizationSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	conn, err := financeConnection(globalCtx, "enable-banking", int64(intArg(queryArgs(r), "connection_id", 0)))
	if err != nil {
		writeOrErr(w, nil, err)
		return
	}
	var raw map[string]any
	if err = executeIntegrationJSON(globalCtx, conn.ID, "get_application", nil, &raw); err != nil {
		writeOrErr(w, nil, err)
		return
	}
	identity, err := globalCtx.PlatformAPI().WhoAmI()
	if err != nil || identity == nil {
		writeOrErr(w, nil, errors.New("unable to determine Finance's public URL"))
		return
	}
	callback := ""
	if identity.PublicURL != "" {
		callback = strings.TrimRight(identity.PublicURL, "/") + "/api/apps/finance/banking/enable/callback?" + url.Values{"project_id": {projectID(globalCtx)}, "install_id": {strconv.FormatInt(identity.InstallID, 10)}}.Encode()
	}
	writeOrErr(w, map[string]any{"environment": raw["environment"], "redirect_urls": raw["redirect_urls"], "callback_url": callback}, nil)
}
