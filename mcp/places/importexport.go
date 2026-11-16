package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	maxImportRows    = 2000
	maxImportLookups = 50
)

type importResult struct {
	Format        string   `json:"format"`
	Rows          int      `json:"rows"`
	Added         int      `json:"added"`
	Duplicates    int      `json:"duplicates"`
	WithoutCoords int      `json:"without_coordinates"`
	Failed        int      `json:"failed"`
	Errors        []string `json:"errors,omitempty"`
	List          string   `json:"list,omitempty"`
}

func importPlaces(db *sql.DB, pid string, geo geocoder, settings Settings, args map[string]any) (*importResult, error) {
	content := strings.TrimSpace(strings.TrimPrefix(stringArg(args, "content"), "\uFEFF"))
	if content == "" {
		return nil, errors.New("content required")
	}
	format := strings.ToLower(strings.TrimSpace(stringArg(args, "format")))
	if format == "" || format == "auto" {
		if strings.HasPrefix(content, "{") || strings.HasPrefix(content, "[") {
			format = "geojson"
		} else {
			format = "csv"
		}
	}
	var rows []map[string]any
	var err error
	switch format {
	case "geojson", "google_takeout", "json":
		format = "geojson"
		rows, err = parseGeoJSONRows(content)
	case "csv":
		rows, err = parseCSVRows(content)
	default:
		return nil, fmt.Errorf("unknown format %q (geojson or csv)", format)
	}
	if err != nil {
		return nil, err
	}
	if len(rows) > maxImportRows {
		return nil, fmt.Errorf("%d rows; import at most %d per call", len(rows), maxImportRows)
	}
	res := &importResult{Format: format, Rows: len(rows), List: strings.TrimSpace(stringArg(args, "list_name"))}
	lookup := boolArg(args, "lookup", false)
	budget := maxImportLookups
	defStatus := stringArg(args, "status")
	defKind := stringArg(args, "kind")
	extraTags, _ := normalizeStringList(args["tags"])
	for i, row := range rows {
		if _, ok := row["status"]; !ok && defStatus != "" {
			row["status"] = defStatus
		}
		if _, ok := row["kind"]; !ok && defKind != "" {
			row["kind"] = defKind
		}
		if extraTags != "[]" {
			var a, b []string
			_ = json.Unmarshal([]byte(extraTags), &a)
			if t, ok := row["tags"].([]string); ok {
				b = t
			}
			row["tags"] = append(b, a...)
		}
		if res.List != "" {
			row["list"] = res.List
		}
		row["lookup"] = lookup
		var g geocoder
		if lookup {
			g = geo
		}
		r, err := addPlace(db, pid, g, settings, row, &budget)
		if err != nil {
			res.Failed++
			if len(res.Errors) < 20 {
				res.Errors = append(res.Errors, fmt.Sprintf("row %d (%s): %v", i+1, stringArg(row, "name"), err))
			}
			continue
		}
		if r.Duplicate {
			res.Duplicates++
		} else {
			res.Added++
			if r.Place.Lat == nil {
				res.WithoutCoords++
			}
		}
	}
	if lookup && budget == 0 {
		res.Errors = append(res.Errors, fmt.Sprintf("lookup limit of %d reached; later rows were saved without lookup", maxImportLookups))
	}
	return res, nil
}

