package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: places
display_name: Places
version: 0.1.1
description: Personal map of restaurants, bars, hotels and spots to try or loved.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app, platform.connections.execute, platform.connections.manage]
  integrations:
    - role: google-places
      kind: integration
      required: false
      compatible_slugs: [google-places]
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: places_add,          description: "Save a place from a name, a Google Maps link or coordinates." }
    - { name: places_lookup,       description: "Look up candidate places without saving." }
    - { name: places_search,       description: "Search saved places." }
    - { name: places_get,          description: "Read one place with visits and lists." }
    - { name: places_update,       description: "Patch a place." }
    - { name: places_delete,       description: "Delete a place." }
    - { name: places_log_visit,    description: "Log a visit to a place." }
    - { name: places_delete_visit, description: "Delete a logged visit." }
    - { name: lists_list,          description: "List place lists." }
    - { name: lists_create,        description: "Create a list." }
    - { name: lists_update,        description: "Rename or describe a list." }
    - { name: lists_delete,        description: "Delete a list (places are kept)." }
    - { name: lists_add,           description: "Add places to a list." }
    - { name: lists_remove,        description: "Remove places from a list." }
    - { name: places_import,       description: "Import Google Takeout saved places, GeoJSON or CSV." }
    - { name: places_export,       description: "Export places as GeoJSON or CSV." }
    - { name: settings_get,        description: "Read Places settings." }
    - { name: settings_set,        description: "Update Places settings." }
    - { name: connections_list,    description: "List Google Places connections." }
  ui_panels:
    - slot: project.page
      label: Places
      icon: map-pin
      entry: /ui/PlacesPanel.mjs
  publishes:
    - { name: place.added,   description: "A place was saved." }
    - { name: place.updated, description: "A place was updated." }
    - { name: place.visited, description: "A visit was logged." }
    - { name: place.deleted, description: "A place was deleted." }
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/places }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/places.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct {
	// newGeocoder is swappable in tests.
	newGeocoder func(ctx *sdk.AppCtx, s Settings) geocoder
}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("places requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("places mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/call/", Handler: a.handleCall},
		{Pattern: "/export", Handler: a.handleExport},
	}
}

// ─── geocoder selection ──────────────────────────────────────────

type platformExecutor struct{ ctx *sdk.AppCtx }

func (p platformExecutor) Execute(connID int64, tool string, input map[string]any) (json.RawMessage, error) {
	res, err := p.ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return nil, fmt.Errorf("google places %s failed: %w", tool, err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("google places %s failed: %s", tool, body)
	}
	return res.Data, nil
}

func (a *App) geocoderFor(ctx *sdk.AppCtx, s Settings) geocoder {
	if a.newGeocoder != nil {
		return a.newGeocoder(ctx, s)
	}
	if s.PlacesConnectionID > 0 {
		return googleGeocoder{exec: platformExecutor{ctx: ctx}, connID: s.PlacesConnectionID}
	}
	return nominatimGeocoder{}
}

// ─── tools ───────────────────────────────────────────────────────

type toolFn = sdk.ToolHandler

