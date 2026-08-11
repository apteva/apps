package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// PublicProduct is the deliberately small, public projection of a Catalog
// product that is actively bound to this community. Catalog remains the
// source of truth for product presentation; Community owns only the mapping
// from a price to the access it grants.
type PublicProduct struct {
	CatalogProductID int64                      `json:"catalog_product_id"`
	Slug             string                     `json:"slug"`
	Name             string                     `json:"name"`
	Description      string                     `json:"description,omitempty"`
	Type             string                     `json:"type"`
	Category         string                     `json:"category,omitempty"`
	Color            string                     `json:"color,omitempty"`
	ImageFileID      *int64                     `json:"image_file_id,omitempty"`
	Storefront       PublicStorefrontContent    `json:"storefront"`
	Offers           []PublicOffer              `json:"offers"`
	Courses          []PublicCourse             `json:"courses"`
	Testimonials     []PublicProductTestimonial `json:"testimonials"`
}

type PublicStorefrontContent struct {
	Headline    string             `json:"headline,omitempty"`
	Eyebrow     string             `json:"eyebrow,omitempty"`
	Included    []PublicBenefit    `json:"included"`
	Testimonial *PublicTestimonial `json:"testimonial,omitempty"`
}

type PublicBenefit struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

type PublicTestimonial struct {
	Quote        string `json:"quote"`
	Name         string `json:"name,omitempty"`
	Role         string `json:"role,omitempty"`
	AvatarFileID *int64 `json:"avatar_file_id,omitempty"`
}

type PublicOffer struct {
	CatalogPriceID  int64    `json:"catalog_price_id"`
	Kind            string   `json:"kind"`
	Name            string   `json:"name"`
	UnitAmountCents int64    `json:"unit_amount_cents"`
	Currency        string   `json:"currency"`
	Interval        string   `json:"interval,omitempty"`
	IntervalCount   int64    `json:"interval_count,omitempty"`
	TrialDays       int64    `json:"trial_days,omitempty"`
	ScopeType       string   `json:"scope_type"`
	CourseSlugs     []string `json:"course_slugs"`
}

type PublicCourse struct {
	Slug           string                       `json:"slug"`
	Name           string                       `json:"name"`
	PreviewLessons []PublicLessonPreviewSummary `json:"preview_lessons"`
	Instructors    []PublicInstructorProfile    `json:"instructors"`
}

type publicCourseOfferBinding struct {
	productID, priceID, amount  int64
	nickname, currency, spaceID string
}

func publicProductSlug(product catalogProduct) string {
	if slug := strings.TrimSpace(product.Slug); slug != "" {
		return slug
	}
	return fmt.Sprintf("product-%d", product.ID)
}

func publicStorefrontContent(product catalogProduct) PublicStorefrontContent {
	content := PublicStorefrontContent{Included: []PublicBenefit{}}
	if len(product.Metadata) == 0 {
		return content
	}
	var metadata struct {
		Storefront PublicStorefrontContent `json:"storefront"`
	}
	if json.Unmarshal(product.Metadata, &metadata) != nil {
		return content
	}
	metadata.Storefront.Headline = strings.TrimSpace(metadata.Storefront.Headline)
	metadata.Storefront.Eyebrow = strings.TrimSpace(metadata.Storefront.Eyebrow)
	clean := make([]PublicBenefit, 0, len(metadata.Storefront.Included))
	for _, benefit := range metadata.Storefront.Included {
		benefit.Title = strings.TrimSpace(benefit.Title)
		benefit.Description = strings.TrimSpace(benefit.Description)
		if benefit.Title != "" {
			clean = append(clean, benefit)
		}
	}
	metadata.Storefront.Included = clean
	if metadata.Storefront.Testimonial != nil {
		metadata.Storefront.Testimonial.Quote = strings.TrimSpace(metadata.Storefront.Testimonial.Quote)
		metadata.Storefront.Testimonial.Name = strings.TrimSpace(metadata.Storefront.Testimonial.Name)
		metadata.Storefront.Testimonial.Role = strings.TrimSpace(metadata.Storefront.Testimonial.Role)
		if metadata.Storefront.Testimonial.Quote == "" {
			metadata.Storefront.Testimonial = nil
		}
	}
	return metadata.Storefront
}

func (a *App) httpPortalProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	community, err := publicPortalCommunity(r)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	products, err := buildPublicProducts(globalCtx, community.ID)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "storefront is temporarily unavailable")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	writeJSON(w, map[string]any{"products": products, "count": len(products)})
}

