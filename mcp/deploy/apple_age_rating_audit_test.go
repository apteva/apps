package main

import (
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

var appleAgeRatingQuestionContract = []struct {
	generic, apple string
	boolean        bool
}{
	{"violence_cartoon_fantasy", "violenceCartoonOrFantasy", false},
	{"violence_realistic", "violenceRealistic", false},
	{"violence_graphic_realistic", "violenceRealisticProlongedGraphicOrSadistic", false},
	{"sexual_content", "sexualContentOrNudity", false},
	{"sexual_content_graphic", "sexualContentGraphicAndNudity", false},
	{"profanity", "profanityOrCrudeHumor", false},
	{"drugs", "alcoholTobaccoOrDrugUseOrReferences", false},
	{"gambling_simulation", "gamblingSimulated", false},
	{"contests", "contests", false},
	{"weapons", "gunsOrOtherWeapons", false},
	{"horror_fear", "horrorOrFearThemes", false},
	{"medical_information", "medicalOrTreatmentInformation", false},
	{"mature_themes", "matureOrSuggestiveThemes", false},
	{"health_wellness", "healthOrWellnessTopics", true},
	{"unrestricted_web_access", "unrestrictedWebAccess", true},
	{"real_money_gambling", "gambling", true},
	{"loot_boxes", "lootBox", true},
	{"advertising", "advertising", true},
	{"messaging_chat", "messagingAndChat", true},
	{"user_generated_content", "userGeneratedContent", true},
	{"parental_controls", "parentalControls", true},
	{"age_assurance", "ageAssurance", true},
	{"social_media", "socialMedia", true},
	{"social_media_age_gate", "socialMediaAgeRestricted", true},
}

func TestAppleAgeRatingEveryQuestionMapsIndependently(t *testing.T) {
	for _, question := range appleAgeRatingQuestionContract {
		values := []any{"NONE", "INFREQUENT", "FREQUENT", "INFREQUENT_OR_MILD", "FREQUENT_OR_INTENSE"}
		if question.boolean {
			values = []any{false, true}
		}
		for _, value := range values {
			input, expected := map[string]any{}, map[string]any{}
			for _, other := range appleAgeRatingQuestionContract {
				var baseline any = "NONE"
				if other.boolean {
					baseline = false
				}
				input[other.generic], expected[other.apple] = baseline, baseline
			}
			input[question.generic] = value
			expected[question.apple] = value
			if !question.boolean {
				expected[question.apple] = normalizeAppleRatingLevel(value.(string))
			}
			body, _ := json.Marshal(input)
			var rating StoreContentRating
			if err := json.Unmarshal(body, &rating); err != nil {
				t.Fatal(err)
			}
			got := appleAgeDeclaration(StoreClassification{ContentRating: rating})
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("%s=%v changed another question or type:\ngot %#v\nwant %#v", question.generic, value, got, expected)
			}
			if err := validateAppleAgeDeclaration(got); err != nil {
				t.Fatalf("%s: %v", question.generic, err)
			}
		}
	}
}

