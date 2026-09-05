package main

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// This credential has authority only at /dns/<tenant>/mcp. The main SDK
// bearer cannot be derived from it, and live grants are checked on each call.
func (a *App) dnsToken(id string) (string, string, error) {
	var epoch int
	var project string
	if err := a.store.db.QueryRow(`SELECT dns_epoch,dns_project_id FROM fleet_tenant_state WHERE tenant_id=?`, id).Scan(&epoch, &project); err != nil {
		return "", "", err
	}
	return a.keys.deriveToken(fmt.Sprintf("fleet-dns:%s:%d", id, epoch)), project, nil
}

func (a *App) tenantDNSEnv(id string, hosted bool) ([]string, error) {
	if id == "" {
		return nil, nil
	}
	token, project, err := a.dnsToken(id)
	if err != nil {
		return nil, err
	}
	base := ""
	if hosted {
		base = strings.TrimRight(a.publicTransferBaseURL(globalCtx), "/")
	} else if port := os.Getenv("APTEVA_APP_PORT"); port != "" {
		base = "http://127.0.0.1:" + port
	}
	if base == "" {
		return nil, nil
	}
	if hosted && !strings.HasPrefix(base, "https://") {
		return nil, fmt.Errorf("hosted DNS delegation requires HTTPS")
	}
	return []string{"APTEVA_DELEGATED_DNS_FLEET_URL=" + base + "/dns/" + id, "APTEVA_DELEGATED_DNS_TOKEN=" + token, "APTEVA_DELEGATED_DNS_TENANT_ID=" + id, "APTEVA_DELEGATED_DNS_PROJECT_ID=" + project}, nil
}

func (a *App) httpDelegatedDNS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	id, tail, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/dns/"), "/")
	token, project, err := a.dnsToken(id)
	if err != nil || tail != "mcp" || !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) {
		http.Error(w, "unauthorized", 401)
		return
	}
	var req struct {
		ID     any    `json:"id"`
		Method string `json:"method"`
		Params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		} `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	args := req.Params.Arguments
	if args == nil {
		args = map[string]any{}
	}
	args["tenant_id"] = id
	args["_project_id"] = project
	var out any
	if req.Method != "tools/call" {
		err = fmt.Errorf("only tools/call is supported")
	} else {
		switch req.Params.Name {
		case "tenant_domain_list":
			var grants []*DomainGrant
			grants, err = a.store.listDomainGrants(id)
			out = map[string]any{"grants": grants}
		case "tenant_domain_record_set":
			out, err = a.toolDomainRecordSet(globalCtx, args)
		case "tenant_domain_record_delete":
			out, err = a.toolDomainRecordDelete(globalCtx, args)
		default:
			err = fmt.Errorf("DNS capability does not authorize this tool")
		}
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": err.Error()}})
		return
	}
	data, _ := json.Marshal(out)
	writeJSON(w, 200, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}}})
}

// Defense in depth for SDK versions that exempt arbitrary ?sig GETs.
func fleetAuthenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("APTEVA_APP_TOKEN")
		if token != "" && !hmac.Equal([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) {
			http.Error(w, "unauthorized", 401)
			return
		}
		next(w, r)
	}
}
