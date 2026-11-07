package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func ageRatingFixture(t *testing.T) StoreContentRating {
	t.Helper()
	var rating StoreContentRating
	if err := json.Unmarshal([]byte(`{
		"violence":"FREQUENT", "violence_cartoon_fantasy":"INFREQUENT",
		"violence_realistic":"NONE", "violence_graphic_realistic":"NONE",
		"sexual_content":"INFREQUENT", "sexual_content_graphic":"NONE",
		"profanity":"NONE", "drugs":"NONE", "gambling_simulation":"NONE",
		"contests":"NONE", "weapons":"NONE", "horror_fear":"NONE",
		"medical_information":"NONE", "health_wellness":"NONE", "mature_themes":"NONE"
	}`), &rating); err != nil {
		t.Fatal(err)
	}
	return rating
}

func TestAppleAgeRatingKeepsContentQuestionsIndependent(t *testing.T) {
	rating := ageRatingFixture(t)
	attributes := appleAgeDeclaration(StoreClassification{ContentRating: rating})
	for key, want := range map[string]any{
		"violenceCartoonOrFantasy": "INFREQUENT", "violenceRealistic": "NONE",
		"violenceRealisticProlongedGraphicOrSadistic": "NONE",
		"sexualContentOrNudity":                       "INFREQUENT", "sexualContentGraphicAndNudity": "NONE",
		"healthOrWellnessTopics": false,
	} {
		if attributes[key] != want {
			t.Errorf("%s=%#v, want %#v", key, attributes[key], want)
		}
	}
	// Explicit stronger answers must also be retained, including over legacy NONE.
	rating.Violence = "NONE"
	rating.ViolenceRealistic = "INFREQUENT"
	rating.ViolenceGraphicRealistic = "FREQUENT"
	rating.SexualContentGraphic = "FREQUENT"
	attributes = appleAgeDeclaration(StoreClassification{ContentRating: rating})
	if attributes["violenceRealistic"] != "INFREQUENT" || attributes["violenceRealisticProlongedGraphicOrSadistic"] != "FREQUENT" || attributes["sexualContentGraphicAndNudity"] != "FREQUENT" {
		t.Fatalf("explicit graphic answers lost: %#v", attributes)
	}
}

func TestAppleAgeRatingLegacyNoneIsUnambiguous(t *testing.T) {
	rating := ageRatingFixture(t)
	var values map[string]any
	body, _ := json.Marshal(rating)
	_ = json.Unmarshal(body, &values)
	for _, key := range []string{"violence_cartoon_fantasy", "violence_realistic", "violence_graphic_realistic", "sexual_content_graphic"} {
		delete(values, key)
	}
	values["violence"], values["sexual_content"] = "NONE", "NONE"
	body, _ = json.Marshal(values)
	rating = StoreContentRating{}
	if err := json.Unmarshal(body, &rating); err != nil {
		t.Fatal(err)
	}
	if !storeContentRatingComplete(rating) {
		t.Fatal("legacy no-content answers needlessly blocked")
	}
	attributes := appleAgeDeclaration(StoreClassification{ContentRating: rating})
	if attributes["violenceCartoonOrFantasy"] != "NONE" || attributes["violenceRealistic"] != "NONE" || attributes["violenceRealisticProlongedGraphicOrSadistic"] != "NONE" || attributes["sexualContentGraphicAndNudity"] != "NONE" {
		t.Fatalf("legacy NONE did not migrate safely: %#v", attributes)
	}
}

func TestAppleAgeRatingPreservesExactDeclarationOverrides(t *testing.T) {
	attributes := appleAgeDeclaration(StoreClassification{ContentRating: ageRatingFixture(t), AgeDeclaration: map[string]any{
		"violenceCartoonOrFantasy": "FREQUENT", "violenceRealistic": "NONE",
		"violenceRealisticProlongedGraphicOrSadistic": "NONE", "healthOrWellnessTopics": true,
	}})
	if attributes["violenceCartoonOrFantasy"] != "FREQUENT" || attributes["violenceRealistic"] != "NONE" || attributes["healthOrWellnessTopics"] != true {
		t.Fatalf("exact answers overwritten: %#v", attributes)
	}
}

