package main

import (
	"fmt"
	"math"
	"mime"
	"path/filepath"
	"strings"
)

// responseSpec describes what a worker must return for one instruction.
// It intentionally lives beside, rather than inside, the instruction kind:
// kind describes what the worker sees; response describes what they provide.
// The JSON representation is stored in body.response so instruction versions,
// template overrides and immutable gig snapshots all carry the same contract.
type responseSpec struct {
	Note              responseNoteSpec `json:"note"`
	Files             responseFileSpec `json:"files"`
	LegacyAnyRequired bool             `json:"-"`
}

type responseNoteSpec struct {
	Enabled     bool   `json:"enabled"`
	Required    bool   `json:"required"`
	Label       string `json:"label,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type responseFileSpec struct {
	Enabled   bool     `json:"enabled"`
	Required  bool     `json:"required"`
	Accept    []string `json:"accept,omitempty"`
	MinItems  int      `json:"min_items,omitempty"`
	MaxItems  int      `json:"max_items,omitempty"`
	MaxSizeMB int      `json:"max_size_mb,omitempty"`
}

func responseSpecFromBody(body map[string]any) responseSpec {
	if raw, ok := body["response"].(map[string]any); ok {
		return responseSpec{
			Note:  parseResponseNote(mapOf(raw["note"])),
			Files: parseResponseFiles(mapOf(raw["files"])),
		}
	}

	// Backward compatibility for immutable v0.2 gig snapshots and pinned
	// instruction versions. Legacy required meant "a note OR a file".
	switch strings.ToLower(strings.TrimSpace(strOf(body["response_mode"]))) {
	case "optional":
		return responseSpec{
			Note:  responseNoteSpec{Enabled: true},
			Files: responseFileSpec{Enabled: true},
		}
	case "required":
		return responseSpec{
			Note:              responseNoteSpec{Enabled: true},
			Files:             responseFileSpec{Enabled: true},
			LegacyAnyRequired: true,
		}
	default:
		return responseSpec{}
	}
}

func parseResponseNote(raw map[string]any) responseNoteSpec {
	if raw == nil {
		return responseNoteSpec{}
	}
	required := boolOf(raw["required"])
	return responseNoteSpec{
		Enabled:     boolOf(raw["enabled"]) || required,
		Required:    required,
		Label:       strings.TrimSpace(strOf(raw["label"])),
		Placeholder: strings.TrimSpace(strOf(raw["placeholder"])),
	}
}

func parseResponseFiles(raw map[string]any) responseFileSpec {
	if raw == nil {
		return responseFileSpec{}
	}
	required := boolOf(raw["required"])
	accept := []string{}
	if values, ok := raw["accept"].([]any); ok {
		for _, value := range values {
			if item := strings.ToLower(strings.TrimSpace(strOf(value))); item != "" {
				accept = append(accept, item)
			}
		}
	} else if values, ok := raw["accept"].([]string); ok {
		for _, value := range values {
			if item := strings.ToLower(strings.TrimSpace(value)); item != "" {
				accept = append(accept, item)
			}
		}
	}
	minItems := intFromAny(raw["min_items"])
	if required && minItems == 0 {
		minItems = 1
	}
	return responseFileSpec{
		Enabled:   boolOf(raw["enabled"]) || required,
		Required:  required,
		Accept:    accept,
		MinItems:  minItems,
		MaxItems:  intFromAny(raw["max_items"]),
		MaxSizeMB: intFromAny(raw["max_size_mb"]),
	}
}

func validateResponseSpec(kind string, body map[string]any) error {
	raw, explicit := body["response"]
	if explicit {
		response, ok := raw.(map[string]any)
		if !ok {
			return errorsf("kind %q body.response must be an object", kind)
		}
		if isInputKind(kind) {
			return errorsf("kind %q already collects structured input and cannot also define body.response", kind)
		}
		for _, sectionName := range []string{"note", "files"} {
			sectionRaw, present := response[sectionName]
			if !present {
				continue
			}
			section, ok := sectionRaw.(map[string]any)
			if !ok {
				return errorsf("kind %q body.response.%s must be an object", kind, sectionName)
			}
			for _, field := range []string{"enabled", "required"} {
				if value, present := section[field]; present {
					if _, ok := value.(bool); !ok {
						return errorsf("kind %q body.response.%s.%s must be a boolean", kind, sectionName, field)
					}
				}
			}
			if boolOf(section["required"]) && section["enabled"] == false {
				return errorsf("kind %q body.response.%s.required cannot be true when enabled is false", kind, sectionName)
			}
		}
		if note := mapOf(response["note"]); note != nil {
			for _, field := range []string{"label", "placeholder"} {
				if value, present := note[field]; present {
					if _, ok := value.(string); !ok {
						return errorsf("kind %q body.response.note.%s must be a string", kind, field)
					}
				}
			}
		}
		if files := mapOf(response["files"]); files != nil {
			if accept, present := files["accept"]; present && !stringList(accept) {
				return errorsf("kind %q body.response.files.accept must be an array of strings", kind)
			}
			for _, field := range []string{"min_items", "max_items", "max_size_mb"} {
				if value, present := files[field]; present && !nonNegativeInteger(value) {
					return errorsf("kind %q body.response.files.%s must be a non-negative integer", kind, field)
				}
			}
		}
	}
	spec := responseSpecFromBody(body)
	if spec.Note.Required && !spec.Note.Enabled {
		return errorsf("kind %q response.note.required needs response.note.enabled", kind)
	}
	if spec.Files.Required && !spec.Files.Enabled {
		return errorsf("kind %q response.files.required needs response.files.enabled", kind)
	}
	if spec.Files.MinItems < 0 || spec.Files.MaxItems < 0 || spec.Files.MaxSizeMB < 0 {
		return errorsf("kind %q response file limits cannot be negative", kind)
	}
	if spec.Files.MaxItems > 0 && spec.Files.MinItems > spec.Files.MaxItems {
		return errorsf("kind %q response.files.min_items cannot exceed max_items", kind)
	}
	for _, pattern := range spec.Files.Accept {
		if !validAcceptPattern(pattern) {
			return errorsf("kind %q has invalid response.files.accept value %q", kind, pattern)
		}
	}
	return nil
}

func stringList(value any) bool {
	switch values := value.(type) {
	case []string:
		return true
	case []any:
		for _, item := range values {
			if _, ok := item.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func nonNegativeInteger(value any) bool {
	switch number := value.(type) {
	case int:
		return number >= 0
	case int64:
		return number >= 0
	case float64:
		return number >= 0 && number == math.Trunc(number)
	default:
		return false
	}
}

func validAcceptPattern(pattern string) bool {
	if strings.HasPrefix(pattern, ".") && len(pattern) > 1 {
		return !strings.ContainsAny(pattern, "/, ")
	}
	parts := strings.Split(pattern, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(pattern, ",")
}

func responseAcceptsFile(spec responseFileSpec, filename, contentType string, sizeBytes int64) error {
	if !spec.Enabled {
		return fmt.Errorf("this instruction does not accept files")
	}
	if spec.MaxSizeMB > 0 && sizeBytes > int64(spec.MaxSizeMB)*1024*1024 {
		return fmt.Errorf("file exceeds this instruction's %d MB limit", spec.MaxSizeMB)
	}
	if len(spec.Accept) == 0 {
		return nil
	}
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	ext := strings.ToLower(filepath.Ext(filename))
	if contentType == "" && ext != "" {
		contentType = strings.ToLower(mime.TypeByExtension(ext))
	}
	for _, pattern := range spec.Accept {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if strings.HasPrefix(pattern, ".") && ext == pattern {
			return nil
		}
		if strings.HasSuffix(pattern, "/*") && strings.HasPrefix(contentType, strings.TrimSuffix(pattern, "*")) {
			return nil
		}
		if contentType != "" && contentType == pattern {
			return nil
		}
	}
	return fmt.Errorf("file type %q is not accepted; expected %s", contentType, strings.Join(spec.Accept, ", "))
}

func instructionResponseKey(resultKey string, sortOrder int) string {
	if strings.TrimSpace(resultKey) != "" {
		return resultKey
	}
	return fmt.Sprintf("step_%d", sortOrder+1)
}

func mapOf(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func boolOf(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		typed = strings.ToLower(strings.TrimSpace(typed))
		return typed == "true" || typed == "1" || typed == "yes"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	default:
		return false
	}
}

func errorsf(format string, args ...any) error { return fmt.Errorf(format, args...) }
