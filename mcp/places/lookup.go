package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Candidate is one lookup result, normalised across Google Places and
// OpenStreetMap (Nominatim). Nothing is saved until places_add.
type Candidate struct {
	Source        string   `json:"source"` // google | osm
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Lat           *float64 `json:"lat"`
	Lng           *float64 `json:"lng"`
	Address       string   `json:"address"`
	City          string   `json:"city"`
	Country       string   `json:"country"`
	GooglePlaceID string   `json:"google_place_id,omitempty"`
	OSMID         string   `json:"osm_id,omitempty"`
	GoogleMapsURL string   `json:"google_maps_url,omitempty"`
	Website       string   `json:"website,omitempty"`
	Phone         string   `json:"phone,omitempty"`
	PriceLevel    *int     `json:"price_level,omitempty"`
	GoogleRating  *float64 `json:"google_rating,omitempty"`
	RatingCount   int      `json:"rating_count,omitempty"`
}

type lookupQuery struct {
	Text  string
	Near  *[2]float64 // lat, lng bias
	Kind  string
	Limit int
}

type geocoder interface {
	Search(q lookupQuery) ([]Candidate, error)
}

// ─── Google Places (New v1) through a platform connection ─────────

type integrationExecutor interface {
	Execute(connID int64, tool string, input map[string]any) (json.RawMessage, error)
}

type googleGeocoder struct {
	exec   integrationExecutor
	connID int64
}

func (g googleGeocoder) Search(q lookupQuery) ([]Candidate, error) {
	limit := q.Limit
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	input := map[string]any{"textQuery": q.Text, "pageSize": limit}
	if t := kindToGoogleType(q.Kind); t != "" {
		input["includedType"] = t
	}
	if q.Near != nil {
		input["locationBias"] = map[string]any{"circle": map[string]any{
			"center": map[string]any{"latitude": q.Near[0], "longitude": q.Near[1]},
			"radius": 20000.0,
		}}
	}
	data, err := g.exec.Execute(g.connID, "search_text", input)
	if err != nil {
		return nil, err
	}
	return parseGooglePlaces(data), nil
}