func TestAppleAgeRatingRejectsAmbiguousLegacyContent(t *testing.T) {
	for _, legacy := range []string{"violence", "sexual_content"} {
		t.Run(legacy, func(t *testing.T) {
			rating := ageRatingFixture(t)
			body, _ := json.Marshal(rating)
			var values map[string]any
			_ = json.Unmarshal(body, &values)
			delete(values, "violence_cartoon_fantasy")
			delete(values, "violence_realistic")
			delete(values, "violence_graphic_realistic")
			delete(values, "sexual_content_graphic")
			values["violence"] = "NONE"
			values["sexual_content"] = "NONE"
			values[legacy] = "INFREQUENT"
			body, _ = json.Marshal(values)
			rating = StoreContentRating{}
			if err := json.Unmarshal(body, &rating); err != nil {
				t.Fatal(err)
			}
			if storeContentRatingComplete(rating) || len(appleAgeDeclaration(StoreClassification{ContentRating: rating})) != 0 {
				t.Fatal("ambiguous answer generated Apple declarations")
			}
			doc := completeIOSStoreDocument()
			doc.Classification = StoreClassification{PrimaryCategory: "GAMES", ContentRating: rating}
			preflight := validateStoreDocument(t.TempDir(), &Deployment{TargetKind: "ios"}, nil, &MobileStoreConfig{}, doc, true)
			blocked := false
			for _, finding := range preflight.Findings {
				blocked = blocked || finding.Code == "age_rating.required"
			}
			if !blocked {
				t.Fatal("preflight allowed ambiguous classification")
			}
		})
	}
}

func TestAppleAgeRatingHealthBooleanAndLegacyMigration(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want bool
	}{
		{`false`, false}, {`true`, true}, {`"NONE"`, false},
		{`"INFREQUENT"`, true}, {`"FREQUENT_OR_INTENSE"`, true},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			rating := ageRatingFixture(t)
			if err := json.Unmarshal([]byte(`{"health_wellness":`+tt.raw+`}`), &rating); err != nil {
				t.Fatal(err)
			}
			attributes := appleAgeDeclaration(StoreClassification{ContentRating: rating})
			if attributes["healthOrWellnessTopics"] != tt.want {
				t.Fatalf("health answer is not the correct boolean: %#v", attributes)
			}
		})
	}
	var rating StoreContentRating
	if err := json.Unmarshal([]byte(`{"health_wellness":"sometimes"}`), &rating); err == nil {
		t.Fatal("invalid legacy health answer was silently accepted")
	}
}

func TestAppleAgeRatingProviderRequestUsesIndependentAnswers(t *testing.T) {
	p := &iosPlatform{responses: map[string]json.RawMessage{
		"list_app_infos":                 json.RawMessage(`{"data":[{"id":"info-1","attributes":{"appStoreState":"PREPARE_FOR_SUBMISSION"}}]}`),
		"get_app_age_rating_declaration": json.RawMessage(`{"data":{"id":"age-1"}}`),
	}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(p))
	old := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = old })
	doc := StoreDocument{Classification: StoreClassification{ContentRating: ageRatingFixture(t)}}
	if err := (&App{}).applyAppleMetadata(&sdk.BoundIntegration{ConnectionID: 77, AppSlug: "app-store-connect"}, "app-1", doc, storeScopeSet{"classification": true}); err != nil {
		t.Fatal(err)
	}
	for _, call := range p.calls {
		if call.Tool != "update_age_rating_declaration" {
			continue
		}
		body, _ := json.Marshal(call.Input["body"])
		var payload struct {
			Data struct {
				Attributes map[string]any `json:"attributes"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		attributes := payload.Data.Attributes
		if attributes["violenceCartoonOrFantasy"] != "INFREQUENT" || attributes["violenceRealistic"] != "NONE" || attributes["violenceRealisticProlongedGraphicOrSadistic"] != "NONE" || attributes["healthOrWellnessTopics"] != false {
			t.Fatalf("incorrect provider payload: %s", body)
		}
		return
	}
	t.Fatal("no age-rating update was sent")
}
