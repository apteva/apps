package main

import (
	"errors"
	"net/http"
	"strings"
)

// httpPortalBootstrap returns the public, non-secret settings needed to boot
// one branded community portal. Auth client IDs and organization slugs are
// public routing identifiers; the portal consumes them automatically and
// never asks a customer to enter them.
func (a *App) httpPortalBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	projectID := scopeProject(globalCtx)
	if projectID == "" {
		writeErr(w, http.StatusBadRequest, "project context is unavailable")
		return
	}
	slug := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("community")))
	if slug == "" && globalCtx.Config() != nil {
		slug = strings.TrimSpace(strings.ToLower(globalCtx.Config().Get("default_community_slug")))
	}
	if slug == "" {
		writeErr(w, http.StatusBadRequest, "community is required")
		return
	}
	community, err := loadCommunityBySlug(globalCtx.AppDB(), projectID, slug)
	if err != nil || community.ArchivedAt != nil {
		writeDomainErr(w, errors.New("community not found"))
		return
	}
	if community.AuthClientID == "" {
		writeErr(w, http.StatusServiceUnavailable, "this community's sign-in is not configured")
		return
	}
	brandName := strings.TrimSpace(community.BrandName)
	if brandName == "" {
		brandName = community.Name
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{
		"community": map[string]any{
			"id": community.ID, "slug": community.Slug, "name": community.Name,
			"description": community.Description,
		},
		"brand": map[string]any{
			"name": brandName, "logo_url": community.LogoURL, "favicon_url": community.FaviconURL,
			"primary_color": community.PrimaryColor, "accent_color": community.AccentColor,
			"support_email": community.SupportEmail,
		},
		"auth": map[string]any{
			"client_id": community.AuthClientID, "organization_slug": community.AuthOrganizationSlug,
		},
		"signup": map[string]any{
			"enabled": community.SignupMode == "open" && community.AutoCreateMembers,
		},
	})
}
