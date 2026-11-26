package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Every panel route requires a person: the platform proxy sets
// X-Apteva-Operator-ID from a validated session or user API key after
// stripping any client-supplied value, and the SDK only lets requests
// carrying this install's app token reach these handlers. Sibling apps
// calling through the platform carry an install identity, not an operator.
func operatorID(r *http.Request) (int64, error) {
	if os.Getenv("APTEVA_APP_TOKEN") == "" && os.Getenv("CRYPTO_WALLET_DEV_INSECURE") != "1" {
		return 0, errors.New("refusing panel requests: no app token, so the operator header cannot be trusted")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("X-Apteva-Operator-ID")), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("a signed-in person is required")
	}
	return id, nil
}

type handlerFunc func(r *http.Request, userID int64) (any, error)

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func (a *App) route(method, pattern string, h handlerFunc) sdk.Route {
	return sdk.Route{Method: method, Pattern: pattern, Handler: func(w http.ResponseWriter, r *http.Request) {
		userID, err := operatorID(r)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error()})
			return
		}
		out, err := h(r, userID)
		if err != nil {
			status := http.StatusBadRequest
			var he *httpError
			if errors.As(err, &he) {
				status = he.status
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, out)
	}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readBody(r *http.Request) (map[string]any, error) {
	body := map[string]any{}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return body, nil
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, errors.New("invalid JSON body")
	}
	return body, nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, &httpError{http.StatusNotFound, "invalid id"}
	}
	return id, nil
}

func reqCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 45*time.Second)
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		a.route("GET", "/api/overview", a.httpOverview),
		a.route("POST", "/api/master-key/generate", a.httpGenerateMasterKey),
		a.route("POST", "/api/wallets", a.httpCreateWallet),
		a.route("GET", "/api/wallets/{id}", a.httpWallet),
		a.route("GET", "/api/wallets/{id}/balances", a.httpBalances),
		a.route("POST", "/api/wallets/{id}/policy", a.httpSetPolicy),
		a.route("POST", "/api/wallets/{id}/freeze", a.httpFreeze),
		a.route("POST", "/api/wallets/{id}/recipients", a.httpAddRecipient),
		a.route("POST", "/api/wallets/{id}/export", a.httpExport),
		a.route("POST", "/api/recipients/{id}/decide", a.httpDecideRecipient),
		a.route("DELETE", "/api/recipients/{id}", a.httpRemoveRecipient),
		a.route("POST", "/api/proposals/{id}/decide", a.httpDecideProposal),
	}
}

func (a *App) httpOverview(r *http.Request, _ int64) (any, error) {
	wallets, err := listWallets(a.db)
	if err != nil {
		return nil, err
	}
	type row struct {
		*Wallet
		ChainName string `json:"chain_name"`
		Testnet   bool   `json:"testnet"`
		Pending   int    `json:"pending"`
	}
	out := []row{}
	for _, w := range wallets {
		chain, _ := chainBySlug(w.Chain)
		n, _ := countPending(a.db, w.ID)
		out = append(out, row{Wallet: w, ChainName: chain.Name, Testnet: chain.Testnet, Pending: n})
	}
	chainList := []map[string]any{}
	for _, slug := range []string{"base-sepolia", "base"} {
		c := chains[slug]
		chainList = append(chainList, map[string]any{"slug": c.Slug, "name": c.Name, "testnet": c.Testnet})
	}
	return map[string]any{
		"master_key_configured": a.keys.configured(),
		"wallets":               out,
		"chains":                chainList,
		"default_policy":        defaultPolicy(),
	}, nil
}

// httpGenerateMasterKey returns a fresh random key for the person to paste
// into the app settings. It is not stored anywhere by this app.
func (a *App) httpGenerateMasterKey(*http.Request, int64) (any, error) {
	if a.keys.configured() {
		return nil, errors.New("a master key is already configured; replacing it would lock existing wallets")
	}
	k, err := generateMasterKey()
	if err != nil {
		return nil, err
	}
	return map[string]any{"master_key": k}, nil
}

func (a *App) httpCreateWallet(r *http.Request, userID int64) (any, error) {
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	if projectID == "" && a.ctx != nil {
		projectID = a.ctx.CurrentProject()
	}
	return a.createWallet(projectID, strArg(body, "name"), strArg(body, "chain"), userID)
}

