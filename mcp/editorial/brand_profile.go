package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Identity is optional. Editorial owns brand policy; Persona Studio owns the
// linked identity and its provider-specific avatar/voice settings.
func validateBrandProfile(b Brand) error {
	if b.PersonaID < 0 {
		return invalid("persona_id must be zero or a positive Persona Studio ID")
	}
	if b.OperatingProfile == nil {
		return nil
	}
	raw, err := json.Marshal(b.OperatingProfile)
	if err != nil || len(raw) > 64*1024 {
		return invalid("brand operating_profile must be a JSON object of at most 64 KiB")
	}
	for _, key := range []string{"audience", "positioning", "voice", "visual_style"} {
		if v, ok := b.OperatingProfile[key]; ok {
			if _, ok := v.(string); !ok {
				return invalid(key + " must be text")
			}
		}
	}
	if rawProfiles, ok := b.OperatingProfile["production_profiles"]; ok {
		profiles, ok := rawProfiles.(map[string]any)
		if !ok || len(profiles) > 30 {
			return invalid("production_profiles must be an object with at most 30 named profiles")
		}
		for name, rawProfile := range profiles {
			p, ok := rawProfile.(map[string]any)
			if !ok || strings.TrimSpace(name) == "" {
				return invalid("each production profile needs a name and object")
			}
			kind, _ := p["asset_type"].(string)
			if !has([]string{"text", "image", "avatar", "video", "audio_tts", "audio_sfx", "music", "slides", "screen_recording", "recorded"}, kind) {
				return invalid(fmt.Sprintf("production profile %s has an unsupported asset_type", name))
			}
			provider, _ := p["required_provider"].(string)
			if provider == "heygen" && kind != "avatar" {
				return invalid("HeyGen presenter profiles must use asset_type=avatar")
			}
			if _, secret := p["api_key"]; secret {
				return invalid("production profiles reference connections, not API keys")
			}
		}
	}
	return nil
}