func (a *App) MCPTools() []sdk.Tool {
	statusDesc := "to_try | visited | favorite | closed"
	kindDesc := "restaurant, cafe, bar, bakery, hotel, museum, attraction, viewpoint, beach, park, hike, shop, market, venue, other (free text allowed)"
	return []sdk.Tool{
		{
			Name: "places_add",
			Description: "Save a place to the personal map. Give a name (plus city for a better match), a Google Maps link (url), or lat/lng. " +
				"With lookup (default true) the app finds address, coordinates, price and Google id via Google Places (if a connection is set) or OpenStreetMap. " +
				"Duplicates are detected (same Google id, or same name within 150 m) and the existing place is returned with duplicate=true unless allow_duplicate. " +
				"Kinds: " + kindDesc + ". Status: " + statusDesc + " (default to_try).",
			InputSchema: schemaObject(map[string]any{
				"name": sString(), "url": sStringDesc("Google Maps link (full or maps.app.goo.gl)"),
				"city": sString(), "country": sString(), "address": sString(),
				"lat": sNumber(), "lng": sNumber(), "kind": sString(), "status": sString(),
				"tags": sArray("string"), "notes": sString(), "source_url": sStringDesc("Where you heard about it (article, Instagram, TikTok)"),
				"my_rating": sIntegerDesc("1-10"), "price_level": sIntegerDesc("0-4"),
				"list":            sStringDesc("List name to add it to (created if missing)"),
				"google_place_id": sString(), "lookup": sBool(), "allow_duplicate": sBool(),
			}, nil),
			Handler: a.toolAdd,
		},
		{
			Name:        "places_lookup",
			Description: "Look up candidate places by text without saving them (Google Places if configured, else OpenStreetMap). Each candidate says whether it's already saved (saved_place_id). Args: query, city?, kind?, near_lat?, near_lng?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"query": sString(), "city": sString(), "kind": sString(),
				"near_lat": sNumber(), "near_lng": sNumber(), "limit": sInteger(),
			}, []string{"query"}),
			Handler: a.toolLookup,
		},
		{
			Name: "places_search",
			Description: "Search saved places. Filters: q (name/notes/address/tags text), kind, status (" + statusDesc + ", 'active' = all but closed [default], 'all'), tag, city, list_id, " +
				"near_lat/near_lng or near_place_id with radius_m, max_price (0-4), min_rating (my rating 1-10), not_visited_days (never visited or not in N days). " +
				"sort: recent | rating | distance | name. Results within a radius include distance_m.",
			InputSchema: schemaObject(map[string]any{
				"q": sString(), "kind": sString(), "status": sString(), "tag": sString(), "city": sString(),
				"list_id": sInteger(), "near_lat": sNumber(), "near_lng": sNumber(), "near_place_id": sInteger(),
				"radius_m": sNumber(), "max_price": sInteger(), "min_rating": sInteger(),
				"not_visited_days": sInteger(), "sort": sString(), "limit": sInteger(),
			}, nil),
			Handler: a.toolSearch,
		},
		{
			Name:        "places_get",
			Description: "Read one place with its visit log and the lists it belongs to. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}),
			Handler:     a.toolGet,
		},
		{
			Name: "places_update",
			Description: "Patch a place. Args: id, plus any of name, kind, status, lat, lng, address, city, country, website, phone, price_level, my_rating (null clears), notes, source_url, " +
				"tags (replace all), add_tags, remove_tags, metadata (object).",
			InputSchema: schemaObject(map[string]any{
				"id": sInteger(), "name": sString(), "kind": sString(), "status": sString(),
				"lat": sNumber(), "lng": sNumber(), "address": sString(), "city": sString(), "country": sString(),
				"website": sString(), "phone": sString(), "price_level": sInteger(), "my_rating": sInteger(),
				"notes": sString(), "source_url": sString(), "tags": sArray("string"),
				"add_tags": sArray("string"), "remove_tags": sArray("string"), "metadata": map[string]any{"type": "object"},
			}, []string{"id"}),
			Handler: a.toolUpdate,
		},
		{
			Name:        "places_delete",
			Description: "Delete a place and its visits. Prefer status=closed for places that shut down. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}),
			Handler:     a.toolDelete,
		},
		{
			Name: "places_log_visit",
			Description: "Log a visit. A to_try place becomes visited; rating (1-10) also becomes the place's my_rating. " +
				"Args: place_id, visited_on? (YYYY-MM-DD, default today), with_whom?, dishes? (array), spend?, currency?, rating?, notes?, " +
				"details? (object, e.g. {room:'sea view', nightly_price:140, booked_via:'direct'} for hotels).",
			InputSchema: schemaObject(map[string]any{
				"place_id": sInteger(), "visited_on": sString(), "with_whom": sString(), "dishes": sArray("string"),
				"spend": sNumber(), "currency": sString(), "rating": sInteger(), "notes": sString(),
				"details": map[string]any{"type": "object"},
			}, []string{"place_id"}),
			Handler: a.toolLogVisit,
		},
		{
			Name:        "places_delete_visit",
			Description: "Delete one logged visit. Args: visit_id.",
			InputSchema: schemaObject(map[string]any{"visit_id": sInteger()}, []string{"visit_id"}),
			Handler:     a.toolDeleteVisit,
		},
		{
			Name:        "lists_list",
			Description: "List all place lists with their place counts.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolListsList,
		},
		{
			Name:        "lists_create",
			Description: "Create a list (returns the existing one if the name is taken). Args: name, description?, color?.",
			InputSchema: schemaObject(map[string]any{"name": sString(), "description": sString(), "color": sString()}, []string{"name"}),
			Handler:     a.toolListsCreate,
		},
		{
			Name:        "lists_update",
			Description: "Rename or describe a list. Args: id, name?, description?, color?.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "name": sString(), "description": sString(), "color": sString()}, []string{"id"}),
			Handler:     a.toolListsUpdate,
		},
		{
			Name:        "lists_delete",
			Description: "Delete a list. The places stay saved. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}),
			Handler:     a.toolListsDelete,
		},
		{
			Name:        "lists_add",
			Description: "Add places to a list. Args: list_id or list_name (created if missing), place_ids.",
			InputSchema: schemaObject(map[string]any{"list_id": sInteger(), "list_name": sString(), "place_ids": sArray("integer")}, []string{"place_ids"}),
			Handler:     a.toolListsAdd,
		},
		{
			Name:        "lists_remove",
			Description: "Remove places from a list. Args: list_id, place_ids.",
			InputSchema: schemaObject(map[string]any{"list_id": sInteger(), "place_ids": sArray("integer")}, []string{"list_id", "place_ids"}),
			Handler:     a.toolListsRemove,
		},
		{
			Name: "places_import",
			Description: "Import places. content = file text. format: auto (default) | geojson (Google Takeout 'Saved Places.json' or any GeoJSON points) | csv (Google Takeout list CSV: Title,Note,URL; or name,lat,lng,... columns). " +
				"Rows without coordinates are looked up when lookup=true (slow: 1/s on OpenStreetMap; max 50 lookups per call). list_name puts every row in that list. status sets the default (to_try).",
			InputSchema: schemaObject(map[string]any{
				"content": sString(), "format": sString(), "list_name": sString(), "status": sString(),
				"kind": sString(), "tags": sArray("string"), "lookup": sBool(),
			}, []string{"content"}),
			Handler: a.toolImport,
		},
		{
			Name:        "places_export",
			Description: "Export places as text. Args: format (geojson | csv, default geojson), status?, list_id?, kind?.",
			InputSchema: schemaObject(map[string]any{"format": sString(), "status": sString(), "list_id": sInteger(), "kind": sString()}, nil),
			Handler:     a.toolExport,
		},
		{
			Name:        "settings_get",
			Description: "Read settings: Google Places connection id (0 = OpenStreetMap lookups), home location used to centre the map and bias lookups, and the suggested kinds.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolSettingsGet,
		},
		{
			Name:        "settings_set",
			Description: "Update settings. Args: places_connection_id? (0 to use OpenStreetMap), home_lat?, home_lng?, home_label?.",
			InputSchema: schemaObject(map[string]any{"places_connection_id": sInteger(), "home_lat": sNumber(), "home_lng": sNumber(), "home_label": sString()}, nil),
			Handler:     a.toolSettingsSet,
		},
		{
			Name:        "connections_list",
			Description: "List Google Places connections available to this project.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolConnections,
		},
	}
}