func (a *App) httpPortalProduct(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/portal/products/"), "/")
	if slug == "" || strings.Contains(slug, "/") {
		writeErr(w, http.StatusBadRequest, "product slug is required")
		return
	}
	community, err := publicPortalCommunity(r)
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	products, err := buildPublicProducts(globalCtx, community.ID)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "storefront is temporarily unavailable")
		return
	}
	for i := range products {
		if products[i].Slug == slug {
			w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
			writeJSON(w, map[string]any{"product": products[i]})
			return
		}
	}
	writeErr(w, http.StatusNotFound, "product not found")
}

// httpStorefrontRoute provides generic, human-readable aliases without
// coupling the source app to a particular customer or course:
// /store/{community}/products/{product} and /store/{community}/checkout/{product}.
func (a *App) httpStorefrontRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/store/"), "/"), "/")
	if len(parts) != 3 || (parts[1] != "products" && parts[1] != "checkout") || !slugRE.MatchString(parts[0]) || parts[2] == "" {
		writeErr(w, http.StatusNotFound, "storefront route not found")
		return
	}
	query := url.Values{}
	query.Set("community", parts[0])
	query.Set("product", parts[2])
	if parts[1] == "checkout" {
		query.Set("intent", "buy")
	}
	for _, key := range []string{"offer", "project_id"} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			query.Set(key, value)
		}
	}
	if query.Get("project_id") == "" && globalCtx != nil {
		query.Set("project_id", scopeProject(globalCtx))
	}
	uiPath := "/ui/portal/dist/index.html"
	if installID := strings.TrimSpace(r.Header.Get("X-Apteva-App-Install-ID")); installID != "" {
		if _, err := strconv.ParseInt(installID, 10, 64); err == nil {
			appName := "community"
			if globalCtx != nil && globalCtx.Manifest() != nil && globalCtx.Manifest().Name != "" {
				appName = globalCtx.Manifest().Name
			}
			uiPath = "/api/apps/" + url.PathEscape(appName) + "/_install/" + installID + uiPath
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, uiPath+"?"+query.Encode(), http.StatusSeeOther)
}

func publicPortalCommunity(r *http.Request) (Community, error) {
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return Community{}, errors.New("community app is unavailable")
	}
	projectID := scopeProject(globalCtx)
	if projectID == "" {
		return Community{}, errors.New("project context is unavailable")
	}
	slug := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("community")))
	if slug == "" && globalCtx.Config() != nil {
		slug = strings.TrimSpace(strings.ToLower(globalCtx.Config().Get("default_community_slug")))
	}
	if slug == "" {
		return Community{}, errors.New("community is required")
	}
	community, err := loadCommunityBySlug(globalCtx.AppDB(), projectID, slug)
	if err != nil || community.ArchivedAt != nil {
		return Community{}, errors.New("community not found")
	}
	return community, nil
}

