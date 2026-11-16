package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type addResult struct {
	Place        *Place `json:"place"`
	Duplicate    bool   `json:"duplicate"`
	LookupSource string `json:"lookup_source,omitempty"`
	LookupError  string `json:"lookup_error,omitempty"`
	AddedToList  string `json:"added_to_list,omitempty"`
}

// addPlace resolves, de-duplicates and saves one place. lookupsLeft
// caps geocoder calls (nil = no cap); it's decremented per call.
func addPlace(db *sql.DB, pid string, geo geocoder, settings Settings, args map[string]any, lookupsLeft *int) (*addResult, error) {
	in := placeInput{
		Name: strings.TrimSpace(stringArg(args, "name")), Kind: stringArg(args, "kind"),
		Status: cleanToken(stringArg(args, "status"), "to_try"),
		Lat:    floatPtrArg(args, "lat"), Lng: floatPtrArg(args, "lng"),
		Address: stringArg(args, "address"), City: strings.TrimSpace(stringArg(args, "city")),
		Country: strings.TrimSpace(stringArg(args, "country")), GooglePlaceID: stringArg(args, "google_place_id"),
		SourceURL: stringArg(args, "source_url"), Notes: stringArg(args, "notes"),
		MyRating: intPtrArg(args, "my_rating"), PriceLevel: intPtrArg(args, "price_level"),
	}
	if (in.Lat == nil) != (in.Lng == nil) {
		return nil, errors.New("lat and lng must be given together")
	}
	tags, err := normalizeStringList(args["tags"])
	if err != nil {
		return nil, fmt.Errorf("tags: %w", err)
	}
	in.TagsJSON = tags
	if meta, ok := args["metadata"]; ok {
		if in.MetadataJSON, err = normalizeObject(meta); err != nil {
			return nil, err
		}
	}

	// A Google Maps link gives us a name and usually exact coordinates.
	if link := strings.TrimSpace(stringArg(args, "url")); link != "" {
		if isMapsURL(link) {
			expanded, _ := expandShortMapsURL(link, nil)
			ml := parseMapsURL(expanded)
			if in.Name == "" {
				in.Name = ml.Name
			}
			if in.Lat == nil && ml.Lat != nil {
				in.Lat, in.Lng = ml.Lat, ml.Lng
			}
			in.GoogleMapsURL = expanded
		} else if in.SourceURL == "" {
			in.SourceURL = link
		}
	}
	if in.Name == "" {
		return nil, errors.New("name required (or a Google Maps link that contains one)")
	}

	res := &addResult{}
	if boolArg(args, "lookup", true) && geo != nil && in.GooglePlaceID == "" && (in.Lat == nil || in.Address == "") {
		if lookupsLeft == nil || *lookupsLeft > 0 {
			if lookupsLeft != nil {
				*lookupsLeft--
			}
			q := lookupQuery{Text: in.Name, Kind: cleanToken(in.Kind, ""), Limit: 5}
			if in.City != "" {
				// More results so the in-city match is among them; a
				// same-named place elsewhere in the province often ranks first.
				q.Limit = 10
				if !strings.Contains(strings.ToLower(in.Name), strings.ToLower(in.City)) {
					q.Text += ", " + in.City
				}
			}
			if in.Lat != nil {
				q.Near = &[2]float64{*in.Lat, *in.Lng}
			} else if settings.HomeLat != nil && settings.HomeLng != nil && in.City == "" {
				q.Near = &[2]float64{*settings.HomeLat, *settings.HomeLng}
			}
			cands, err := geo.Search(q)
			switch {
			case err != nil:
				res.LookupError = err.Error()
			case len(cands) == 0:
				res.LookupError = "no match found; saved without lookup details"
			case in.City != "" && in.Lat == nil && len(filterInCity(cands, in.City)) == 0:
				// Never take a same-named place in another town: saving it
				// without a location is better than a pin 40 km away.
				res.LookupError = fmt.Sprintf("no match in %s; saved without a location", in.City)
			default:
				if in.City != "" && in.Lat == nil {
					cands = filterInCity(cands, in.City)
				}
				cands = preferKind(cands, cleanToken(in.Kind, ""))
				c := pickCandidate(cands, in.Lat, in.Lng)
				if c == nil {
					res.LookupError = "no match near the given coordinates; saved as given"
				} else {
					res.LookupSource = c.Source
					mergeCandidate(&in, *c)
				}
			}
		}
	}

	if !boolArg(args, "allow_duplicate", false) {
		existing, err := findPlaceByGoogleID(db, pid, in.GooglePlaceID)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			if existing, err = findNearDuplicate(db, pid, in.Name, in.Lat, in.Lng, in.City); err != nil {
				return nil, err
			}
		}
		if existing != nil {
			res.Place, res.Duplicate = existing, true
			if err := addToNamedList(db, pid, stringArg(args, "list"), existing, res); err != nil {
				return nil, err
			}
			return res, nil
		}
	}

	p, err := insertPlace(db, pid, in)
	if err != nil {
		return nil, err
	}
	res.Place = p
	if err := addToNamedList(db, pid, stringArg(args, "list"), p, res); err != nil {
		return nil, err
	}
	return res, nil
}

