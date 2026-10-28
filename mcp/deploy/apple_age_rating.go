package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// The checked-in subset of Apple's published OpenAPI schema keeps validation
// offline and records the source version and full upstream JSON digest.
//
//go:embed schemas/apple-age-rating-4.5.1.json
var appleAgeRatingSchemaJSON []byte

type appleAgeRatingField struct {
	Type     string   `json:"type"`
	Nullable bool     `json:"nullable"`
	Enum     []string `json:"enum"`
	Format   string   `json:"format"`
	Ref      string   `json:"$ref"`
}

type appleAgeRatingSchemaDefinition struct {
	Version    string `json:"version"`
	Attributes struct {
		Properties map[string]appleAgeRatingField `json:"properties"`
	} `json:"attributes"`
	KidsAgeBand appleAgeRatingField `json:"kidsAgeBand"`
}

var appleAgeRatingSchema = func() appleAgeRatingSchemaDefinition {
	var schema appleAgeRatingSchemaDefinition
	if err := json.Unmarshal(appleAgeRatingSchemaJSON, &schema); err != nil {
		panic(err)
	}
	for key, field := range schema.Attributes.Properties {
		if field.Ref == "#/components/schemas/KidsAgeBand" {
			field.Type, field.Enum = schema.KidsAgeBand.Type, schema.KidsAgeBand.Enum
			schema.Attributes.Properties[key] = field
		}
	}
	return schema
}()

func validateAppleAgeDeclaration(attributes map[string]any) error {
	keys := make([]string, 0, len(attributes))
	for key := range attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field, ok := appleAgeRatingSchema.Attributes.Properties[key]
		if !ok {
			return fmt.Errorf("unknown Apple age-rating field %q (schema %s)", key, appleAgeRatingSchema.Version)
		}
		value := attributes[key]
		if value == nil && field.Nullable {
			continue
		}
		switch field.Type {
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("Apple age-rating field %s must be a boolean", key)
			}
		case "string":
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("Apple age-rating field %s must be a string", key)
			}
			if len(field.Enum) > 0 {
				allowed := false
				for _, option := range field.Enum {
					allowed = allowed || text == option
				}
				if !allowed {
					return fmt.Errorf("Apple age-rating field %s must be one of %s", key, strings.Join(field.Enum, ", "))
				}
			}
			if field.Format == "uri" {
				parsed, err := url.Parse(text)
				if err != nil || !parsed.IsAbs() || strings.ContainsAny(text, " \t\r\n") {
					return fmt.Errorf("Apple age-rating field %s must be an absolute URI", key)
				}
			}
		default:
			return fmt.Errorf("unsupported Apple age-rating field type for %s", key)
		}
	}
	return nil
}

func appleAgeDeclarationMatches(raw json.RawMessage, desired StoreClassification) bool {
	expected := appleAgeDeclaration(desired)
	if len(expected) == 0 || validateAppleAgeDeclaration(expected) != nil {
		return false
	}
	var payload struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	for key, want := range expected {
		got, present := payload.Data.Attributes[key]
		if !present {
			return false
		}
		// Apple may return the older spelling of a frequency value. Boolean
		// questions and rating overrides must never go through that conversion.
		field := appleAgeRatingSchema.Attributes.Properties[key]
		if appleAgeRatingFrequencyField(field) {
			if text, ok := want.(string); ok {
				want = normalizeAppleRatingLevel(text)
			}
			if text, ok := got.(string); ok {
				got = normalizeAppleRatingLevel(text)
			}
		}
		if jsonDigest(got) != jsonDigest(want) {
			return false
		}
	}
	return true
}

func appleAgeRatingFrequencyField(field appleAgeRatingField) bool {
	for _, option := range field.Enum {
		if option == "INFREQUENT" {
			return true
		}
	}
	return false
}
