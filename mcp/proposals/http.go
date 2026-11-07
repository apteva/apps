package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const maxBody = 512 << 10

//go:embed ui/offer.html
var offerHTML []byte

//go:embed ui/offer.js
var offerJS []byte

//go:embed ui/offer.css
var offerCSS []byte

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/proposals", Handler: a.handlePanel},
		{Pattern: "/proposals/", Handler: a.handlePanel},
		{Pattern: "/offer/", Handler: a.handleOffer, NoAuth: true},
	}
}

func (a *App) handlePanel(w http.ResponseWriter, r *http.Request) {
	if project := r.URL.Query().Get("project_id"); project != "" && project != a.project {
		writeError(w, errNotFound)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/proposals/")
	if name == "clients_resources" && r.Method == http.MethodGet {
		var op operation
		for _, candidate := range operations {
			if candidate.name == "clients_list" {
				op = candidate
				break
			}
		}
		result, err := a.execute(r.Context(), op, input{})
		if err != nil {
			writeError(w, err)
			return
		}
		items := []map[string]any{}
		for _, c := range result.(map[string]any)["clients"].([]client) {
			items = append(items, map[string]any{"id": strconv.FormatInt(c.ID, 10), "label": c.Name})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	if name == "resources" && r.Method == http.MethodGet {
		rows, err := a.store.list(r.Context(), a.project, 0, 100)
		if err != nil {
			writeError(w, err)
			return
		}
		items := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			items = append(items, map[string]any{"id": strconv.FormatInt(row.ProposalID, 10), "label": row.Content.Title})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	var in input
	if r.URL.Path == "/proposals" && r.Method == http.MethodGet {
		name = "list"
		in.Before, _ = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		in.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	} else if name == "get" && r.Method == http.MethodGet {
		in.ProposalID, _ = strconv.ParseInt(r.URL.Query().Get("proposal_id"), 10, 64)
	} else if r.Method == http.MethodPost {
		if err := decodeBody(w, r, &in); err != nil {
			writeError(w, err)
			return
		}
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	for _, op := range operations {
		if op.name == name {
			result, err := a.execute(r.Context(), op, in)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
	}
	writeError(w, errNotFound)
}

type offerRequest struct {
	Token     string    `json:"token"`
	Outcome   string    `json:"outcome,omitempty"`
	Name      string    `json:"name,omitempty"`
	Note      string    `json:"note,omitempty"`
	Selection selection `json:"selection,omitempty"`
}

type offerView struct {
	Language  string    `json:"language"`
	State     string    `json:"state"`
	Number    int       `json:"number,omitempty"`
	Content   *content  `json:"content,omitempty"`
	ExpiresAt int64     `json:"expires_at,omitempty"`
	Response  *response `json:"response,omitempty"`
}

func publicView(r revision) offerView {
	if r.State != "published" && r.Response == nil {
		return offerView{State: r.State, Language: r.Content.Language}
	}
	r.Content.ClientEmail = ""
	r.Content.OpportunityID = 0
	r.Content.ClientID = 0
	r.Content.Categories = append([]category(nil), r.Content.Categories...)
	for i := range r.Content.Categories {
		r.Content.Categories[i].Items = append([]item(nil), r.Content.Categories[i].Items...)
		for j := range r.Content.Categories[i].Items {
			r.Content.Categories[i].Items[j].BacklogItemID = 0
		}
	}
	return offerView{State: r.State, Language: r.Content.Language, Number: r.Number, Content: &r.Content, ExpiresAt: r.ExpiresAt, Response: r.Response}
}

func (a *App) handleOffer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	if r.URL.Query().Get("project_id") != a.project {
		writeError(w, errNotFound)
		return
	}

	switch r.Method + " " + r.URL.Path {
	case "GET /offer/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		query := html.EscapeString("?" + url.Values{"project_id": {a.project}}.Encode())
		page := strings.NewReplacer("client.css", "client.css"+query, "client.js", "client.js"+query).Replace(string(offerHTML))
		_, _ = io.WriteString(w, page)
	case "GET /offer/client.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(offerJS)
	case "GET /offer/client.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(offerCSS)
	case "POST /offer/view", "POST /offer/respond":
		var in offerRequest
		if err := decodeBody(w, r, &in); err != nil {
			writeError(w, err)
			return
		}
		ctx := r.Context()
		var result revision
		var err error
		if r.URL.Path == "/offer/view" {
			result, err = a.store.offer(ctx, a.store.db, a.project, in.Token)
		} else {
			result, err = a.store.respond(ctx, a.project, in.Token, in.Outcome, in.Name, in.Note, in.Selection)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, publicView(result))
	default:
		writeError(w, errNotFound)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return invalid("expected application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return invalid("invalid or oversized JSON body")
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return invalid("expected a single JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

func writeError(w http.ResponseWriter, err error) {
	var badInput inputError
	status := http.StatusInternalServerError
	message := "The proposal could not be processed. Try again."
	switch {
	case errors.Is(err, errNotFound):
		status, message = http.StatusNotFound, "This proposal link or record could not be found."
	case errors.Is(err, errConflict):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, errForbidden):
		status, message = http.StatusForbidden, err.Error()
	case errors.Is(err, errIntegration):
		status, message = http.StatusBadGateway, err.Error()
	case errors.As(err, &badInput):
		status, message = http.StatusBadRequest, err.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}