func addToNamedList(db *sql.DB, pid, name string, p *Place, res *addResult) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	l, _, err := ensureList(db, pid, name, "", "")
	if err != nil {
		return err
	}
	if _, err := addToList(db, pid, l.ID, []int64{p.ID}); err != nil {
		return err
	}
	res.AddedToList = l.Name
	refreshed, err := getPlace(db, pid, p.ID)
	if err == nil {
		res.Place = refreshed
	}
	return nil
}

// filterInCity keeps candidates located in city. It trusts the
// candidate's own city field; the full address is only consulted when
// that field is empty, because addresses also name the province
// ("…, Sant Feliu de Codines, Vallès Oriental, Barcelona, …").
func filterInCity(cands []Candidate, city string) []Candidate {
	want := foldName(city)
	out := []Candidate{}
	for _, c := range cands {
		if c.City != "" {
			if foldName(c.City) == want {
				out = append(out, c)
			}
			continue
		}
		parts := strings.Split(c.Address, ",")
		for _, part := range parts {
			// "08002 Barcelona" → "barcelona"
			fields := strings.Fields(part)
			kept := []string{}
			for _, f := range fields {
				if strings.IndexFunc(f, func(r rune) bool { return r >= '0' && r <= '9' }) < 0 {
					kept = append(kept, f)
				}
			}
			if foldName(strings.Join(kept, " ")) == want {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// rankInCity moves candidates in city to the front, keeping order otherwise.
func rankInCity(cands []Candidate, city string) []Candidate {
	if strings.TrimSpace(city) == "" {
		return cands
	}
	in := filterInCity(cands, city)
	seen := map[int]bool{}
	for _, c := range in {
		for i := range cands {
			if !seen[i] && cands[i].Name == c.Name && cands[i].Address == c.Address {
				seen[i] = true
				break
			}
		}
	}
	out := append([]Candidate{}, in...)
	for i, c := range cands {
		if !seen[i] {
			out = append(out, c)
		}
	}
	return out
}

// foldName lowercases and strips common Latin accents so "Barcelona",
// "barcelona" and "Barcelóna" compare equal, as do "Lisboa"/"lisboa".
func foldName(s string) string {
	r := strings.NewReplacer(
		"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a",
		"é", "e", "è", "e", "ê", "e", "ë", "e",
		"í", "i", "ì", "i", "î", "i", "ï", "i",
		"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o",
		"ú", "u", "ù", "u", "û", "u", "ü", "u",
		"ç", "c", "ñ", "n", "·", "", "'", "", "’", "",
	)
	return strings.Join(strings.Fields(r.Replace(strings.ToLower(strings.TrimSpace(s)))), " ")
}

// preferKind reorders candidates when the caller said what kind of place
// it is: exact kind matches first, then any typed place, then "other"
// (manors, houses, localities that merely share the name). Order within
// each group is kept.
func preferKind(cands []Candidate, kind string) []Candidate {
	if kind == "" || kind == "other" {
		return cands
	}
	var exact, typed, rest []Candidate
	for _, c := range cands {
		switch {
		case c.Kind == kind:
			exact = append(exact, c)
		case c.Kind != "" && c.Kind != "other":
			typed = append(typed, c)
		default:
			rest = append(rest, c)
		}
	}
	return append(append(exact, typed...), rest...)
}

// pickCandidate takes the first result, or — when we already know the
// coordinates — the closest result within 300 m.
func pickCandidate(cands []Candidate, lat, lng *float64) *Candidate {
	if lat == nil || lng == nil {
		return &cands[0]
	}
	var best *Candidate
	bestD := 300.0
	for i := range cands {
		c := &cands[i]
		if c.Lat == nil || c.Lng == nil {
			continue
		}
		if d := haversine(*lat, *lng, *c.Lat, *c.Lng); d <= bestD {
			best, bestD = c, d
		}
	}
	return best
}

// mergeCandidate fills fields the caller didn't provide.
func mergeCandidate(in *placeInput, c Candidate) {
	if in.Lat == nil && c.Lat != nil {
		in.Lat, in.Lng = c.Lat, c.Lng
	}
	if in.Address == "" {
		in.Address = c.Address
	}
	if in.City == "" {
		in.City = c.City
	}
	if in.Country == "" {
		in.Country = c.Country
	}
	if in.Kind == "" && c.Kind != "" {
		in.Kind = c.Kind
	}
	in.GooglePlaceID = firstNonEmpty(in.GooglePlaceID, c.GooglePlaceID)
	in.OSMID = firstNonEmpty(in.OSMID, c.OSMID)
	in.GoogleMapsURL = firstNonEmpty(in.GoogleMapsURL, c.GoogleMapsURL)
	in.Website = firstNonEmpty(in.Website, c.Website)
	in.Phone = firstNonEmpty(in.Phone, c.Phone)
	if in.PriceLevel == nil {
		in.PriceLevel = c.PriceLevel
	}
	if in.GoogleRating == nil {
		in.GoogleRating = c.GoogleRating
	}
}

// buildPatch turns places_update args into a column patch.
func buildPatch(current *Place, args map[string]any) (map[string]any, error) {
	patch := map[string]any{}
	for _, k := range []string{"name", "address", "city", "country", "website", "phone", "notes", "source_url"} {
		if v, ok := stringPtrArg(args, k); ok {
			if k == "name" && strings.TrimSpace(*v) == "" {
				return nil, errors.New("name cannot be empty")
			}
			patch[k] = strings.TrimSpace(*v)
		}
	}
	if v, ok := stringPtrArg(args, "kind"); ok {
		patch["kind"] = cleanToken(*v, "other")
	}
	if v, ok := stringPtrArg(args, "status"); ok {
		s := cleanToken(*v, "")
		if !validStatuses[s] {
			return nil, fmt.Errorf("invalid status %q (to_try, visited, favorite, closed)", *v)
		}
		patch["status"] = s
	}
	_, hasLat := args["lat"]
	_, hasLng := args["lng"]
	if hasLat || hasLng {
		lat, lng := floatPtrArg(args, "lat"), floatPtrArg(args, "lng")
		if (lat == nil) != (lng == nil) {
			return nil, errors.New("lat and lng must be set together")
		}
		patch["lat"], patch["lng"] = lat, lng
	}
	for _, k := range []string{"price_level", "my_rating"} {
		if _, ok := args[k]; ok {
			v := intPtrArg(args, k) // nil clears
			if k == "my_rating" {
				if err := validateRating(v); err != nil {
					return nil, err
				}
			}
			if k == "price_level" && v != nil && (*v < 0 || *v > 4) {
				return nil, errors.New("price_level must be 0-4")
			}
			patch[k] = v
		}
	}
	if _, ok := args["metadata"]; ok {
		meta, err := normalizeObject(args["metadata"])
		if err != nil {
			return nil, err
		}
		patch["metadata_json"] = meta
	}
	_, setTags := args["tags"]
	_, addTags := args["add_tags"]
	_, rmTags := args["remove_tags"]
	if setTags || addTags || rmTags {
		var tags []string
		if setTags {
			raw, err := normalizeStringList(args["tags"])
			if err != nil {
				return nil, err
			}
			_ = json.Unmarshal([]byte(raw), &tags)
		} else {
			_ = json.Unmarshal(current.Tags, &tags)
		}
		if addTags {
			raw, err := normalizeStringList(args["add_tags"])
			if err != nil {
				return nil, err
			}
			var extra []string
			_ = json.Unmarshal([]byte(raw), &extra)
			tags = append(tags, extra...)
		}
		if rmTags {
			raw, err := normalizeStringList(args["remove_tags"])
			if err != nil {
				return nil, err
			}
			var drop []string
			_ = json.Unmarshal([]byte(raw), &drop)
			kept := tags[:0]
			for _, t := range tags {
				remove := false
				for _, d := range drop {
					if strings.EqualFold(t, d) {
						remove = true
					}
				}
				if !remove {
					kept = append(kept, t)
				}
			}
			tags = kept
		}
		b, _ := json.Marshal(dedupeStrings(tags))
		patch["tags_json"] = string(b)
	}
	return patch, nil
}

// ─── arg helpers ─────────────────────────────────────────────────

func stringArg(args map[string]any, key string) string {
	switch x := args[key].(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

func stringPtrArg(args map[string]any, key string) (*string, bool) {
	v, ok := args[key]
	if !ok {
		return nil, false
	}
	if v == nil {
		s := ""
		return &s, true
	}
	s, ok := v.(string)
	if !ok {
		s = fmt.Sprint(v)
	}
	return &s, true
}

func floatPtrArg(args map[string]any, key string) *float64 {
	switch x := args[key].(type) {
	case float64:
		return &x
	case int:
		f := float64(x)
		return &f
	case int64:
		f := float64(x)
		return &f
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return &f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return &f
		}
	}
	return nil
}

func intPtrArg(args map[string]any, key string) *int {
	f := floatPtrArg(args, key)
	if f == nil {
		return nil
	}
	i := int(*f)
	return &i
}

func int64Arg(args map[string]any, key string) int64 {
	if f := floatPtrArg(args, key); f != nil {
		return int64(*f)
	}
	return 0
}

func intArg(args map[string]any, key string, fallback int) int {
	if f := floatPtrArg(args, key); f != nil && *f != 0 {
		return int(*f)
	}
	return fallback
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	switch x := args[key].(type) {
	case bool:
		return x
	case string:
		if b, err := strconv.ParseBool(x); err == nil {
			return b
		}
	}
	return fallback
}

func int64sArg(args map[string]any, key string) []int64 {
	out := []int64{}
	switch x := args[key].(type) {
	case []any:
		for _, v := range x {
			if id := int64Arg(map[string]any{"v": v}, "v"); id > 0 {
				out = append(out, id)
			}
		}
	case []int64:
		out = append(out, x...)
	case float64, string:
		if id := int64Arg(args, key); id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// normalizeStringList accepts an array or a comma-separated string and
// returns a JSON array of trimmed, de-duplicated strings.
func normalizeStringList(v any) (string, error) {
	var items []string
	switch x := v.(type) {
	case nil:
	case []string:
		items = x
	case []any:
		for _, it := range x {
			items = append(items, fmt.Sprint(it))
		}
	case string:
		items = strings.Split(x, ",")
	default:
		return "", errors.New("must be an array or comma-separated string")
	}
	b, _ := json.Marshal(dedupeStrings(items))
	return string(b), nil
}

func dedupeStrings(items []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range items {
		s = strings.TrimSpace(s)
		k := strings.ToLower(s)
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

func normalizeObject(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "{}", nil
	case map[string]any:
		b, err := json.Marshal(x)
		return string(b), err
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return "{}", nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(x), &m); err != nil {
			return "", errors.New("must be a JSON object")
		}
		return x, nil
	}
	return "", errors.New("must be an object")
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func sString() map[string]any             { return map[string]any{"type": "string"} }
func sStringDesc(d string) map[string]any { return map[string]any{"type": "string", "description": d} }
func sNumber() map[string]any             { return map[string]any{"type": "number"} }
func sInteger() map[string]any            { return map[string]any{"type": "integer"} }
func sIntegerDesc(d string) map[string]any {
	return map[string]any{"type": "integer", "description": d}
}
func sBool() map[string]any { return map[string]any{"type": "boolean"} }
func sArray(item string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": item}}
}
