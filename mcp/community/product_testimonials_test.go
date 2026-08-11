package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func publishedTestimonial(id int64, name, quote, result string) testimonialsAppRecord {
	rating := 5
	return testimonialsAppRecord{
		ID: id, Status: "published", Kind: "review", Quote: quote, Body: result, Rating: &rating,
		AuthorName: name, AuthorRole: "Student", AuthorCompany: "Makecademy",
		MediaFileID: "42", ConsentStatus: "granted", PermissionScope: "marketing",
	}
}

func TestProductTestimonialsAreOrderedValidatedAndProjectedOnce(t *testing.T) {
	platform := newSalesPlatformStub()
	platform.product.Metadata = json.RawMessage(`{"storefront":{"testimonial":{"quote":"Legacy Catalog quote","name":"Legacy"}}}`)
	platform.testimonials[1] = publishedTestimonial(1, "Ada", "I shipped it.", "My first project was live in a weekend.")
	platform.testimonials[2] = publishedTestimonial(2, "Grace", "Clear and practical.", "Clear and practical.")
	platform.testimonials[3] = testimonialsAppRecord{
		ID: 3, Status: "approved", Kind: "review", Quote: "Not public", ConsentStatus: "granted", PermissionScope: "marketing",
	}
	ctx, community, _, _ := salesFixture(t, platform)

	out, err := toolProductTestimonialsSet(ctx, map[string]any{
		"community_id": community.ID, "catalog_product_id": int64(61), "testimonial_ids": []any{2, 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(ProductTestimonialsResult)
	if len(result.TestimonialIDs) != 2 || result.TestimonialIDs[0] != 2 || result.TestimonialIDs[1] != 1 {
		t.Fatalf("ordered ids=%v", result.TestimonialIDs)
	}
	if len(result.Testimonials) != 2 || result.Testimonials[0].StudentName != "Grace" || result.Testimonials[1].StudentName != "Ada" {
		t.Fatalf("ordered testimonials=%+v", result.Testimonials)
	}
	if result.Testimonials[0].Result != "" {
		t.Fatalf("duplicate quote/result was not removed: %+v", result.Testimonials[0])
	}
	if !result.Testimonials[1].Verified || result.Testimonials[1].VerificationStatus != "published_with_consent" {
		t.Fatalf("verification projection=%+v", result.Testimonials[1])
	}
	if result.Testimonials[1].AvatarStorageFileID != "42" || result.Testimonials[1].Role != "Student · Makecademy" {
		t.Fatalf("author projection=%+v", result.Testimonials[1])
	}

	products, err := buildPublicProducts(ctx, community.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || len(products[0].Testimonials) != 2 {
		t.Fatalf("public products=%+v", products)
	}
	if products[0].Storefront.Testimonial != nil {
		t.Fatal("legacy Catalog testimonial was repeated alongside curated Testimonials proof")
	}

	if _, err := toolProductTestimonialsSet(ctx, map[string]any{
		"community_id": community.ID, "catalog_product_id": int64(61), "testimonial_ids": []any{3},
	}); err == nil || !strings.Contains(err.Error(), "published") {
		t.Fatalf("unpublished testimonial accepted: %v", err)
	}
}

func TestProductTestimonialsRequireOfferedProductAndUniqueIDs(t *testing.T) {
	platform := newSalesPlatformStub()
	platform.testimonials[1] = publishedTestimonial(1, "Ada", "Useful", "A concrete result")
	ctx, community, _, _ := salesFixture(t, platform)
	if _, err := toolProductTestimonialsSet(ctx, map[string]any{
		"community_id": community.ID, "catalog_product_id": int64(999), "testimonial_ids": []any{1},
	}); err == nil || !strings.Contains(err.Error(), "not actively offered") {
		t.Fatalf("unoffered product accepted: %v", err)
	}
	if _, err := toolProductTestimonialsSet(ctx, map[string]any{
		"community_id": community.ID, "catalog_product_id": int64(61), "testimonial_ids": []any{1, 1},
	}); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate ids accepted: %v", err)
	}
}

func TestProductTestimonialsDisappearWhenPublicationIsRevoked(t *testing.T) {
	platform := newSalesPlatformStub()
	platform.testimonials[1] = publishedTestimonial(1, "Ada", "Useful", "A concrete result")
	ctx, community, _, _ := salesFixture(t, platform)
	if _, err := toolProductTestimonialsSet(ctx, map[string]any{
		"community_id": community.ID, "catalog_product_id": int64(61), "testimonial_ids": []any{1},
	}); err != nil {
		t.Fatal(err)
	}
	revoked := platform.testimonials[1]
	revoked.ConsentStatus = "revoked"
	platform.testimonials[1] = revoked
	products, err := buildPublicProducts(ctx, community.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(products) != 1 || len(products[0].Testimonials) != 0 {
		t.Fatalf("revoked testimonial remained public: %+v", products)
	}
}