func parseGooglePlaces(data json.RawMessage) []Candidate {
	var raw struct {
		Places []struct {
			ID          string `json:"id"`
			DisplayName struct {
				Text string `json:"text"`
			} `json:"displayName"`
			FormattedAddress string `json:"formattedAddress"`
			Location         *struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"location"`
			Rating             *float64 `json:"rating"`
			UserRatingCount    int      `json:"userRatingCount"`
			PriceLevel         string   `json:"priceLevel"`
			PrimaryType        string   `json:"primaryType"`
			Types              []string `json:"types"`
			GoogleMapsURI      string   `json:"googleMapsUri"`
			WebsiteURI         string   `json:"websiteUri"`
			InternationalPhone string   `json:"internationalPhoneNumber"`
			AddressComponents  []struct {
				LongText string   `json:"longText"`
				Types    []string `json:"types"`
			} `json:"addressComponents"`
		} `json:"places"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	out := []Candidate{}
	for _, p := range raw.Places {
		c := Candidate{
			Source:        "google",
			Name:          p.DisplayName.Text,
			Address:       p.FormattedAddress,
			GooglePlaceID: p.ID,
			GoogleMapsURL: p.GoogleMapsURI,
			Website:       p.WebsiteURI,
			Phone:         p.InternationalPhone,
			GoogleRating:  p.Rating,
			RatingCount:   p.UserRatingCount,
			PriceLevel:    googlePriceLevel(p.PriceLevel),
			Kind:          googleTypeToKind(append([]string{p.PrimaryType}, p.Types...)),
		}
		if p.Location != nil {
			lat, lng := p.Location.Latitude, p.Location.Longitude
			c.Lat, c.Lng = &lat, &lng
		}
		for _, ac := range p.AddressComponents {
			for _, t := range ac.Types {
				switch t {
				case "locality":
					c.City = ac.LongText
				case "postal_town":
					if c.City == "" {
						c.City = ac.LongText
					}
				case "country":
					c.Country = ac.LongText
				}
			}
		}
		if c.City == "" || c.Country == "" {
			city, country := splitAddress(p.FormattedAddress)
			if c.City == "" {
				c.City = city
			}
			if c.Country == "" {
				c.Country = country
			}
		}
		out = append(out, c)
	}
	return out
}

func googlePriceLevel(s string) *int {
	m := map[string]int{
		"PRICE_LEVEL_FREE": 0, "PRICE_LEVEL_INEXPENSIVE": 1, "PRICE_LEVEL_MODERATE": 2,
		"PRICE_LEVEL_EXPENSIVE": 3, "PRICE_LEVEL_VERY_EXPENSIVE": 4,
	}
	if v, ok := m[s]; ok {
		return &v
	}
	return nil
}

func kindToGoogleType(kind string) string {
	switch kind {
	case "restaurant", "cafe", "bar", "bakery", "museum", "park", "market":
		if kind == "market" {
			return "market"
		}
		return kind
	case "hotel":
		return "lodging"
	case "attraction":
		return "tourist_attraction"
	case "beach":
		return "beach"
	case "shop":
		return "store"
	}
	return ""
}

func googleTypeToKind(types []string) string {
	for _, t := range types {
		switch t {
		case "restaurant", "meal_takeaway", "meal_delivery":
			return "restaurant"
		case "cafe", "coffee_shop":
			return "cafe"
		case "bar", "night_club", "wine_bar", "pub":
			return "bar"
		case "bakery":
			return "bakery"
		case "lodging", "hotel", "hostel", "bed_and_breakfast", "guest_house", "resort_hotel":
			return "hotel"
		case "museum", "art_gallery":
			return "museum"
		case "tourist_attraction", "historical_landmark", "monument":
			return "attraction"
		case "park", "national_park", "garden":
			return "park"
		case "beach":
			return "beach"
		case "market", "supermarket", "farmers_market":
			return "market"
		case "store", "clothing_store", "book_store", "shopping_mall":
			return "shop"
		}
		if strings.HasSuffix(t, "_restaurant") {
			return "restaurant"
		}
	}
	return "other"
}

// ─── OpenStreetMap Nominatim (keyless fallback) ───────────────────

type nominatimGeocoder struct {
	baseURL string
	client  *http.Client
}

var (
	nominatimMu   sync.Mutex
	nominatimLast time.Time
)

func (n nominatimGeocoder) Search(q lookupQuery) ([]Candidate, error) {
	limit := q.Limit
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	params := url.Values{}
	params.Set("q", q.Text)
	params.Set("format", "jsonv2")
	params.Set("addressdetails", "1")
	params.Set("extratags", "1")
	params.Set("limit", strconv.Itoa(limit))
	if q.Near != nil {
		// ~0.3° box bias, not a hard bound.
		params.Set("viewbox", fmt.Sprintf("%f,%f,%f,%f", q.Near[1]-0.3, q.Near[0]+0.3, q.Near[1]+0.3, q.Near[0]-0.3))
	}
	base := n.baseURL
	if base == "" {
		base = "https://nominatim.openstreetmap.org"
	}
	// Nominatim's usage policy: at most 1 request per second.
	nominatimMu.Lock()
	if wait := time.Second - time.Since(nominatimLast); wait > 0 {
		time.Sleep(wait)
	}
	nominatimLast = time.Now()
	nominatimMu.Unlock()

	req, _ := http.NewRequest(http.MethodGet, base+"/search?"+params.Encode(), nil)
	req.Header.Set("User-Agent", "apteva-places/0.1 (self-hosted personal places app)")
	req.Header.Set("Accept-Language", "en")
	client := n.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openstreetmap lookup failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openstreetmap lookup failed: http %d", resp.StatusCode)
	}
	return parseNominatim(body), nil
}

func parseNominatim(body []byte) []Candidate {
	var raw []struct {
		OSMType     string            `json:"osm_type"`
		OSMID       int64             `json:"osm_id"`
		Lat         string            `json:"lat"`
		Lon         string            `json:"lon"`
		Name        string            `json:"name"`
		DisplayName string            `json:"display_name"`
		Category    string            `json:"category"`
		Type        string            `json:"type"`
		Address     map[string]string `json:"address"`
		ExtraTags   map[string]string `json:"extratags"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	out := []Candidate{}
	for _, r := range raw {
		c := Candidate{
			Source:  "osm",
			Name:    r.Name,
			Address: r.DisplayName,
			Kind:    osmKind(r.Category, r.Type),
			OSMID:   fmt.Sprintf("%s/%d", r.OSMType, r.OSMID),
		}
		if c.Name == "" {
			c.Name = strings.TrimSpace(strings.SplitN(r.DisplayName, ",", 2)[0])
		}
		if lat, err := strconv.ParseFloat(r.Lat, 64); err == nil {
			c.Lat = &lat
		}
		if lng, err := strconv.ParseFloat(r.Lon, 64); err == nil {
			c.Lng = &lng
		}
		for _, k := range []string{"city", "town", "village", "municipality"} {
			if v := r.Address[k]; v != "" {
				c.City = v
				break
			}
		}
		c.Country = r.Address["country"]
		c.Website = firstNonEmpty(r.ExtraTags["website"], r.ExtraTags["contact:website"])
		c.Phone = firstNonEmpty(r.ExtraTags["phone"], r.ExtraTags["contact:phone"])
		out = append(out, c)
	}
	return out
}

func osmKind(category, typ string) string {
	switch category + "/" + typ {
	case "amenity/restaurant", "amenity/fast_food", "amenity/food_court":
		return "restaurant"
	case "amenity/cafe":
		return "cafe"
	case "amenity/bar", "amenity/pub", "amenity/biergarten", "amenity/nightclub":
		return "bar"
	case "shop/bakery":
		return "bakery"
	case "tourism/hotel", "tourism/guest_house", "tourism/hostel", "tourism/motel", "tourism/apartment", "tourism/chalet":
		return "hotel"
	case "tourism/museum", "tourism/gallery":
		return "museum"
	case "tourism/attraction", "historic/monument", "historic/castle":
		return "attraction"
	case "tourism/viewpoint":
		return "viewpoint"
	case "natural/beach":
		return "beach"
	case "leisure/park", "leisure/garden":
		return "park"
	case "amenity/marketplace":
		return "market"
	}
	if category == "shop" {
		return "shop"
	}
	return "other"
}

// ─── Google Maps links ────────────────────────────────────────────

var (
	reAtCoords   = regexp.MustCompile(`@(-?\d+\.\d+),(-?\d+\.\d+)`)
	reDataCoords = regexp.MustCompile(`!3d(-?\d+\.\d+)!4d(-?\d+\.\d+)`)
	reQCoords    = regexp.MustCompile(`^(-?\d+\.\d+),\s*(-?\d+\.\d+)$`)
)

// mapsLink is what we can read straight out of a Google Maps URL.
type mapsLink struct {
	Name     string
	Lat, Lng *float64
}

func isMapsURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" {
		return false
	}
	h := strings.ToLower(u.Host)
	return strings.Contains(h, "google.") && strings.Contains(u.Path, "/maps") ||
		h == "maps.app.goo.gl" || h == "goo.gl" || strings.HasPrefix(h, "maps.google.")
}

// parseMapsURL extracts the place name and coordinates. Precise
// "!3d…!4d…" pin coordinates win over the "@lat,lng" viewport centre.
func parseMapsURL(raw string) mapsLink {
	var out mapsLink
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return out
	}
	if i := strings.Index(u.Path, "/place/"); i >= 0 {
		seg := strings.SplitN(u.Path[i+len("/place/"):], "/", 2)[0]
		name, _ := url.PathUnescape(strings.ReplaceAll(seg, "+", " "))
		out.Name = strings.TrimSpace(name)
	}
	full := raw
	if m := reDataCoords.FindStringSubmatch(full); m != nil {
		out.Lat, out.Lng = parseFloatPair(m[1], m[2])
	} else if m := reAtCoords.FindStringSubmatch(full); m != nil {
		out.Lat, out.Lng = parseFloatPair(m[1], m[2])
	}
	if q := u.Query().Get("q"); q != "" {
		if m := reQCoords.FindStringSubmatch(strings.TrimSpace(q)); m != nil {
			if out.Lat == nil {
				out.Lat, out.Lng = parseFloatPair(m[1], m[2])
			}
		} else if out.Name == "" {
			out.Name = q
		}
	}
	return out
}

// expandShortMapsURL follows maps.app.goo.gl / goo.gl redirects.
func expandShortMapsURL(raw string, client *http.Client) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw, err
	}
	h := strings.ToLower(u.Host)
	if h != "maps.app.goo.gl" && h != "goo.gl" {
		return raw, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	current := raw
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest(http.MethodGet, current, nil)
		req.Header.Set("User-Agent", "apteva-places/0.1")
		resp, err := c.Do(req)
		if err != nil {
			return raw, err
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if loc == "" {
			return current, nil
		}
		next, err := u.Parse(loc)
		if err != nil {
			return current, nil
		}
		current = next.String()
		if nh := strings.ToLower(next.Host); nh != "maps.app.goo.gl" && nh != "goo.gl" {
			return current, nil
		}
	}
	return current, errors.New("too many redirects")
}

func parseFloatPair(a, b string) (*float64, *float64) {
	x, err1 := strconv.ParseFloat(a, 64)
	y, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil {
		return nil, nil
	}
	return &x, &y
}

// splitAddress guesses city and country from "street, 08001 City, Country".
func splitAddress(addr string) (city, country string) {
	parts := strings.Split(addr, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	if len(parts) >= 2 {
		country = parts[len(parts)-1]
		cityPart := parts[len(parts)-2]
		fields := strings.Fields(cityPart)
		kept := []string{}
		for _, f := range fields {
			if strings.IndexFunc(f, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0 {
				continue // drop postal codes
			}
			kept = append(kept, f)
		}
		city = strings.Join(kept, " ")
	}
	return city, country
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
