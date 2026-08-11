package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const maxProductTestimonials = 20

type testimonialsAppRecord struct {
	ID              int64    `json:"id"`
	Status          string   `json:"status"`
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	Quote           string   `json:"quote"`
	Body            string   `json:"body"`
	Rating          *int     `json:"rating,omitempty"`
	AuthorName      string   `json:"author_name"`
	AuthorRole      string   `json:"author_role"`
	AuthorCompany   string   `json:"author_company"`
	MediaFileID     string   `json:"media_file_id"`
	MediaURL        string   `json:"media_url"`
	ConsentStatus   string   `json:"consent_status"`
	PermissionScope string   `json:"permission_scope"`
	Tags            []string `json:"tags"`
	PublishedAt     string   `json:"published_at"`
}

// PublicProductTestimonial is the deliberately small projection Community
// places on a public product. Private author email, arbitrary metadata,
// consent internals, and unpublished proof never cross this boundary.
type PublicProductTestimonial struct {
	ID                  int64  `json:"id"`
	Kind                string `json:"kind"`
	Quote               string `json:"quote"`
	Result              string `json:"result,omitempty"`
	StudentName         string `json:"student_name,omitempty"`
	Role                string `json:"role,omitempty"`
	Rating              *int   `json:"rating,omitempty"`
	AvatarStorageFileID string `json:"avatar_storage_file_id,omitempty"`
	MediaURL            string `json:"media_url,omitempty"`
	Verified            bool   `json:"verified"`
	VerificationStatus  string `json:"verification_status"`
}

type ProductTestimonialsResult struct {
	CommunityID           string                     `json:"community_id"`
	CatalogProductID      int64                      `json:"catalog_product_id"`
	TestimonialIDs        []int64                    `json:"testimonial_ids"`
	Testimonials          []PublicProductTestimonial `json:"testimonials"`
	AvailableTestimonials []PublicProductTestimonial `json:"available_testimonials"`
	TestimonialsAvailable bool                       `json:"testimonials_available"`
	UnavailableReason     string                     `json:"unavailable_reason,omitempty"`
}

func productTestimonialTools() []sdk.Tool {
	ids := map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "maxItems": maxProductTestimonials}
	base := map[string]any{
		"community_id":       map[string]any{"type": "string"},
		"catalog_product_id": map[string]any{"type": "integer"},
	}
	setFields := map[string]any{
		"community_id":       base["community_id"],
		"catalog_product_id": base["catalog_product_id"],
		"testimonial_ids":    ids,
	}
	return []sdk.Tool{
		{
			Name:        "product_testimonials_get",
			Description: "Get the ordered published Testimonials proof curated for one Community storefront product, plus published proof available for assignment.",
			InputSchema: schemaObject(base, []string{"community_id", "catalog_product_id"}),
			Handler:     toolProductTestimonialsGet,
		},
		{
			Name:        "product_testimonials_set",
			Description: "Set ordered Testimonials IDs for a Catalog product actively offered by this community. Every item must be published with granted consent and public or marketing permission.",
			InputSchema: schemaObject(setFields, []string{"community_id", "catalog_product_id", "testimonial_ids"}),
			Handler:     toolProductTestimonialsSet,
		},
	}
}

func toolProductTestimonialsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, productID, err := productTestimonialTarget(ctx, args)
	if err != nil {
		return nil, err
	}
	return productTestimonialsResult(ctx, communityID, productID), nil
}

func toolProductTestimonialsSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, productID, err := productTestimonialTarget(ctx, args)
	if err != nil {
		return nil, err
	}
	ids, err := testimonialIDArrayArg(args["testimonial_ids"])
	if err != nil {
		return nil, err
	}
	if len(ids) > maxProductTestimonials {
		return nil, fmt.Errorf("a product can show at most %d testimonials", maxProductTestimonials)
	}
	for _, id := range ids {
		record, err := getTestimonialsAppRecord(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("testimonial %d: %w", id, err)
		}
		if !eligibleProductTestimonial(record) {
			return nil, fmt.Errorf("testimonial %d must be published with granted consent and public or marketing permission", id)
		}
	}
	encoded, _ := json.Marshal(ids)
	if _, err := ctx.AppDB().Exec(`INSERT INTO storefront_product_profiles
		(community_id,catalog_product_id,testimonial_ids_json) VALUES(?,?,?)
		ON CONFLICT(community_id,catalog_product_id) DO UPDATE SET
		testimonial_ids_json=excluded.testimonial_ids_json,updated_at=CURRENT_TIMESTAMP`,
		communityID, productID, string(encoded)); err != nil {
		return nil, err
	}
	emit(ctx, "product.testimonials_updated", map[string]any{
		"community_id": communityID, "catalog_product_id": productID, "testimonial_ids": ids,
	})
	return productTestimonialsResult(ctx, communityID, productID), nil
}

func productTestimonialTarget(ctx *sdk.AppCtx, args map[string]any) (string, int64, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return "", 0, err
	}
	community, err := loadCommunity(ctx.AppDB(), communityID)
	if err != nil || ensureCommunityReadable(ctx, community) != nil {
		return "", 0, errors.New("community not found")
	}
	productID, ok := intArg(args, "catalog_product_id")
	if !ok || productID <= 0 {
		return "", 0, errors.New("catalog_product_id must be a positive integer")
	}
	if !communityOffersCatalogProduct(ctx.AppDB(), communityID, productID) {
		return "", 0, errors.New("Catalog product is not actively offered by this community")
	}
	return communityID, productID, nil
}

