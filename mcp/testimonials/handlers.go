package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) handleTestimonialsCollection(w http.ResponseWriter, r *http.Request) {
	app := ctxForRequest(r)
	switch r.Method {
	case http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		items, err := listTestimonials(app.AppDB(), app.CurrentProject(), TestimonialFilter{
			Status:        r.URL.Query().Get("status"),
			Kind:          r.URL.Query().Get("kind"),
			Source:        r.URL.Query().Get("source"),
			Tag:           r.URL.Query().Get("tag"),
			Q:             r.URL.Query().Get("q"),
			PublishedOnly: r.URL.Query().Get("published_only") == "1",
			Limit:         limit,
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, map[string]any{"testimonials": items})
	case http.MethodPost:
		var t Testimonial
		if err := decodeJSON(r, &t); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		created, err := createTestimonial(app.AppDB(), app.CurrentProject(), &t)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		app.Emit("testimonial.created", map[string]any{"id": created.ID, "status": created.Status, "kind": created.Kind})
		w.WriteHeader(http.StatusCreated)
		httpJSON(w, map[string]any{"testimonial": created})
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleTestimonialsItem(w http.ResponseWriter, r *http.Request) {
	app := ctxForRequest(r)
	rest := strings.TrimPrefix(r.URL.Path, "/testimonials/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid testimonial id")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch tail {
	case "":
		switch r.Method {
		case http.MethodGet:
			item, err := getTestimonial(app.AppDB(), app.CurrentProject(), id)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if item == nil {
				httpErr(w, http.StatusNotFound, "testimonial not found")
				return
			}
			httpJSON(w, map[string]any{"testimonial": item})
		case http.MethodPatch:
			var fields map[string]any
			if err := decodeJSON(r, &fields); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			item, err := updateTestimonial(app.AppDB(), app.CurrentProject(), id, fields)
			if err != nil {
				writeStoreErr(w, err, "testimonial not found")
				return
			}
			app.Emit("testimonial.updated", map[string]any{"id": item.ID, "status": item.Status, "kind": item.Kind})
			httpJSON(w, map[string]any{"testimonial": item})
		case http.MethodDelete:
			hard := r.URL.Query().Get("hard") == "1"
			if err := deleteTestimonial(app.AppDB(), app.CurrentProject(), id, hard); err != nil {
				writeStoreErr(w, err, "testimonial not found")
				return
			}
			app.Emit("testimonial.deleted", map[string]any{"id": id, "hard": hard})
			httpJSON(w, map[string]any{"deleted": hard, "archived": !hard, "id": id})
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	case "status":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body struct {
			Status string `json:"status"`
		}
		if err := decodeJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		item, err := setTestimonialStatus(app.AppDB(), app.CurrentProject(), id, body.Status)
		if err != nil {
			writeStoreErr(w, err, "testimonial not found")
			return
		}
		app.Emit("testimonial.status_changed", map[string]any{"id": item.ID, "status": item.Status})
		httpJSON(w, map[string]any{"testimonial": item})
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func ctxForRequest(r *http.Request) *sdk.AppCtx {
	if globalCtx == nil {
		return nil
	}
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		return globalCtx.WithProject(projectID)
	}
	return globalCtx
}

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	dec.UseNumber()
	err := dec.Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return errors.New("invalid json: " + err.Error())
	}
	return nil
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error, notFound string) {
	if errors.Is(err, errNotFound) {
		httpErr(w, http.StatusNotFound, notFound)
		return
	}
	httpErr(w, http.StatusBadRequest, err.Error())
}
