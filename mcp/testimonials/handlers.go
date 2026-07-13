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
		limit, err := queryInt(r, "limit")
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		offset, err := queryInt(r, "offset")
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		publishedOnly := queryBool(r, "published_only")
		items, total, err := listTestimonialsPage(app.AppDB(), app.CurrentProject(), TestimonialFilter{
			Status:          r.URL.Query().Get("status"),
			Kind:            r.URL.Query().Get("kind"),
			Source:          r.URL.Query().Get("source"),
			Tag:             r.URL.Query().Get("tag"),
			Q:               r.URL.Query().Get("q"),
			PublishedOnly:   publishedOnly,
			IncludeArchived: queryBool(r, "include_archived"),
			Limit:           limit,
			Offset:          offset,
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		var responseItems any = items
		if publishedOnly {
			responseItems = publicTestimonials(items)
		}
		httpJSON(w, map[string]any{
			"testimonials": responseItems,
			"total":        total,
			"limit":        normalizedLimit(limit),
			"offset":       offset,
			"has_more":     offset+len(items) < total,
		})
	case http.MethodPost:
		var t Testimonial
		if err := decodeJSON(w, r, &t); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		created, err := createTestimonial(app.AppDB(), app.CurrentProject(), &t)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		app.Emit("testimonial.created", map[string]any{"id": created.ID, "status": created.Status, "kind": created.Kind})
		httpJSONStatus(w, http.StatusCreated, map[string]any{"testimonial": created})
	default:
		methodNotAllowed(w, "GET, POST")
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
			if err := decodeJSON(w, r, &fields); err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			before, err := getTestimonial(app.AppDB(), app.CurrentProject(), id)
			if err != nil {
				writeStoreErr(w, err, "testimonial not found")
				return
			}
			if before == nil {
				httpErr(w, http.StatusNotFound, "testimonial not found")
				return
			}
			item, err := updateTestimonial(app.AppDB(), app.CurrentProject(), id, fields)
			if err != nil {
				writeStoreErr(w, err, "testimonial not found")
				return
			}
			app.Emit("testimonial.updated", map[string]any{"id": item.ID, "status": item.Status, "kind": item.Kind})
			if before.Status != item.Status {
				app.Emit("testimonial.status_changed", map[string]any{"id": item.ID, "status": item.Status})
			}
			httpJSON(w, map[string]any{"testimonial": item})
		case http.MethodDelete:
			hard := r.URL.Query().Get("hard") == "1"
			if err := deleteTestimonial(app.AppDB(), app.CurrentProject(), id, hard); err != nil {
				writeStoreErr(w, err, "testimonial not found")
				return
			}
			app.Emit("testimonial.deleted", map[string]any{"id": id, "hard": hard})
			if !hard {
				app.Emit("testimonial.status_changed", map[string]any{"id": id, "status": "archived"})
			}
			httpJSON(w, map[string]any{"deleted": hard, "archived": !hard, "id": id})
		default:
			methodNotAllowed(w, "GET, PATCH, DELETE")
		}
	case "status":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return
		}
		var body struct {
			Status string `json:"status"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
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

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return errors.New("invalid json: " + err.Error())
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("invalid json: expected one object")
		}
		return errors.New("invalid json: " + err.Error())
	}
	return nil
}

func httpJSON(w http.ResponseWriter, v any) {
	httpJSONStatus(w, http.StatusOK, v)
}

func httpJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func queryBool(r *http.Request, key string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

func queryInt(r *http.Request, key string) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New(key + " must be an integer")
	}
	return value, nil
}

func normalizedLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}

func writeStoreErr(w http.ResponseWriter, err error, notFound string) {
	if errors.Is(err, errNotFound) {
		httpErr(w, http.StatusNotFound, notFound)
		return
	}
	httpErr(w, http.StatusBadRequest, err.Error())
}