func buildPublicProducts(ctx *sdk.AppCtx, communityID string) ([]PublicProduct, error) {
	bindings, err := publicCourseOfferBindings(ctx.AppDB(), communityID)
	if err != nil {
		return nil, err
	}
	plans, err := listMembershipPlans(ctx.AppDB(), communityID, false)
	if err != nil {
		return nil, err
	}
	productIDs := map[int64]struct{}{}
	for _, binding := range bindings {
		productIDs[binding.productID] = struct{}{}
	}
	for _, plan := range plans {
		productIDs[plan.CatalogProductID] = struct{}{}
	}
	catalog := make(map[int64]catalogProduct, len(productIDs))
	for id := range productIDs {
		var out struct {
			Product catalogProduct `json:"product"`
		}
		if err := callAppResult(ctx, "catalog", "catalog_products_get", map[string]any{
			"_project_id": scopeProject(ctx), "id": id,
		}, &out); err != nil {
			return nil, fmt.Errorf("get Catalog product %d: %w", id, err)
		}
		if out.Product.ID != 0 && out.Product.ArchivedAt == "" {
			catalog[id] = out.Product
		}
	}

	products := map[int64]*PublicProduct{}
	courses := map[string]PublicCourse{}
	courseFor := func(spaceID string) (PublicCourse, error) {
		if course, ok := courses[spaceID]; ok {
			return course, nil
		}
		course, err := loadPublicCourse(ctx.AppDB(), spaceID)
		if err == nil {
			courses[spaceID] = course
		}
		return course, err
	}
	ensureProduct := func(id int64) *PublicProduct {
		if existing := products[id]; existing != nil {
			return existing
		}
		product, ok := catalog[id]
		if !ok {
			return nil
		}
		created := &PublicProduct{
			CatalogProductID: id, Slug: publicProductSlug(product), Name: product.Name,
			Description: product.Description, Type: product.Type, Category: product.Category,
			Color: product.Color, ImageFileID: product.ImageFileID, Storefront: publicStorefrontContent(product),
			Offers: []PublicOffer{}, Courses: []PublicCourse{}, Testimonials: []PublicProductTestimonial{},
		}
		products[id] = created
		return created
	}

	for _, binding := range bindings {
		product := ensureProduct(binding.productID)
		if product == nil {
			continue
		}
		course, err := courseFor(binding.spaceID)
		if err != nil {
			return nil, err
		}
		product.Offers = append(product.Offers, PublicOffer{
			CatalogPriceID: binding.priceID, Kind: "one_time", Name: firstNonEmptyString(binding.nickname, product.Name),
			UnitAmountCents: binding.amount, Currency: binding.currency, ScopeType: "selected_courses",
			CourseSlugs: []string{course.Slug},
		})
		appendPublicCourse(product, course)
	}
	for _, plan := range plans {
		product := ensureProduct(plan.CatalogProductID)
		if product == nil {
			continue
		}
		spaceIDs, err := publicPlanCourseIDs(ctx.AppDB(), plan)
		if err != nil {
			return nil, err
		}
		slugs := make([]string, 0, len(spaceIDs))
		for _, spaceID := range spaceIDs {
			course, err := courseFor(spaceID)
			if err != nil {
				return nil, err
			}
			slugs = append(slugs, course.Slug)
			appendPublicCourse(product, course)
		}
		product.Offers = append(product.Offers, PublicOffer{
			CatalogPriceID: plan.CatalogPriceID, Kind: "recurring", Name: plan.Name,
			UnitAmountCents: plan.UnitAmountCents, Currency: plan.Currency,
			Interval: plan.Interval, IntervalCount: plan.IntervalCount, TrialDays: plan.TrialDays,
			ScopeType: plan.ScopeType, CourseSlugs: slugs,
		})
	}

	out := make([]PublicProduct, 0, len(products))
	for _, product := range products {
		if testimonials, err := publicProductTestimonials(ctx, communityID, product.CatalogProductID); err == nil {
			product.Testimonials = testimonials
			if len(testimonials) > 0 {
				// Catalog's legacy single testimonial remains a compatibility
				// fallback only. Curated Testimonials proof is the sole rendered
				// source once a product has explicit assignments.
				product.Storefront.Testimonial = nil
			}
		}
		sort.Slice(product.Offers, func(i, j int) bool {
			if product.Offers[i].Kind != product.Offers[j].Kind {
				return product.Offers[i].Kind < product.Offers[j].Kind
			}
			return product.Offers[i].UnitAmountCents < product.Offers[j].UnitAmountCents
		})
		sort.Slice(product.Courses, func(i, j int) bool { return product.Courses[i].Name < product.Courses[j].Name })
		out = append(out, *product)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func publicCourseOfferBindings(db *sql.DB, communityID string) ([]publicCourseOfferBinding, error) {
	rows, err := db.Query(`SELECT co.catalog_product_id,co.catalog_price_id,co.unit_amount_cents,
		co.price_nickname,co.currency,s.id
		FROM course_offers co JOIN spaces s ON s.id=co.space_id
		WHERE s.community_id=? AND s.kind='course' AND s.archived_at IS NULL
		AND co.active=1 AND co.archived_at IS NULL ORDER BY co.updated_at DESC`, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []publicCourseOfferBinding{}
	for rows.Next() {
		var item publicCourseOfferBinding
		if err := rows.Scan(&item.productID, &item.priceID, &item.amount, &item.nickname, &item.currency, &item.spaceID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func loadPublicCourse(db *sql.DB, spaceID string) (PublicCourse, error) {
	course := PublicCourse{PreviewLessons: []PublicLessonPreviewSummary{}, Instructors: []PublicInstructorProfile{}}
	if err := db.QueryRow(`SELECT slug,name FROM spaces WHERE id=? AND kind='course' AND archived_at IS NULL`, spaceID).Scan(&course.Slug, &course.Name); err != nil {
		return course, err
	}
	previews, err := loadPublicLessonPreviewSummaries(db, spaceID)
	if err != nil {
		return course, err
	}
	course.PreviewLessons = previews
	instructors, err := publicInstructorsForCourse(db, spaceID)
	if err != nil {
		return course, err
	}
	course.Instructors = instructors
	return course, nil
}

func publicPlanCourseIDs(db *sql.DB, plan *MembershipPlan) ([]string, error) {
	if plan.ScopeType == "selected_courses" {
		return append([]string(nil), plan.CourseIDs...), nil
	}
	all, err := queryStrings(db, `SELECT id FROM spaces WHERE community_id=? AND kind='course' AND archived_at IS NULL ORDER BY sort_order,name`, plan.CommunityID)
	if err != nil || plan.ScopeType == "all_courses" {
		return all, err
	}
	wanted := map[string]bool{}
	for _, tag := range plan.Tags {
		wanted[strings.ToLower(strings.TrimSpace(tag))] = true
	}
	filtered := []string{}
	for _, spaceID := range all {
		details, err := loadCourseDetails(db, spaceID)
		if err != nil {
			return nil, err
		}
		for _, tag := range details.Tags {
			if wanted[strings.ToLower(strings.TrimSpace(tag))] {
				filtered = append(filtered, spaceID)
				break
			}
		}
	}
	return filtered, nil
}

func appendPublicCourse(product *PublicProduct, course PublicCourse) {
	for _, existing := range product.Courses {
		if existing.Slug == course.Slug {
			return
		}
	}
	product.Courses = append(product.Courses, course)
}

func storefrontTools() []sdk.Tool {
	return []sdk.Tool{{
		Name:        "storefront_checkout_start",
		Description: "Start checkout for a verified member using a public Catalog price bound to this community.",
		InputSchema: schemaObject(map[string]any{
			"community_id":     map[string]any{"type": "string"},
			"catalog_price_id": map[string]any{"type": "integer"},
			"member_id":        map[string]any{"type": "string"},
			"success_url":      map[string]any{"type": "string"},
			"cancel_url":       map[string]any{"type": "string"},
			"return_url":       map[string]any{"type": "string"},
			"presentation":     map[string]any{"type": "string", "enum": []string{"hosted", "elements"}},
		}, []string{"community_id", "catalog_price_id", "member_id"}),
		Handler: toolStorefrontCheckoutStart,
	}, {
		Name:        "storefront_checkout_claim",
		Description: "Claim a Checkout app guest session for the verified member before confirming payment.",
		InputSchema: schemaObject(map[string]any{
			"community_id":     map[string]any{"type": "string"},
			"catalog_price_id": map[string]any{"type": "integer"},
			"member_id":        map[string]any{"type": "string"},
			"recovery_token":   map[string]any{"type": "string"},
		}, []string{"community_id", "catalog_price_id", "member_id", "recovery_token"}),
		Handler: toolStorefrontCheckoutClaim,
	}}
}

func toolStorefrontCheckoutStart(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	community, err := loadCommunity(ctx.AppDB(), communityID)
	if err != nil || community.ProjectID != scopeProject(ctx) || community.ArchivedAt != nil {
		return nil, errors.New("community not found")
	}
	priceID, ok := intArg(args, "catalog_price_id")
	if !ok || priceID <= 0 {
		return nil, errors.New("catalog_price_id must be a positive integer")
	}
	forward := map[string]any{
		"member_id":         strArg(args, "member_id", ""),
		"success_url":       strArg(args, "success_url", ""),
		"cancel_url":        strArg(args, "cancel_url", ""),
		"_viewer_member_id": strArg(args, "_viewer_member_id", ""),
		"_auth_subject_id":  strArg(args, "_auth_subject_id", ""),
		"_subject_email":    strArg(args, "_subject_email", ""),
		"presentation":      strArg(args, "presentation", "hosted"),
		"return_url":        strArg(args, "return_url", ""),
	}
	var spaceID string
	err = ctx.AppDB().QueryRow(`SELECT s.id FROM course_offers co JOIN spaces s ON s.id=co.space_id
		WHERE s.community_id=? AND s.kind='course' AND s.archived_at IS NULL
		AND co.catalog_price_id=? AND co.active=1 AND co.archived_at IS NULL`, communityID, priceID).Scan(&spaceID)
	if err == nil {
		forward["space_id"] = spaceID
		out, callErr := toolCoursePurchaseStart(ctx, forward)
		if result, ok := out.(map[string]any); ok {
			result["offer_kind"] = "one_time"
		}
		return out, callErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var planID string
	err = ctx.AppDB().QueryRow(`SELECT id FROM membership_plans WHERE community_id=? AND catalog_price_id=?
		AND active=1 AND archived_at IS NULL`, communityID, priceID).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("this product offer is not available")
	}
	if err != nil {
		return nil, err
	}
	forward["plan_id"] = planID
	out, callErr := toolMembershipCheckoutStart(ctx, forward)
	if result, ok := out.(map[string]any); ok {
		result["offer_kind"] = "recurring"
	}
	return out, callErr
}
