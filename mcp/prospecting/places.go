package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type discoverySettings struct {
	PlacesConnectionID      int64 `json:"places_connection_id"`
	DailyPlacesRequestLimit int   `json:"daily_places_request_limit"`
}

type placeDetails struct {
	ID          string `json:"id"`
	DisplayName struct {
		Text string `json:"text"`
	} `json:"displayName"`
	Address        string   `json:"formattedAddress"`
	Website        string   `json:"websiteUri"`
	Phone          string   `json:"internationalPhoneNumber"`
	NationalPhone  string   `json:"nationalPhoneNumber"`
	BusinessStatus string   `json:"businessStatus"`
	PrimaryType    string   `json:"primaryType"`
	MapsURI        string   `json:"googleMapsUri"`
	Rating         *float64 `json:"rating,omitempty"`
	ReviewCount    *int     `json:"userRatingCount,omitempty"`
	Location       struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
	Attributions []map[string]any `json:"attributions,omitempty"`
}

func loadDiscoverySettings(ctx *sdk.AppCtx) (discoverySettings, error) {
	s := discoverySettings{DailyPlacesRequestLimit: 100}
	err := ctx.AppDB().QueryRow(`SELECT places_connection_id,daily_places_request_limit FROM prospecting_settings WHERE project_id=?`, ctx.CurrentProject()).Scan(&s.PlacesConnectionID, &s.DailyPlacesRequestLimit)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return s, err
}

func (a *App) toolDiscoverySettings(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := loadDiscoverySettings(ctx)
	if err != nil {
		return nil, err
	}
	if len(args) > 0 {
		if _, ok := args["places_connection_id"]; ok {
			s.PlacesConnectionID = int64Arg(args, "places_connection_id")
			if s.PlacesConnectionID < 0 {
				return nil, errors.New("connection id must be nonnegative")
			}
			if s.PlacesConnectionID > 0 {
				if err = validatePlacesConnection(ctx, s.PlacesConnectionID); err != nil {
					return nil, err
				}
			}
		}
		if _, ok := args["daily_places_request_limit"]; ok {
			s.DailyPlacesRequestLimit = int(int64Arg(args, "daily_places_request_limit"))
			if s.DailyPlacesRequestLimit < 1 || s.DailyPlacesRequestLimit > 1000 {
				return nil, errors.New("daily request limit must be between 1 and 1000")
			}
		}
		_, err = ctx.AppDB().Exec(`INSERT INTO prospecting_settings VALUES (?,?,?) ON CONFLICT(project_id) DO UPDATE SET places_connection_id=excluded.places_connection_id,daily_places_request_limit=excluded.daily_places_request_limit`, ctx.CurrentProject(), s.PlacesConnectionID, s.DailyPlacesRequestLimit)
	}
	return s, err
}

func validatePlacesConnection(ctx *sdk.AppCtx, id int64) error {
	c, err := ctx.PlatformAPI().GetConnection(id)
	if err != nil {
		return fmt.Errorf("Google Places connection: %w", err)
	}
	if c == nil || c.AppSlug != "google-places" || (c.ProjectID != "" && c.ProjectID != ctx.CurrentProject()) {
		return errors.New("select an accessible Google Places connection for this project")
	}
	if c.Status != "active" && c.Status != "connected" {
		return errors.New("Google Places connection is not active")
	}
	return nil
}

func (a *App) toolPlacesConnections(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	cs, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: "google-places"})
	out := []sdk.PlatformConnection{}
	for _, c := range cs {
		if c.ProjectID == "" || c.ProjectID == ctx.CurrentProject() {
			out = append(out, c)
		}
	}
	return map[string]any{"connections": out}, err
}

// Reservations happen before remote calls, so failures and retries also consume
// the bounded request allowance. No credential ever enters the app's database.
func reservePlacesRequest(ctx *sdk.AppCtx) error {
	s, err := loadDiscoverySettings(ctx)
	if err != nil {
		return err
	}
	r, err := ctx.AppDB().Exec(`INSERT INTO places_usage VALUES (?,?,1) ON CONFLICT(project_id,day) DO UPDATE SET requests=requests+1 WHERE requests<?`, ctx.CurrentProject(), time.Now().UTC().Format("2006-01-02"), s.DailyPlacesRequestLimit)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("daily Google Places request limit reached")
	}
	return nil
}

func callPlaces(c context.Context, ctx *sdk.AppCtx, id int64, tool string, input map[string]any, out any) error {
	if err := reservePlacesRequest(ctx); err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(c, 45*time.Second)
	defer cancel()
	r, err := sdk.ExecuteIntegrationToolContext(deadline, ctx.PlatformAPI(), id, tool, input)
	if err != nil {
		return fmt.Errorf("Google Places %s: %w", tool, err)
	}
	if r == nil || !r.Success {
		return fmt.Errorf("Google Places %s failed", tool)
	}
	if err = json.Unmarshal(r.Data, out); err != nil {
		return fmt.Errorf("Google Places response: %w", err)
	}
	return nil
}

const placesSearchFields = "places.id,places.displayName,places.formattedAddress,places.websiteUri,places.internationalPhoneNumber,places.businessStatus,places.primaryType,places.googleMapsUri,places.location,places.attributions,nextPageToken"

func mapsPlaceURL(id string) string {
	return "https://www.google.com/maps/search/?api=1&query=business&query_place_id=" + url.QueryEscape(id)
}

