package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type brandProductionPolicy struct {
	AssetType      string `json:"asset_type"`
	Provider       string `json:"required_provider"`
	Model          string `json:"model"`
	PersonaID      int64  `json:"persona_id"`
	StyleProfileID int64  `json:"persona_style_profile_id"`
}

// Policy is opt-in for existing callers. New shared-team requests name both
// their canonical Editorial brand and production profile; failed resolution
// must never degrade to the global generic-video default.
func resolveBrandProductionPolicy(ctx *sdk.AppCtx, args map[string]any) (*brandProductionPolicy, error) {
	brand, profile := strings.TrimSpace(strArg(args, "brand_id", "")), strings.TrimSpace(strArg(args, "production_profile", ""))
	if brand == "" && profile == "" {
		return nil, nil
	}
	if brand == "" || profile == "" {
		return nil, errors.New("brand_id and production_profile must be supplied together")
	}
	var settings struct {
		Brands []struct {
			ID        string `json:"id"`
			PersonaID int64  `json:"persona_id"`
			Profile   struct {
				Profiles map[string]brandProductionPolicy `json:"production_profiles"`
			} `json:"operating_profile"`
		} `json:"brands"`
	}
	if err := ctx.PlatformAPI().CallAppResult("editorial", "editorial_settings_get", map[string]any{}, &settings); err != nil {
		return nil, fmt.Errorf("cannot resolve Editorial brand production policy: %w", err)
	}
	for _, b := range settings.Brands {
		if b.ID != brand {
			continue
		}
		p, ok := b.Profile.Profiles[profile]
		if !ok {
			return nil, errors.New("unknown production profile for this brand")
		}
		if p.PersonaID == 0 {
			p.PersonaID = b.PersonaID
		}
		if err := applyBrandProductionPolicy(p, args); err != nil {
			return nil, err
		}
		if p.PersonaID > 0 && (p.AssetType == KindAvatar || p.AssetType == KindAudioTTS) {
			if err := resolveBrandIdentity(ctx, p, args); err != nil {
				return nil, err
			}
		}
		// An old cache entry cannot cross a brand/profile or identity revision.
		if key := strArg(args, "cache_key", ""); key != "" {
			raw, _ := json.Marshal(map[string]any{"brand": brand, "profile": profile, "policy": p, "avatar": args["avatar"], "voice": args["voice"], "key": key})
			args["cache_key"] = fmt.Sprintf("brand-policy:%x", sha256.Sum256(raw))
		}
		return &p, nil
	}
	return nil, errors.New("unknown Editorial brand in this project")
}

func applyBrandProductionPolicy(p brandProductionPolicy, args map[string]any) error {
	if p.AssetType == "" || strArg(args, "kind", "") != p.AssetType {
		return fmt.Errorf("brand profile requires kind=%s; generic substitution is not allowed", p.AssetType)
	}
	if p.Provider == "heygen" && p.AssetType != KindAvatar {
		return errors.New("HeyGen presenter production requires kind=avatar")
	}
	if chosen := strArg(args, "provider", ""); chosen != "" && p.Provider != "" && chosen != p.Provider {
		return errors.New("requested provider conflicts with the brand production profile")
	}
	if model := strArg(args, "model", ""); model != "" && p.Provider != "" {
		if provider, _, ok := splitProviderModel(model); ok && provider != p.Provider {
			return errors.New("model provider conflicts with the brand production profile")
		}
	}
	if p.Provider != "" {
		args["provider"] = p.Provider
	}
	if strArg(args, "model", "") == "" && p.Model != "" {
		args["model"] = p.Model
	}
	return nil
}

func resolveBrandIdentity(ctx *sdk.AppCtx, p brandProductionPolicy, args map[string]any) error {
	var out struct {
		Persona struct {
			ID     int64  `json:"id"`
			Avatar string `json:"default_avatar_id"`
			Voice  string `json:"default_voice_id"`
		} `json:"persona"`
		Styles []struct {
			ID        int64          `json:"id"`
			AssetType string         `json:"asset_type"`
			Settings  map[string]any `json:"provider_settings"`
		} `json:"style_profiles"`
	}
	if err := ctx.PlatformAPI().CallAppResult("persona-studio", "persona_get", map[string]any{"id": p.PersonaID}, &out); err != nil {
		return fmt.Errorf("cannot resolve the approved persona: %w", err)
	}
	if out.Persona.ID != p.PersonaID {
		return errors.New("approved persona is unavailable in this project")
	}
	avatar, voice := out.Persona.Avatar, ""
	if p.AssetType == KindAudioTTS {
		voice = out.Persona.Voice
	}
	if p.StyleProfileID > 0 {
		found := false
		for _, style := range out.Styles {
			if style.ID != p.StyleProfileID {
				continue
			}
			found = true
			if style.AssetType != p.AssetType {
				return errors.New("persona style does not match the required production kind")
			}
			if v, ok := style.Settings["avatar"].(string); ok && v != "" {
				avatar = v
			}
			if v, ok := style.Settings["voice"].(string); ok && v != "" {
				voice = v
			}
			if v, ok := style.Settings["provider"].(string); ok && p.Provider != "" && v != p.Provider {
				return errors.New("persona provider conflicts with the brand production profile")
			}
		}
		if !found {
			return errors.New("approved persona style profile is unavailable")
		}
	}
	if p.AssetType == KindAvatar {
		if avatar == "" {
			return errors.New("approved persona has no configured avatar")
		}
		if chosen := strArg(args, "avatar", ""); chosen != "" && chosen != avatar {
			return errors.New("requested avatar does not match the approved persona")
		}
		args["avatar"] = avatar
	}
	if voice != "" {
		if chosen := strArg(args, "voice", ""); chosen != "" && chosen != voice {
			return errors.New("requested voice does not match the approved persona profile")
		}
		args["voice"] = voice
	}
	return nil
}