func (a *App) httpWallet(r *http.Request, _ int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	w, err := getWallet(a.db, id)
	if err != nil {
		return nil, &httpError{http.StatusNotFound, err.Error()}
	}
	chain, _ := chainBySlug(w.Chain)
	pol, err := currentPolicy(a.db, id)
	if err != nil {
		return nil, err
	}
	rcpts, err := listRecipients(a.db, id)
	if err != nil {
		return nil, err
	}
	props, err := listProposals(a.db, id, nil, 50)
	if err != nil {
		return nil, err
	}
	txs, err := listTransactions(a.db, id, 50)
	if err != nil {
		return nil, err
	}
	a.decorateTxs(w, txs)
	ledger, err := listLedger(a.db, id, 100)
	if err != nil {
		return nil, err
	}
	spent, err := spentSince(a.db, id, a.now().Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"wallet": w, "chain": chain, "policy": pol, "recipients": rcpts, "proposals": props,
		"transactions": txs, "ledger": ledger, "spent_24h_usd_micros": spent,
	}, nil
}

func (a *App) httpBalances(r *http.Request, _ int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	w, err := getWallet(a.db, id)
	if err != nil {
		return nil, &httpError{http.StatusNotFound, err.Error()}
	}
	ctx, cancel := reqCtx(r)
	defer cancel()
	bals, err := a.balances(ctx, w)
	if err != nil {
		return nil, &httpError{http.StatusBadGateway, err.Error()}
	}
	return map[string]any{"balances": bals}, nil
}

// dollarsToMicros reads a USD amount the panel sends as a number or string.
func dollarsToMicros(body map[string]any, key string) (int64, error) {
	s := strArg(body, key)
	if s == "" {
		return 0, errors.New(key + " required")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, errors.New(key + " must be a dollar amount")
	}
	return int64(math.Round(f * 1_000_000)), nil
}

func (a *App) httpSetPolicy(r *http.Request, userID int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var p Policy
	if p.PerTxUSDMicros, err = dollarsToMicros(body, "per_tx_usd"); err != nil {
		return nil, err
	}
	if p.DailyUSDMicros, err = dollarsToMicros(body, "daily_usd"); err != nil {
		return nil, err
	}
	if p.ApprovalUSDMicros, err = dollarsToMicros(body, "approval_usd"); err != nil {
		return nil, err
	}
	p.NewRecipientDelayHours = int(int64Arg(body, "new_recipient_delay_hours"))
	if raw, ok := body["tokens"].([]any); ok {
		for _, t := range raw {
			if s, ok := t.(string); ok {
				p.Tokens = append(p.Tokens, s)
			}
		}
	}
	return a.setPolicy(id, p, userID)
}

func (a *App) httpFreeze(r *http.Request, userID int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	frozen, _ := body["frozen"].(bool)
	return a.setFrozen(id, frozen, clip(strArg(body, "reason"), 200), userActor(userID))
}

func (a *App) httpAddRecipient(r *http.Request, userID int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	return a.addRecipient(id, strArg(body, "address"), clip(strArg(body, "label"), 80), userID)
}

func (a *App) httpExport(r *http.Request, userID int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	key, err := a.exportKey(id, strArg(body, "confirm_address"), userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"private_key": key}, nil
}

func decisionArg(body map[string]any) (bool, error) {
	switch strings.ToLower(strArg(body, "decision")) {
	case "approve":
		return true, nil
	case "reject":
		return false, nil
	}
	return false, errors.New("decision must be approve or reject")
}

func (a *App) httpDecideRecipient(r *http.Request, userID int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	approve, err := decisionArg(body)
	if err != nil {
		return nil, err
	}
	return a.decideRecipient(id, approve, userActor(userID), "panel", strArg(body, "note"), userID)
}

func (a *App) httpRemoveRecipient(r *http.Request, userID int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, a.removeRecipient(id, userID)
}

func (a *App) httpDecideProposal(r *http.Request, userID int64) (any, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	body, err := readBody(r)
	if err != nil {
		return nil, err
	}
	approve, err := decisionArg(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := reqCtx(r)
	defer cancel()
	return a.decideProposal(ctx, id, approve, userActor(userID), "panel", strArg(body, "note"), userID)
}
