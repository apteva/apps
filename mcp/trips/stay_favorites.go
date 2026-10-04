package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Favourites remain planning options until explicitly selected. One top choice
// per destination; one itinerary accommodation per favourite, even on retries.
type StayFavorite struct {
	ID              int64  `json:"id"`
	TripID          int64  `json:"trip_id"`
	DestinationID   int64  `json:"destination_id"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	ListingURL      string `json:"listing_url"`
	PhotoURL        string `json:"photo_url"`
	Address         string `json:"address"`
	PriceAmount     *int64 `json:"price_amount"`
	PriceBasis      string `json:"price_basis"`
	Currency        string `json:"currency"`
	Notes           string `json:"notes"`
	TopChoice       bool   `json:"top_choice"`
	AccommodationID *int64 `json:"accommodation_id,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

const favoriteSelect = `SELECT f.id, f.trip_id, f.destination_id, f.name, f.kind,
 f.listing_url, f.photo_url, f.address, f.price_amount, f.price_basis, f.currency,
 f.notes, f.top_choice, a.id, f.created_at, f.updated_at
 FROM stay_favorites f JOIN trips t ON t.id=f.trip_id
 LEFT JOIN accommodations a ON a.stay_favorite_id=f.id`

func scanFavorite(row rowScanner) (StayFavorite, error) {
	var f StayFavorite
	var amount, accommodation sql.NullInt64
	err := row.Scan(&f.ID, &f.TripID, &f.DestinationID, &f.Name, &f.Kind,
		&f.ListingURL, &f.PhotoURL, &f.Address, &amount, &f.PriceBasis, &f.Currency,
		&f.Notes, &f.TopChoice, &accommodation, &f.CreatedAt, &f.UpdatedAt)
	if amount.Valid {
		f.PriceAmount = &amount.Int64
	}
	if accommodation.Valid {
		f.AccommodationID = &accommodation.Int64
	}
	return f, err
}

func readFavorite(ctx *sdk.AppCtx, id int64) (StayFavorite, error) {
	return scanFavorite(ctx.AppDB().QueryRow(favoriteSelect+` WHERE f.id=? AND t.project_id=?`, id, projectID()))
}

func listFavorites(ctx *sdk.AppCtx, tripID, destinationID int64) ([]StayFavorite, error) {
	rows, err := ctx.AppDB().Query(favoriteSelect+` WHERE t.project_id=? AND f.trip_id=?
 AND (?=0 OR f.destination_id=?) ORDER BY f.destination_id, f.top_choice DESC, f.id`,
		projectID(), tripID, destinationID, destinationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StayFavorite{}
	for rows.Next() {
		f, err := scanFavorite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func favoriteInteger(value any, field string) (int64, error) {
	var n int64
	switch v := value.(type) {
	case int:
		n = int64(v)
	case int64:
		n = v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || math.Abs(v) > 9007199254740991 {
			return 0, fmt.Errorf("%s must be an integer in minor units", field)
		}
		n = int64(v)
	default:
		return 0, fmt.Errorf("%s must be an integer", field)
	}
	if n < 0 || n > 9007199254740991 {
		return 0, fmt.Errorf("%s must be non-negative and within the supported range", field)
	}
	return n, nil
}

func favoriteID(args map[string]any, field string) (int64, error) {
	id, err := favoriteInteger(args[field], field)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}
	return id, nil
}

func favoriteDestination(ctx *sdk.AppCtx, id int64) (Destination, Trip, error) {
	d, err := readDestination(ctx, id)
	if err != nil {
		return d, Trip{}, errors.New("destination not found")
	}
	t, err := readTrip(ctx, d.TripID)
	if err != nil || t.ProjectID != projectID() {
		return d, t, errors.New("destination not found")
	}
	return d, t, nil
}

func (a *App) toolStayFavoritesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var tripID, destID int64
	var err error
	if v, ok := args["trip_id"]; ok {
		tripID, err = favoriteInteger(v, "trip_id")
		if err != nil {
			return nil, err
		}
	}
	if v, ok := args["destination_id"]; ok {
		destID, err = favoriteInteger(v, "destination_id")
		if err != nil {
			return nil, err
		}
	}
	if destID != 0 {
		_, trip, err := favoriteDestination(ctx, destID)
		if err != nil {
			return nil, err
		}
		if tripID != 0 && tripID != trip.ID {
			return nil, errors.New("destination does not belong to trip")
		}
		tripID = trip.ID
	}
	if tripID == 0 {
		return nil, errors.New("trip_id or destination_id required")
	}
	trip, err := readTrip(ctx, tripID)
	if err != nil || trip.ProjectID != projectID() {
		return nil, errors.New("trip not found")
	}
	favorites, err := listFavorites(ctx, tripID, destID)
	return map[string]any{"favorites": favorites}, err
}

func applyFavoriteFields(f *StayFavorite, args map[string]any) error {
	for _, field := range []string{"name", "kind", "listing_url", "photo_url", "address", "price_basis", "currency", "notes"} {
		value, ok := args[field]
		if !ok {
			continue
		}
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", field)
		}
		s = strings.TrimSpace(s)
		switch field {
		case "name":
			f.Name = s
		case "kind":
			f.Kind = s
		case "listing_url", "photo_url":
			if s != "" {
				u, err := url.Parse(s)
				if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
					return fmt.Errorf("%s must be an http or https URL", field)
				}
			}
			if field == "listing_url" {
				f.ListingURL = s
			} else {
				f.PhotoURL = s
			}
		case "address":
			f.Address = s
		case "price_basis":
			f.PriceBasis = s
		case "currency":
			f.Currency = s
		case "notes":
			f.Notes = s
		}
	}
	if v, ok := args["price_amount"]; ok {
		f.PriceAmount = nil
		if v != nil {
			amount, err := favoriteInteger(v, "price_amount")
			if err != nil {
				return err
			}
			f.PriceAmount = &amount
		}
	}
	if v, ok := args["top_choice"]; ok {
		b, ok := v.(bool)
		if !ok {
			return errors.New("top_choice must be a boolean")
		}
		f.TopChoice = b
	}
	if f.Name == "" {
		return errors.New("name required")
	}
	if !contains(accommodationKinds(), f.Kind) {
		return errors.New("invalid accommodation kind")
	}
	if f.PriceBasis != "total" && f.PriceBasis != "per_night" {
		return errors.New("price_basis must be total or per_night")
	}
	currency, err := normalizeCurrency(f.Currency)
	if err != nil {
		return err
	}
	f.Currency = currency
	return nil
}

