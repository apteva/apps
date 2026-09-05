package main

import (
	"database/sql"
	"errors"
	"strings"
)

func validateEffectivePolicies(db *sql.DB, api *API, replacement *APIRoute, remove int64) error {
	if err := validateAuthPolicy(api.AuthJSON); err != nil {
		return err
	}
	if _, err := parseEffectiveCORSPolicy(api.CORSJSON, "{}"); err != nil {
		return err
	}
	routes, err := dbListRoutes(db, api.ProjectID, api.ID)
	if err != nil {
		return err
	}
	origins := map[string]bool{}
	validate := func(route *APIRoute) error {
		kind, err := effectiveAuthKind(api.AuthJSON, route.AuthJSON)
		if err != nil {
			return err
		}
		if route.TargetKind == "app_events" && (kind != "api_key" && kind != "auth_jwt") {
			return errors.New("app_events routes require api_key or auth_jwt authentication")
		}
		cors, err := parseEffectiveCORSPolicy(api.CORSJSON, route.CORSJSON)
		if err != nil {
			return err
		}
		if route.Enabled && cors.Enabled {
			for _, origin := range cors.Origins {
				origins[origin] = true
			}
		}
		return nil
	}
	for _, route := range routes {
		if route.ID == remove {
			continue
		}
		if replacement != nil && route.Method == replacement.Method && route.PathPattern == replacement.PathPattern {
			continue
		}
		if err := validate(route); err != nil {
			return err
		}
	}
	if replacement != nil {
		if err := validate(replacement); err != nil {
			return err
		}
	}
	if len(origins) > 100 {
		return errors.New("effective API CORS policy exceeds 100 origins")
	}
	return nil
}
func validateRouteTargetPath(path string) error {
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\\\r\n") {
		return errors.New("target_path must be an absolute path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return errors.New("target_path cannot traverse directories")
		}
	}
	return nil
}