func (a *App) toolAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, _ := loadSettings(ctx.AppDB(), ctx.CurrentProject())
	res, err := addPlace(ctx.AppDB(), ctx.CurrentProject(), a.geocoderFor(ctx, settings), settings, args, nil)
	if err != nil {
		return nil, err
	}
	if !res.Duplicate {
		emit(ctx, "place.added", res.Place)
	}
	return res, nil
}

func (a *App) toolLookup(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	settings, _ := loadSettings(db, pid)
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return nil, errors.New("query required")
	}
	if city := strings.TrimSpace(stringArg(args, "city")); city != "" && !strings.Contains(strings.ToLower(query), strings.ToLower(city)) {
		query += ", " + city
	}
	q := lookupQuery{Text: query, Kind: stringArg(args, "kind"), Limit: intArg(args, "limit", 5)}
	if lat, lng := floatPtrArg(args, "near_lat"), floatPtrArg(args, "near_lng"); lat != nil && lng != nil {
		q.Near = &[2]float64{*lat, *lng}
	} else if settings.HomeLat != nil && settings.HomeLng != nil {
		q.Near = &[2]float64{*settings.HomeLat, *settings.HomeLng}
	}
	cands, err := a.geocoderFor(ctx, settings).Search(q)
	if err != nil {
		return nil, err
	}
	cands = rankInCity(cands, strings.TrimSpace(stringArg(args, "city")))
	type withSaved struct {
		Candidate
		SavedPlaceID int64 `json:"saved_place_id,omitempty"`
	}
	out := make([]withSaved, 0, len(cands))
	for _, c := range cands {
		w := withSaved{Candidate: c}
		if p, _ := findPlaceByGoogleID(db, pid, c.GooglePlaceID); p != nil {
			w.SavedPlaceID = p.ID
		} else if p, _ := findNearDuplicate(db, pid, c.Name, c.Lat, c.Lng, c.City); p != nil {
			w.SavedPlaceID = p.ID
		}
		out = append(out, w)
	}
	source := "osm"
	if settings.PlacesConnectionID > 0 {
		source = "google"
	}
	return map[string]any{"candidates": out, "source": source}, nil
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	f := placeFilter{
		Q: stringArg(args, "q"), Kind: cleanToken(stringArg(args, "kind"), ""), Status: stringArg(args, "status"),
		Tag: stringArg(args, "tag"), City: stringArg(args, "city"), ListID: int64Arg(args, "list_id"),
		NearLat: floatPtrArg(args, "near_lat"), NearLng: floatPtrArg(args, "near_lng"),
		MaxPrice: intPtrArg(args, "max_price"), MinMyRating: intPtrArg(args, "min_rating"),
		NotVisitedDays: intArg(args, "not_visited_days", 0), Sort: stringArg(args, "sort"),
		Limit: intArg(args, "limit", 50),
	}
	if r := floatPtrArg(args, "radius_m"); r != nil {
		f.RadiusM = *r
	}
	if nid := int64Arg(args, "near_place_id"); nid > 0 {
		p, err := getPlace(db, pid, nid)
		if err != nil {
			return nil, fmt.Errorf("near_place_id: %w", err)
		}
		if p.Lat == nil {
			return nil, errors.New("near_place_id has no coordinates")
		}
		f.NearLat, f.NearLng = p.Lat, p.Lng
	}
	if f.Status != "" && f.Status != "all" && f.Status != "active" && !validStatuses[f.Status] {
		return nil, fmt.Errorf("invalid status %q", f.Status)
	}
	places, err := listPlaces(db, pid, f)
	if err != nil {
		return nil, err
	}
	return map[string]any{"places": places, "count": len(places)}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	p, err := getPlace(db, pid, int64Arg(args, "id"))
	if err != nil {
		return nil, notFound(err, "place")
	}
	visits, err := listVisits(db, pid, p.ID, 0)
	if err != nil {
		return nil, err
	}
	lists, err := listLists(db, pid)
	if err != nil {
		return nil, err
	}
	in := []*List{}
	for _, l := range lists {
		for _, id := range p.ListIDs {
			if id == l.ID {
				in = append(in, l)
			}
		}
	}
	return map[string]any{"place": p, "visits": visits, "lists": in}, nil
}

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	id := int64Arg(args, "id")
	current, err := getPlace(db, pid, id)
	if err != nil {
		return nil, notFound(err, "place")
	}
	patch, err := buildPatch(current, args)
	if err != nil {
		return nil, err
	}
	p, err := updatePlace(db, pid, id, patch)
	if err != nil {
		return nil, err
	}
	emit(ctx, "place.updated", p)
	return map[string]any{"place": p}, nil
}

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	id := int64Arg(args, "id")
	p, err := getPlace(db, pid, id)
	if err != nil {
		return nil, notFound(err, "place")
	}
	if err := deletePlace(db, pid, id); err != nil {
		return nil, err
	}
	emit(ctx, "place.deleted", p)
	return map[string]any{"deleted": true, "id": id}, nil
}