func (a *App) toolStayFavoritesAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := favoriteID(args, "destination_id")
	if err != nil {
		return nil, err
	}
	_, trip, err := favoriteDestination(ctx, id)
	if err != nil {
		return nil, err
	}
	f := StayFavorite{TripID: trip.ID, DestinationID: id, Kind: "hotel", PriceBasis: "total", Currency: trip.HomeCurrency}
	if err := applyFavoriteFields(&f, args); err != nil {
		return nil, err
	}
	return saveFavorite(ctx, f)
}

func (a *App) toolStayFavoritesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := favoriteID(args, "id")
	if err != nil {
		return nil, err
	}
	f, err := readFavorite(ctx, id)
	if err != nil {
		return nil, errors.New("favorite not found")
	}
	if err := applyFavoriteFields(&f, args); err != nil {
		return nil, err
	}
	// Destination ownership is immutable: a shortlist belongs to one trip stop.
	if _, supplied := args["destination_id"]; supplied {
		return nil, errors.New("destination_id cannot be changed")
	}
	return saveFavorite(ctx, f)
}

func saveFavorite(ctx *sdk.AppCtx, f StayFavorite) (StayFavorite, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return f, err
	}
	defer tx.Rollback()
	if f.TopChoice {
		if _, err := tx.Exec(`UPDATE stay_favorites SET top_choice=0, updated_at=CURRENT_TIMESTAMP WHERE destination_id=? AND top_choice=1`, f.DestinationID); err != nil {
			return f, err
		}
	}
	values := []any{f.Name, f.Kind, f.ListingURL, f.PhotoURL, f.Address, f.PriceAmount, f.PriceBasis, f.Currency, f.Notes, f.TopChoice}
	if f.ID == 0 {
		result, err := tx.Exec(`INSERT INTO stay_favorites(name,kind,listing_url,photo_url,address,price_amount,price_basis,currency,notes,top_choice,trip_id,destination_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, append(values, f.TripID, f.DestinationID)...)
		if err != nil {
			return f, err
		}
		f.ID, err = result.LastInsertId()
		if err != nil {
			return f, err
		}
	} else {
		if _, err := tx.Exec(`UPDATE stay_favorites SET name=?,kind=?,listing_url=?,photo_url=?,address=?,price_amount=?,price_basis=?,currency=?,notes=?,top_choice=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, append(values, f.ID)...); err != nil {
			return f, err
		}
	}
	if err := tx.Commit(); err != nil {
		return f, err
	}
	emitTripEvent(ctx, "stay_favorite.updated", f.TripID, "stay_favorite", f.ID, nil)
	return readFavorite(ctx, f.ID)
}