// parseGeoJSONRows reads a FeatureCollection: Google Takeout "Saved
// Places.json" (new and old layouts), our own export, or any point
// GeoJSON with name/title properties.
func parseGeoJSONRows(content string) ([]map[string]any, error) {
	var fc struct {
		Features []struct {
			Geometry *struct {
				Type        string    `json:"type"`
				Coordinates []float64 `json:"coordinates"`
			} `json:"geometry"`
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal([]byte(content), &fc); err != nil {
		return nil, fmt.Errorf("invalid GeoJSON: %w", err)
	}
	rows := []map[string]any{}
	for _, f := range fc.Features {
		p := f.Properties
		if p == nil {
			p = map[string]any{}
		}
		row := map[string]any{}
		loc, _ := p["location"].(map[string]any)
		oldLoc, _ := p["Location"].(map[string]any)
		row["name"] = firstNonEmpty(str(p["name"]), str(p["Title"]), str(p["title"]), str(loc["name"]),
			str(oldLoc["Business Name"]))
		row["address"] = firstNonEmpty(str(p["address"]), str(loc["address"]), str(oldLoc["Address"]))
		if cc := str(loc["country_code"]); cc != "" {
			row["country"] = cc
		}
		mapsURL := firstNonEmpty(str(p["google_maps_url"]), str(p["Google Maps URL"]))
		if mapsURL != "" {
			row["google_maps_url_raw"] = mapsURL
		}
		if notes := firstNonEmpty(str(p["notes"]), str(p["Comment"]), str(p["comment"]), str(p["description"])); notes != "" {
			row["notes"] = notes
		}
		for _, k := range []string{"kind", "status", "city", "country", "website", "phone", "source_url", "google_place_id"} {
			if v := str(p[k]); v != "" {
				row[k] = v
			}
		}
		for _, k := range []string{"my_rating", "price_level"} {
			if v, ok := p[k].(float64); ok {
				row[k] = v
			}
		}
		if tags, ok := p["tags"].([]any); ok {
			ts := []string{}
			for _, t := range tags {
				ts = append(ts, fmt.Sprint(t))
			}
			row["tags"] = ts
		}
		if f.Geometry != nil && len(f.Geometry.Coordinates) >= 2 {
			lng, lat := f.Geometry.Coordinates[0], f.Geometry.Coordinates[1]
			if lat != 0 || lng != 0 {
				row["lat"], row["lng"] = lat, lng
			}
		}
		if _, ok := row["lat"]; !ok {
			if gc, ok := oldLoc["Geo Coordinates"].(map[string]any); ok {
				lat, _ := strconv.ParseFloat(str(gc["Latitude"]), 64)
				lng, _ := strconv.ParseFloat(str(gc["Longitude"]), 64)
				if lat != 0 || lng != 0 {
					row["lat"], row["lng"] = lat, lng
				}
			}
		}
		finishImportRow(row)
		rows = append(rows, row)
	}
	return rows, nil
}

// parseCSVRows reads Google Takeout list CSVs (Title,Note,URL[,Tags,Comment])
// or a generic CSV with name/lat/lng/... headers.
func parseCSVRows(content string) ([]map[string]any, error) {
	r := csv.NewReader(strings.NewReader(content))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("invalid CSV: %w", err)
	}
	if len(records) < 2 {
		return []map[string]any{}, nil
	}
	alias := map[string]string{
		"title": "name", "name": "name", "note": "notes", "notes": "notes", "comment": "notes",
		"url": "url", "google maps url": "url", "link": "url", "lat": "lat", "latitude": "lat",
		"lng": "lng", "lon": "lng", "long": "lng", "longitude": "lng", "city": "city", "country": "country",
		"address": "address", "kind": "kind", "type": "kind", "category": "kind", "status": "status",
		"tags": "tags", "my_rating": "my_rating", "rating": "my_rating", "price_level": "price_level",
		"website": "website", "phone": "phone", "source_url": "source_url",
	}
	cols := map[int]string{}
	for i, h := range records[0] {
		if k, ok := alias[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))]; ok {
			cols[i] = k
		}
	}
	if len(cols) == 0 {
		return nil, errors.New("CSV header not recognised (expected Title/Note/URL or name/lat/lng columns)")
	}
	rows := []map[string]any{}
	for _, rec := range records[1:] {
		row := map[string]any{}
		empty := true
		for i, v := range rec {
			k, ok := cols[i]
			v = strings.TrimSpace(v)
			if !ok || v == "" {
				continue
			}
			empty = false
			switch k {
			case "notes":
				if prev := str(row["notes"]); prev != "" {
					v = prev + "\n" + v
				}
				row["notes"] = v
			case "lat", "lng", "my_rating", "price_level":
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					row[k] = f
				}
			case "tags":
				parts := strings.FieldsFunc(v, func(r rune) bool { return r == ';' || r == ',' || r == '|' })
				row["tags"] = parts
			case "url":
				row["google_maps_url_raw"] = v
			default:
				row[k] = v
			}
		}
		if empty {
			continue
		}
		finishImportRow(row)
		rows = append(rows, row)
	}
	return rows, nil
}