func (a *App) toolLogVisit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	dishes, err := normalizeStringList(args["dishes"])
	if err != nil {
		return nil, fmt.Errorf("dishes: %w", err)
	}
	details, err := normalizeObject(args["details"])
	if err != nil {
		return nil, fmt.Errorf("details: %w", err)
	}
	v, p, err := logVisit(db, pid, int64Arg(args, "place_id"), visitInput{
		VisitedOn: stringArg(args, "visited_on"), WithWhom: stringArg(args, "with_whom"),
		DishesJSON: dishes, Spend: floatPtrArg(args, "spend"), Currency: stringArg(args, "currency"),
		Rating: intPtrArg(args, "rating"), Notes: stringArg(args, "notes"), DetailsJSON: details,
	})
	if err != nil {
		return nil, notFound(err, "place")
	}
	emit(ctx, "place.visited", p)
	return map[string]any{"visit": v, "place": p}, nil
}

func (a *App) toolDeleteVisit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := deleteVisit(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "visit_id"))
	if err != nil {
		return nil, notFound(err, "visit")
	}
	return map[string]any{"deleted": true, "place": p}, nil
}

func (a *App) toolListsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	lists, err := listLists(ctx.AppDB(), ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	return map[string]any{"lists": lists}, nil
}

func (a *App) toolListsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	l, created, err := ensureList(ctx.AppDB(), ctx.CurrentProject(), stringArg(args, "name"), stringArg(args, "description"), stringArg(args, "color"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"list": l, "created": created}, nil
}