func (a *App) toolStayFavoritesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := favoriteID(args, "id")
	if err != nil {
		return nil, err
	}
	f, err := readFavorite(ctx, id)
	if err != nil {
		return nil, errors.New("favorite not found")
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM stay_favorites WHERE id=?`, id); err != nil {
		return nil, err
	}
	// ON DELETE SET NULL preserves a stay already chosen for the itinerary.
	emitTripEvent(ctx, "stay_favorite.deleted", f.TripID, "stay_favorite", id, nil)
	return map[string]any{"deleted": id}, nil
}

var favoriteSelectionMu sync.Mutex

func (a *App) toolStayFavoritesSelect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	favoriteSelectionMu.Lock()
	defer favoriteSelectionMu.Unlock()
	id, err := favoriteID(args, "id")
	if err != nil {
		return nil, err
	}
	f, err := readFavorite(ctx, id)
	if err != nil {
		return nil, errors.New("favorite not found")
	}
	if f.AccommodationID != nil {
		return readAccommodation(ctx, *f.AccommodationID)
	}
	d, _, err := favoriteDestination(ctx, f.DestinationID)
	if err != nil {
		return nil, err
	}
	start, end := "", ""
	if stayNights(d.ArriveAt, d.DepartAt) > 0 {
		start = d.ArriveAt[:10] + "T15:00:00Z"
		end = d.DepartAt[:10] + "T11:00:00Z"
	}
	for _, field := range []string{"check_in_at", "check_out_at"} {
		if v, ok := args[field]; ok {
			value, err := nullableDateString(v, field)
			if err != nil {
				return nil, err
			}
			if field == "check_in_at" {
				start = value
			} else {
				end = value
			}
		}
	}
	start, end, err = normalizeOptionalRange(start, end, "check_in_at", "check_out_at")
	if err != nil {
		return nil, err
	}
	var cost any
	if v, ok := args["cost_estimated"]; ok {
		if v != nil {
			cost, err = favoriteInteger(v, "cost_estimated")
			if err != nil {
				return nil, err
			}
		}
	} else if f.PriceAmount != nil {
		if f.PriceBasis == "total" {
			cost = *f.PriceAmount
		} else {
			if start == "" {
				return nil, errors.New("per-night prices require stay dates or an explicit cost_estimated (null for unknown)")
			}
			nights := stayNights(start, end)
			if nights <= 0 {
				return nil, errors.New("per-night prices require at least one night")
			}
			if *f.PriceAmount > 9007199254740991/nights {
				return nil, errors.New("total price exceeds the supported range")
			}
			cost = *f.PriceAmount * nights
		}
	}
	currency := f.Currency
	if value, ok := args["currency"]; ok {
		s, ok := value.(string)
		if !ok {
			return nil, errors.New("currency must be a string")
		}
		currency, err = normalizeCurrency(s)
		if err != nil {
			return nil, err
		}
		if currency != f.Currency {
			if _, supplied := args["cost_estimated"]; !supplied {
				return nil, errors.New("cost_estimated required when changing currency")
			}
		}
	}
	notes := f.Notes
	if f.ListingURL != "" {
		notes = strings.TrimSpace(notes + "\n" + f.ListingURL)
	}
	out, err := a.addAccommodation(ctx, map[string]any{"trip_id": f.TripID, "destination_id": f.DestinationID,
		"name": f.Name, "kind": f.Kind, "address": f.Address, "notes": notes,
		"check_in_at": start, "check_out_at": end, "cost_estimated": cost, "currency": currency}, f.ID)
	if err != nil {
		// A unique DB constraint protects selection across sidecar processes too.
		if current, readErr := readFavorite(ctx, id); readErr == nil && current.AccommodationID != nil {
			return readAccommodation(ctx, *current.AccommodationID)
		}
		return nil, err
	}
	emitTripEvent(ctx, "stay_favorite.selected", f.TripID, "stay_favorite", id, nil)
	return out, nil
}

func stayNights(start, end string) int64 {
	if len(start) < 10 || len(end) < 10 {
		return 0
	}
	s, err := time.Parse("2006-01-02", start[:10])
	if err != nil {
		return 0
	}
	e, err := time.Parse("2006-01-02", end[:10])
	if err != nil {
		return 0
	}
	return int64(e.Sub(s) / (24 * time.Hour))
}

func favoriteFields() map[string]any {
	return map[string]any{
		"name": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": accommodationKinds()},
		"listing_url": map[string]any{"type": "string"}, "photo_url": map[string]any{"type": "string"},
		"address": map[string]any{"type": "string"}, "notes": map[string]any{"type": "string"},
		"price_amount": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "description": "Estimate in currency minor units; null clears it."},
		"price_basis":  map[string]any{"type": "string", "enum": []string{"total", "per_night"}},
		"currency":     map[string]any{"type": "string"}, "top_choice": map[string]any{"type": "boolean"},
	}
}

func (a *App) favoriteTools() []sdk.Tool {
	add, update := favoriteFields(), favoriteFields()
	add["destination_id"] = map[string]any{"type": "integer"}
	update["id"] = map[string]any{"type": "integer"}
	return []sdk.Tool{
		{Name: "stay_favorites_list", Description: "List saved hotel/Airbnb options for a trip or destination. Shortlists do not affect budgets or calendars.", InputSchema: schemaObject(map[string]any{"trip_id": map[string]any{"type": "integer"}, "destination_id": map[string]any{"type": "integer"}}, nil), Handler: a.toolStayFavoritesList},
		{Name: "stay_favorites_add", Description: "Save a stay option for a destination within a trip. Prices are minor units. top_choice replaces the previous top choice for that destination.", InputSchema: schemaObject(add, []string{"destination_id", "name"}), Handler: a.toolStayFavoritesAdd},
		{Name: "stay_favorites_update", Description: "Edit a favourite; does not change any already selected itinerary accommodation.", InputSchema: schemaObject(update, []string{"id"}), Handler: a.toolStayFavoritesUpdate},
		{Name: "stay_favorites_delete", Description: "Remove a favourite, preserving any already selected itinerary stay.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolStayFavoritesDelete},
		{Name: "stay_favorites_select", Description: "Use an option as an unbooked itinerary stay. Defaults dates from destination and calculates per-night totals. Optional date/price overrides; null dates save an idea. Repeated selection returns the existing stay without changes. Alternatives remain saved.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"}, "check_in_at": map[string]any{"type": []string{"string", "null"}}, "check_out_at": map[string]any{"type": []string{"string", "null"}}, "cost_estimated": map[string]any{"type": []string{"integer", "null"}, "minimum": 0}, "currency": map[string]any{"type": "string"},
		}, []string{"id"}), Handler: a.toolStayFavoritesSelect},
	}
}

func (a *App) handleStayFavorites(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		favoriteBody(w, r, a.toolStayFavoritesAdd, 0)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	args := map[string]any{}
	for _, field := range []string{"trip_id", "destination_id"} {
		if value := r.URL.Query().Get(field); value != "" {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				http.Error(w, "invalid "+field, http.StatusBadRequest)
				return
			}
			args[field] = id
		}
	}
	out, err := a.toolStayFavoritesList(globalCtx, args)
	writeOrErr(w, out, err)
}

func (a *App) handleStayFavoritesItem(w http.ResponseWriter, r *http.Request) {
	selecting := strings.HasSuffix(r.URL.Path, "/select")
	path := r.URL.Path
	if selecting {
		path = strings.TrimSuffix(path, "/select")
	}
	id, ok := pathID(path, "/stay-favorites/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if selecting {
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		favoriteBody(w, r, a.toolStayFavoritesSelect, id)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := readFavorite(globalCtx, id)
		writeOrErr(w, out, err)
	case http.MethodPatch:
		favoriteBody(w, r, a.toolStayFavoritesUpdate, id)
	case http.MethodDelete:
		_, err := a.toolStayFavoritesDelete(globalCtx, map[string]any{"id": id})
		if err != nil {
			writeOrErr(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func favoriteBody(w http.ResponseWriter, r *http.Request, handler func(*sdk.AppCtx, map[string]any) (any, error), id int64) {
	args := map[string]any{}
	if !decodeRequestBody(w, r, &args) {
		return
	}
	if id != 0 {
		args["id"] = id
	}
	out, err := handler(globalCtx, args)
	writeOrErr(w, out, err)
}