func communityOffersCatalogProduct(db *sql.DB, communityID string, productID int64) bool {
	var count int
	err := db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM course_offers co JOIN spaces s ON s.id=co.space_id
		 WHERE s.community_id=? AND s.archived_at IS NULL AND co.catalog_product_id=?
		 AND co.active=1 AND co.archived_at IS NULL) +
		(SELECT COUNT(*) FROM membership_plans mp
		 WHERE mp.community_id=? AND mp.catalog_product_id=? AND mp.active=1 AND mp.archived_at IS NULL)`,
		communityID, productID, communityID, productID).Scan(&count)
	return err == nil && count > 0
}

func loadProductTestimonialIDs(db *sql.DB, communityID string, productID int64) ([]int64, error) {
	var raw string
	err := db.QueryRow(`SELECT testimonial_ids_json FROM storefront_product_profiles
		WHERE community_id=? AND catalog_product_id=?`, communityID, productID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return []int64{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func productTestimonialsResult(ctx *sdk.AppCtx, communityID string, productID int64) ProductTestimonialsResult {
	result := ProductTestimonialsResult{
		CommunityID: communityID, CatalogProductID: productID, TestimonialIDs: []int64{},
		Testimonials: []PublicProductTestimonial{}, AvailableTestimonials: []PublicProductTestimonial{},
	}
	ids, err := loadProductTestimonialIDs(ctx.AppDB(), communityID, productID)
	if err != nil {
		result.UnavailableReason = err.Error()
		return result
	}
	result.TestimonialIDs = ids
	selected, err := publicProductTestimonials(ctx, communityID, productID)
	if err == nil {
		result.Testimonials = selected
	} else {
		result.UnavailableReason = err.Error()
	}
	available, err := listAvailableProductTestimonials(ctx)
	if err != nil {
		if result.UnavailableReason == "" {
			result.UnavailableReason = err.Error()
		}
		return result
	}
	result.TestimonialsAvailable = true
	result.AvailableTestimonials = available
	return result
}

func publicProductTestimonials(ctx *sdk.AppCtx, communityID string, productID int64) ([]PublicProductTestimonial, error) {
	ids, err := loadProductTestimonialIDs(ctx.AppDB(), communityID, productID)
	if err != nil || len(ids) == 0 {
		return []PublicProductTestimonial{}, err
	}
	out := make([]PublicProductTestimonial, 0, len(ids))
	for _, id := range ids {
		record, err := getTestimonialsAppRecord(ctx, id)
		if err != nil || !eligibleProductTestimonial(record) {
			continue
		}
		out = append(out, projectProductTestimonial(record))
	}
	return out, nil
}

func getTestimonialsAppRecord(ctx *sdk.AppCtx, id int64) (testimonialsAppRecord, error) {
	var response struct {
		Found       bool                  `json:"found"`
		Testimonial testimonialsAppRecord `json:"testimonial"`
	}
	if err := callAppResult(ctx, "testimonials", "testimonials_get", map[string]any{"id": id}, &response); err != nil {
		return response.Testimonial, errors.New("Testimonials app is not installed or bound")
	}
	if !response.Found || response.Testimonial.ID == 0 {
		return response.Testimonial, errors.New("not found")
	}
	return response.Testimonial, nil
}

func listAvailableProductTestimonials(ctx *sdk.AppCtx) ([]PublicProductTestimonial, error) {
	var response struct {
		Testimonials []testimonialsAppRecord `json:"testimonials"`
	}
	if err := callAppResult(ctx, "testimonials", "testimonials_list", map[string]any{
		"published_only": true, "limit": 200, "offset": 0,
	}, &response); err != nil {
		return nil, errors.New("Testimonials app is not installed or bound")
	}
	out := make([]PublicProductTestimonial, 0, len(response.Testimonials))
	for _, record := range response.Testimonials {
		// testimonials_list(published_only=true) is already the Testimonials
		// app's public projection. The explicit checks keep this safe against
		// older or malformed integrations.
		if record.Status != "published" || strings.TrimSpace(record.Quote) == "" && strings.TrimSpace(record.Body) == "" {
			continue
		}
		out = append(out, projectProductTestimonial(record))
	}
	return out, nil
}

func eligibleProductTestimonial(record testimonialsAppRecord) bool {
	return record.ID > 0 && record.Status == "published" && record.ConsentStatus == "granted" &&
		(record.PermissionScope == "public" || record.PermissionScope == "marketing") &&
		(strings.TrimSpace(record.Quote) != "" || strings.TrimSpace(record.Body) != "")
}

func projectProductTestimonial(record testimonialsAppRecord) PublicProductTestimonial {
	quote := strings.TrimSpace(record.Quote)
	result := strings.TrimSpace(record.Body)
	if quote == "" {
		quote, result = result, ""
	} else if result == quote {
		result = ""
	}
	role := strings.TrimSpace(record.AuthorRole)
	company := strings.TrimSpace(record.AuthorCompany)
	if company != "" {
		if role == "" {
			role = company
		} else {
			role += " · " + company
		}
	}
	avatar := ""
	if record.Kind == "text" || record.Kind == "review" {
		avatar = strings.TrimSpace(record.MediaFileID)
	}
	return PublicProductTestimonial{
		ID: record.ID, Kind: record.Kind, Quote: quote, Result: result,
		StudentName: strings.TrimSpace(record.AuthorName), Role: role, Rating: record.Rating,
		AvatarStorageFileID: avatar, MediaURL: strings.TrimSpace(record.MediaURL),
		Verified: true, VerificationStatus: "published_with_consent",
	}
}

func testimonialIDArrayArg(raw any) ([]int64, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, errors.New("testimonial_ids must be an array of integers")
	}
	var ids []int64
	if err := json.Unmarshal(encoded, &ids); err != nil {
		return nil, errors.New("testimonial_ids must be an array of integers")
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, errors.New("testimonial_ids must contain unique positive integers")
		}
		seen[id] = true
	}
	return ids, nil
}