func (a *App) toolListsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, _ := stringPtrArg(args, "name")
	desc, _ := stringPtrArg(args, "description")
	color, _ := stringPtrArg(args, "color")
	l, err := updateList(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), name, desc, color)
	if err != nil {
		return nil, notFound(err, "list")
	}
	return map[string]any{"list": l}, nil
}

func (a *App) toolListsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := deleteList(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id")); err != nil {
		return nil, notFound(err, "list")
	}
	return map[string]any{"deleted": true}, nil
}

func (a *App) toolListsAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	listID := int64Arg(args, "list_id")
	if listID == 0 {
		l, _, err := ensureList(db, pid, stringArg(args, "list_name"), "", "")
		if err != nil {
			return nil, errors.New("list_id or list_name required")
		}
		listID = l.ID
	}
	added, err := addToList(db, pid, listID, int64sArg(args, "place_ids"))
	if err != nil {
		return nil, notFound(err, "list or place")
	}
	l, _ := getList(db, pid, listID)
	return map[string]any{"added": added, "list": l}, nil
}

func (a *App) toolListsRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	removed, err := removeFromList(db, pid, int64Arg(args, "list_id"), int64sArg(args, "place_ids"))
	if err != nil {
		return nil, notFound(err, "list")
	}
	return map[string]any{"removed": removed}, nil
}