func TestAppleAgeRatingAuditedSchemaCoversAllThirtyFields(t *testing.T) {
	covered := map[string]bool{}
	for _, question := range appleAgeRatingQuestionContract {
		field, exists := appleAgeRatingSchema.Attributes.Properties[question.apple]
		if !exists || covered[question.apple] {
			t.Fatalf("missing or duplicated question %s", question.apple)
		}
		if question.boolean != (field.Type == "boolean") || (!question.boolean && !appleAgeRatingFrequencyField(field)) {
			t.Fatalf("wrong question type: %s", question.apple)
		}
		covered[question.apple] = true
	}
	for key, value := range map[string]any{
		"kidsAgeBand": "SIX_TO_EIGHT", "ageRatingOverride": "SEVENTEEN_PLUS",
		"ageRatingOverrideV2": "EIGHTEEN_PLUS", "koreaAgeRatingOverride": "TWELVE_PLUS",
		"gracRatingClassificationNumber": "GRAC-2026-123", "developerAgeRatingInfoUrl": "https://example.com/age-rating",
	} {
		if _, exists := appleAgeRatingSchema.Attributes.Properties[key]; !exists || covered[key] {
			t.Fatalf("missing or duplicated advanced field %s", key)
		}
		attributes := appleAgeDeclaration(StoreClassification{AgeDeclaration: map[string]any{key: value}})
		if err := validateAppleAgeDeclaration(attributes); err != nil {
			t.Fatal(err)
		}
		if len(attributes) != 1 || attributes[key] != value {
			t.Fatalf("advanced field %s was changed: %#v", key, attributes)
		}
		covered[key] = true
	}
	if len(covered) != 30 || len(appleAgeRatingSchema.Attributes.Properties) != 30 || appleAgeRatingSchema.Version != "4.5.1" {
		t.Fatal("Apple age-rating schema coverage changed; review every field")
	}
	var provenance struct {
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(appleAgeRatingSchemaJSON, &provenance); err != nil || provenance.SHA256 != "7518d3a94a8bd701ac25c1c601b95b8f53aad92091affb15a2a9d331713dac1a" {
		t.Fatal("upstream schema snapshot changed without audit")
	}
}

func TestAppleAgeRatingEverySchemaTypeEnumAndNullableValue(t *testing.T) {
	for key, field := range appleAgeRatingSchema.Attributes.Properties {
		t.Run(key, func(t *testing.T) {
			if err := validateAppleAgeDeclaration(map[string]any{key: nil}); err != nil {
				t.Fatalf("documented nullable value rejected: %v", err)
			}
			if err := validateAppleAgeDeclaration(map[string]any{key: 42}); err == nil {
				t.Fatal("wrong primitive type accepted")
			}
			if field.Type == "boolean" {
				for _, value := range []bool{false, true} {
					if err := validateAppleAgeDeclaration(map[string]any{key: value}); err != nil {
						t.Fatal(err)
					}
				}
				if err := validateAppleAgeDeclaration(map[string]any{key: "false"}); err == nil {
					t.Fatal("string boolean accepted")
				}
			}
			for _, value := range field.Enum {
				attributes := map[string]any{key: value}
				if err := validateAppleAgeDeclaration(attributes); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(appleAgeDeclaration(StoreClassification{AgeDeclaration: attributes}), attributes) {
					t.Fatal("exact enum answer changed")
				}
			}
			if len(field.Enum) > 0 {
				if err := validateAppleAgeDeclaration(map[string]any{key: "INVALID"}); err == nil {
					t.Fatal("invalid enum accepted")
				}
			}
		})
	}
}

func TestAppleAgeRatingInvalidAdvancedAnswersBlockBeforeProviderCalls(t *testing.T) {
	p := &iosPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(p))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	for key, value := range map[string]any{
		"healthOrWellnessTopics": "NONE", "advertising": "false",
		"gamblingSimulated": true, "profanityOrCrudeHumor": "SOMETIMES",
		"kidsAgeBand": "NONE", "ageRatingOverrideV2": "SEVENTEEN_PLUS",
		"koreaAgeRatingOverride": "EIGHTEEN_PLUS", "developerAgeRatingInfoUrl": "not a URL",
		"gracRatingClassificationNumber": 123, "gamblingAndContests": "NONE",
	} {
		t.Run(key, func(t *testing.T) {
			doc := completeIOSStoreDocument()
			doc.Classification.AgeDeclaration = map[string]any{key: value}
			preflight := validateStoreDocument(t.TempDir(), &Deployment{TargetKind: "ios"}, nil, &MobileStoreConfig{}, doc, true)
			if !hasStoreFinding(preflight, "age_rating.invalid") {
				t.Fatal("invalid Apple answer was not blocked")
			}
			before := len(p.calls)
			if err := (&App{}).applyAppleMetadata(&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}, "app-1", doc, storeScopeSet{"classification": true}); err == nil {
				t.Fatal("invalid declaration reached provider apply")
			}
			if len(p.calls) != before {
				t.Fatal("invalid declaration contacted provider")
			}
		})
	}
}

func TestAppleAgeRatingReadinessComparesRequestedAnswers(t *testing.T) {
	p := &iosPlatform{responses: map[string]json.RawMessage{
		"list_app_infos": json.RawMessage(`{"data":[{"id":"info-1","attributes":{"appStoreState":"PREPARE_FOR_SUBMISSION"},"relationships":{"primaryCategory":{"data":{"id":"GAMES"}}}}]}`),
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(p))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	d := &Deployment{TargetKind: "ios", TargetConfigJSON: `{"app_store_app_id":"app-1"}`}
	doc := StoreDocument{Classification: StoreClassification{PrimaryCategory: "GAMES", AgeDeclaration: map[string]any{"healthOrWellnessTopics": false, "gamblingSimulated": "INFREQUENT"}}}
	for _, tt := range []struct {
		name, response string
		verified       bool
	}{
		{"matching", `{"healthOrWellnessTopics":false,"gamblingSimulated":"INFREQUENT"}`, true},
		{"legacy_equivalent", `{"healthOrWellnessTopics":false,"gamblingSimulated":"INFREQUENT_OR_MILD"}`, true},
		{"wrong_value", `{"healthOrWellnessTopics":true,"gamblingSimulated":"INFREQUENT"}`, false},
		{"wrong_type", `{"healthOrWellnessTopics":"false","gamblingSimulated":"INFREQUENT"}`, false},
		{"missing_answer", `{"gamblingSimulated":"INFREQUENT"}`, false},
		{"empty", `{}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p.responses["get_app_age_rating_declaration"] = json.RawMessage(`{"data":{"id":"age-1","attributes":` + tt.response + `}}`)
			observed, err := (&App{}).observeAppleStoreConfig(d, doc)
			if err != nil {
				t.Fatal(err)
			}
			got := readinessValueVerified(observed["readiness"].(map[string]any)["classification"])
			if got != tt.verified {
				t.Fatalf("classification verified=%v, want %v", got, tt.verified)
			}
		})
	}
}
