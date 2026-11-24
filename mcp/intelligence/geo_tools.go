package main

// MCP tools and panel routes for the world map, data quality, entity
// locations and synthetic sample data.

import (
	"errors"
	"fmt"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) geoTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "world_map", Description: "Everything the app knows, laid out for a map: located entities (with attributed locations, basis, latest values and quality flags), relationships between them, evidence points with their own locations, per-country totals, and every entity that has no location with the reason. Args: basis? (comma list of observed,inferred,synthetic), kind?, as_of?.",
			InputSchema: schemaObject(map[string]any{"basis": prop("string"), "kind": prop("string"), "as_of": prop("string")}, nil), Handler: a.toolWorldMap},
		{Name: "data_quality", Description: "What is stale (sources and series past their cadence, unpriced questions), what is missing (series never loaded, entities without a location, questions without evidence, evidence without dates or references) and where sources conflict (different values for the same day, disagreeing claims, locations far apart). Also counts rows by basis (observed/inferred/synthetic) and evidence by access class. Nothing is auto-resolved.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolDataQuality},
		{Name: "entity_locate", Description: "Place an entity on the map with attributed coordinates. Args: entity (id or name), lat, lon, precision (point|city|region|country), provenance (how the coordinates were obtained), label?, evidence_id?, basis? (observed|inferred|synthetic, default observed). One location per entity per source (source = operator); other sources keep theirs. public_role entities take country precision only.",
			InputSchema: schemaObject(map[string]any{"entity": prop("string"), "lat": prop("number"), "lon": prop("number"), "precision": prop("string"),
				"provenance": prop("string"), "label": prop("string"), "evidence_id": prop("integer"), "basis": prop("string")}, []string{"entity", "lat", "lon", "precision", "provenance"}),
			Handler: a.toolEntityLocate},
		{Name: "synthetic_seed", Description: "Load a small synthetic shipping scenario (labelled synthetic everywhere) to try the map and the quality checks: located facilities, observed/inferred/assumed links, a stale series, conflicting sources, a missing location and a restricted record. Synthetic rows never feed forecasts, the ledger, extraction or scenarios.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolSyntheticSeed},
		{Name: "synthetic_clear", Description: "Delete every synthetic row (evidence, entities, links, locations, values, claims). Real data is untouched.",
			InputSchema: schemaObject(nil, nil), Handler: a.toolSyntheticClear},
	}
}

// handleAccessClasses lists the access classes for the grant picker.
func handleAccessClasses(w http.ResponseWriter, r *http.Request) {
	httpJSON(w, 200, map[string]any{"items": []map[string]string{
		{"id": accessOpen, "label": "Open (public sources)"},
		{"id": accessInternal, "label": "Internal (licensed or operator data)"},
		{"id": accessRestricted, "label": "Restricted (explicit grant only)"},
	}})
}

func (a *App) toolWorldMap(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := ensureDefaultSources(ctx, pid); err != nil {
		return nil, err
	}
	f, err := parseMapFilter(args)
	if err != nil {
		return nil, err
	}
	return buildWorldMap(ctx.AppDB(), pid, readerFrom(args), f, nowUTC())
}

func (a *App) toolDataQuality(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if err := ensureDefaultSources(ctx, pid); err != nil {
		return nil, err
	}
	return dataQuality(ctx.AppDB(), pid, nowUTC())
}

func (a *App) toolEntityLocate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, err := entityRef(ctx, pid, args, "entity")
	if err != nil {
		return nil, err
	}
	lat, okLat := floatArgAny(args, "lat")
	lon, okLon := floatArgAny(args, "lon")
	if !okLat || !okLon {
		return nil, errors.New("lat and lon are required numbers")
	}
	l := EntityLocation{EntityID: id, Lat: lat, Lon: lon, Label: strArg(args, "label"), Precision: strArg(args, "precision"),
		Provenance: strArg(args, "provenance"), Source: "operator", Basis: strArg(args, "basis")}
	if ev := intArgAny(args, "evidence_id"); ev > 0 {
		l.EvidenceID = &ev
	}
	if l.Precision == "country" {
		if c, ok := gazetteerLookup(l.Label); ok {
			l.CountryID = c.ID
		}
	}
	if l.CountryID == "" {
		l.CountryID = nearestCountry(lat, lon)
	}
	if err := putEntityLocation(ctx.AppDB(), pid, l); err != nil {
		return nil, err
	}
	locs, err := entityLocations(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"entity_id": id, "locations": locs[id], "location": bestLocation(locs[id])}, nil
}

func (a *App) toolSyntheticSeed(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	counts, err := seedSynthetic(ctx.AppDB(), pid, nowUTC())
	if err != nil {
		return nil, err
	}
	return map[string]any{"seeded": counts, "note": "All rows are basis synthetic; clear them with synthetic_clear."}, nil
}

func (a *App) toolSyntheticClear(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	removed, err := clearSynthetic(ctx.AppDB(), pid)
	if err != nil {
		return nil, fmt.Errorf("clear synthetic data: %w", err)
	}
	return map[string]any{"removed": removed}, nil
}