func (a *App) toolImport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	settings, _ := loadSettings(ctx.AppDB(), ctx.CurrentProject())
	res, err := importPlaces(ctx.AppDB(), ctx.CurrentProject(), a.geocoderFor(ctx, settings), settings, args)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (a *App) toolExport(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	format, content, err := exportPlaces(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"format": format, "content": content}, nil
}

func (a *App) toolSettingsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := loadSettings(ctx.AppDB(), ctx.CurrentProject())
	if err != nil {
		return nil, err
	}
	lookup := "openstreetmap"
	if s.PlacesConnectionID > 0 {
		lookup = "google"
	}
	return map[string]any{"settings": s, "lookup_source": lookup, "kinds": suggestedKinds}, nil
}

func (a *App) toolSettingsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	s, err := loadSettings(db, pid)
	if err != nil {
		return nil, err
	}
	if _, ok := args["places_connection_id"]; ok {
		s.PlacesConnectionID = int64Arg(args, "places_connection_id")
	}
	if _, ok := args["home_lat"]; ok {
		s.HomeLat = floatPtrArg(args, "home_lat")
	}
	if _, ok := args["home_lng"]; ok {
		s.HomeLng = floatPtrArg(args, "home_lng")
	}
	if v, ok := stringPtrArg(args, "home_label"); ok {
		s.HomeLabel = strings.TrimSpace(*v)
	}
	if (s.HomeLat == nil) != (s.HomeLng == nil) {
		return nil, errors.New("home_lat and home_lng must be set together")
	}
	if err := saveSettings(db, s); err != nil {
		return nil, err
	}
	return a.toolSettingsGet(ctx, nil)
}

func (a *App) toolConnections(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: ctx.CurrentProject(), AppSlug: "google-places"})
	if err != nil {
		return nil, err
	}
	type out struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	res := make([]out, 0, len(conns))
	for _, c := range conns {
		res = append(res, out{ID: c.ID, Name: c.Name, Status: c.Status})
	}
	return map[string]any{"connections": res}, nil
}

// ─── HTTP (panel) ────────────────────────────────────────────────

// handleCall exposes every MCP tool to the panel as POST /call/<tool>
// with the tool's args as the JSON body, so the panel and agents share
// one code path.
func (a *App) handleCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	ctx := requestCtx(r)
	if ctx == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/call/"), "/")
	var handler toolFn
	for _, t := range a.MCPTools() {
		if t.Name == name {
			handler = t.Handler
			break
		}
	}
	if handler == nil {
		http.NotFound(w, r)
		return
	}
	args := map[string]any{}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 20<<20)).Decode(&args); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	out, err := handler(ctx, args)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sql.ErrNoRows) || strings.HasSuffix(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	if ctx == nil {
		http.Error(w, "not mounted", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	args := map[string]any{"format": q.Get("format"), "status": q.Get("status"), "kind": q.Get("kind")}
	if id, _ := strconv.ParseInt(q.Get("list_id"), 10, 64); id > 0 {
		args["list_id"] = float64(id)
	}
	format, content, err := exportPlaces(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ctype, ext := "application/geo+json", "geojson"
	if format == "csv" {
		ctype, ext = "text/csv; charset=utf-8", "csv"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="places.`+ext+`"`)
	_, _ = w.Write([]byte(content))
}

func requestCtx(r *http.Request) *sdk.AppCtx {
	ctx := globalCtx
	if ctx == nil {
		return nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return ctx.WithProject(pid)
	}
	return ctx
}

func emit(ctx *sdk.AppCtx, topic string, p *Place) {
	if ctx == nil || p == nil {
		return
	}
	ctx.EmitWithProject(topic, p.ProjectID, map[string]any{
		"id": p.ID, "name": p.Name, "kind": p.Kind, "status": p.Status, "city": p.City,
		"tags": p.Tags,
	})
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s not found", what)
	}
	return err
}