// finishImportRow turns a Maps URL into name/coords without network calls
// (short links are left as source_url: expanding 2000 of them is too slow).
func finishImportRow(row map[string]any) {
	raw := str(row["google_maps_url_raw"])
	delete(row, "google_maps_url_raw")
	if raw == "" {
		return
	}
	if isMapsURL(raw) && !strings.Contains(raw, "goo.gl") {
		// addPlace reads name, coordinates and the link from "url".
		row["url"] = raw
		return
	}
	if str(row["source_url"]) == "" {
		row["source_url"] = raw
	}
}

func exportPlaces(db *sql.DB, pid string, args map[string]any) (string, string, error) {
	format := strings.ToLower(firstNonEmpty(stringArg(args, "format"), "geojson"))
	status := firstNonEmpty(stringArg(args, "status"), "all")
	places, err := listPlaces(db, pid, placeFilter{Status: status, Kind: stringArg(args, "kind"), ListID: int64Arg(args, "list_id"), Sort: "name", Limit: 1000})
	if err != nil {
		return "", "", err
	}
	switch format {
	case "geojson":
		features := []map[string]any{}
		for _, p := range places {
			var tags []string
			_ = json.Unmarshal(p.Tags, &tags)
			props := map[string]any{
				"id": p.ID, "name": p.Name, "kind": p.Kind, "status": p.Status, "address": p.Address,
				"city": p.City, "country": p.Country, "tags": tags, "notes": p.Notes,
				"my_rating": p.MyRating, "price_level": p.PriceLevel, "google_place_id": p.GooglePlaceID,
				"google_maps_url": p.GoogleMapsURL, "website": p.Website, "phone": p.Phone,
				"source_url": p.SourceURL, "visit_count": p.VisitCount, "last_visited_at": p.LastVisitedAt,
			}
			var geom any
			if p.Lat != nil && p.Lng != nil {
				geom = map[string]any{"type": "Point", "coordinates": []float64{*p.Lng, *p.Lat}}
			}
			features = append(features, map[string]any{"type": "Feature", "geometry": geom, "properties": props})
		}
		b, err := json.MarshalIndent(map[string]any{"type": "FeatureCollection", "features": features}, "", "  ")
		return "geojson", string(b), err
	case "csv":
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write([]string{"name", "kind", "status", "lat", "lng", "address", "city", "country", "tags", "notes",
			"my_rating", "price_level", "visit_count", "last_visited_at", "website", "google_maps_url", "source_url"})
		for _, p := range places {
			var tags []string
			_ = json.Unmarshal(p.Tags, &tags)
			_ = w.Write([]string{p.Name, p.Kind, p.Status, fmtFloat(p.Lat), fmtFloat(p.Lng), p.Address, p.City,
				p.Country, strings.Join(tags, ";"), p.Notes, fmtInt(p.MyRating), fmtInt(p.PriceLevel),
				strconv.Itoa(p.VisitCount), p.LastVisitedAt, p.Website, p.GoogleMapsURL, p.SourceURL})
		}
		w.Flush()
		return "csv", buf.String(), w.Error()
	}
	return "", "", fmt.Errorf("unknown format %q (geojson or csv)", format)
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func fmtFloat(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', 6, 64)
}

func fmtInt(i *int) string {
	if i == nil {
		return ""
	}
	return strconv.Itoa(*i)
}