func candidatePlace(db *sql.DB, pid string, id int64) (map[string]any, error) {
	var raw, at string
	err := db.QueryRow(`SELECT details_json,fetched_at FROM candidate_places WHERE project_id=? AND candidate_id=?`, pid, id).Scan(&raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p placeDetails
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return map[string]any{"details": p, "fetched_at": at, "attribution": "Google Maps", "phone_kind": "business"}, nil
}

func ingestPlace(ctx *sdk.AppCtx, profile *TargetProfile, p placeDetails) (*Candidate, bool, string, error) {
	if p.ID == "" || strings.TrimSpace(p.DisplayName.Text) == "" {
		return nil, false, "invalid place identity", nil
	}
	if p.BusinessStatus != "" && p.BusinessStatus != "OPERATIONAL" {
		return nil, false, "business is temporarily or permanently closed", nil
	}
	website, domain := normalizeWebsite(p.Website)
	input := candidateInput{ProfileID: profile.ID, CompanyName: p.DisplayName.Text, CompanyDomain: domain, Website: website, Phone: normalizeQualifiedPhone(defaultString(p.Phone, p.NationalPhone), profile.Locations), Source: "google_places", SourceURL: defaultString(p.MapsURI, mapsPlaceURL(p.ID)), CanonicalKey: "place:" + p.ID}
	blocked, err := isExcluded(ctx.AppDB(), ctx.CurrentProject(), input)
	if err != nil {
		return nil, false, "", err
	}
	if blocked {
		return nil, false, "matches an exclusion", nil
	}
	db, pid := ctx.AppDB(), ctx.CurrentProject()
	var existingID int64
	err = db.QueryRow(`SELECT candidate_id FROM candidate_places WHERE project_id=? AND profile_id=? AND place_id=?`, pid, profile.ID, p.ID).Scan(&existingID)
	var candidate *Candidate
	created := false
	if err == nil {
		candidate, err = getCandidate(db, pid, existingID)
	} else if errors.Is(err, sql.ErrNoRows) {
		// Only reconcile an unassigned Web/manual lead when its phone agrees
		// or it is the sole company-domain lead. Once assigned, a second Place
		// ID creates a distinct branch instead of collapsing a chain's sites.
		if domain != "" {
			rows, e := db.Query(candidateSelect+` WHERE project_id=? AND profile_id=? AND company_domain=? AND id NOT IN (SELECT candidate_id FROM candidate_places WHERE project_id=?)`, pid, profile.ID, domain, pid)
			if e != nil {
				return nil, false, "", e
			}
			matches := []*Candidate{}
			for rows.Next() {
				c, e := scanCandidateRow(rows)
				if e != nil {
					rows.Close()
					return nil, false, "", e
				}
				matches = append(matches, c)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, false, "", e
			}
			for _, c := range matches {
				if input.Phone != "" && normalizeQualifiedPhone(c.Phone, profile.Locations) == input.Phone {
					candidate = c
					break
				}
			}
			if candidate == nil && len(matches) == 1 && matches[0].Phone == "" {
				candidate = matches[0]
			}
		}
		if candidate == nil {
			candidate, created, err = insertCandidate(db, pid, input, profile)
		} else {
			err = nil
		}
	}
	if err != nil {
		return nil, false, "", err
	}
	if candidate == nil {
		return nil, false, "", errors.New("candidate unavailable")
	}
	if candidate.Status == "rejected" || candidate.Status == "deferred" {
		return candidate, false, "existing prospect is " + candidate.Status, nil
	}
	_, err = db.Exec(`INSERT INTO candidate_places VALUES (?,?,?,?,?,?) ON CONFLICT(project_id,profile_id,place_id) DO UPDATE SET details_json=excluded.details_json,fetched_at=excluded.fetched_at`, pid, profile.ID, p.ID, candidate.ID, mustJSON(p), nowUTC())
	if err != nil {
		return nil, false, "", err
	}
	if created {
		_, err = db.Exec(`UPDATE candidates SET location=? WHERE project_id=? AND id=?`, p.Address, pid, candidate.ID)
		if err != nil {
			return nil, false, "", err
		}
		err = addEvidence(db, pid, Evidence{CandidateID: candidate.ID, SourceKind: "google_places", Title: p.DisplayName.Text, URL: input.SourceURL, Excerpt: "Google Maps business listing: " + p.Address + ". Business phone: " + input.Phone + ". Category: " + p.PrimaryType, RetrievedAt: nowUTC()})
		if err != nil {
			return nil, false, "", err
		}
		candidate, err = rescoreCandidate(db, pid, candidate.ID)
		if err != nil {
			return nil, false, "", err
		}
		ctx.EmitWithProject("prospecting.candidate.created", pid, candidateEvent(candidate))
	}
	return candidate, created, "", err
}

// A Web result identifies a company domain, whereas Places identifies a
// location. Never create a redundant company lead when locations for that
// domain are already represented; do not arbitrarily merge distinct branches.
func existingPlaceCompany(db *sql.DB, pid string, profileID int64, domain string) (*Candidate, bool, error) {
	if domain == "" {
		return nil, false, nil
	}
	rows, err := db.Query(candidateSelect+` WHERE project_id=? AND profile_id=? AND company_domain=? AND id IN (SELECT candidate_id FROM candidate_places WHERE project_id=?) ORDER BY id LIMIT 2`, pid, profileID, domain, pid)
	if err != nil {
		return nil, false, err
	}
	matches := []*Candidate{}
	for rows.Next() {
		c, e := scanCandidateRow(rows)
		if e != nil {
			rows.Close()
			return nil, false, e
		}
		matches = append(matches, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	if len(matches) == 1 {
		return matches[0], false, nil
	}
	return nil, len(matches) > 1, nil
}
