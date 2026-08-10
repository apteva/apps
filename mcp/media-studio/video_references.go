package main

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	videoReferenceRoleIdentity  = "identity"
	videoReferenceRoleScene     = "scene"
	videoReferenceRoleReference = "reference"
)

type videoReferenceGroup struct {
	Role   string
	Images []string
}

type veniceVideoReferenceProfile struct {
	Style             string
	MaxImages         int
	MaxIdentityImages int
	MaxSceneImages    int
}

func veniceReferenceProfile(model string) veniceVideoReferenceProfile {
	lower := strings.ToLower(strings.TrimSpace(model))
	profile := veniceVideoReferenceProfile{
		Style:             "flat",
		MaxImages:         9,
		MaxIdentityImages: 9,
		MaxSceneImages:    9,
	}
	if !strings.Contains(lower, "reference-to-video") {
		profile.MaxImages = 1
		profile.MaxIdentityImages = 1
		profile.MaxSceneImages = 0
		return profile
	}
	if strings.HasPrefix(lower, "kling-") {
		profile.Style = "elements"
		profile.MaxImages = 7
		profile.MaxIdentityImages = 4
		profile.MaxSceneImages = 4
		return profile
	}
	if strings.HasPrefix(lower, "grok-imagine-") {
		profile.MaxImages = 7
	}
	return profile
}

func parseVideoReferenceGroups(value any) ([]videoReferenceGroup, error) {
	if value == nil {
		return nil, nil
	}
	var rawGroups []any
	switch typed := value.(type) {
	case []any:
		rawGroups = typed
	case []map[string]any:
		rawGroups = make([]any, 0, len(typed))
		for _, group := range typed {
			rawGroups = append(rawGroups, group)
		}
	case []videoReferenceGroup:
		return typed, nil
	default:
		return nil, errors.New("must be an array")
	}

	groups := make([]videoReferenceGroup, 0, len(rawGroups))
	for i, raw := range rawGroups {
		groupMap, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("[%d] must be an object", i)
		}
		role := strings.ToLower(strings.TrimSpace(fmt.Sprint(groupMap["role"])))
		if role == "" || role == "<nil>" {
			role = videoReferenceRoleIdentity
		}
		switch role {
		case videoReferenceRoleIdentity, videoReferenceRoleScene, videoReferenceRoleReference:
		default:
			return nil, fmt.Errorf("[%d].role must be identity, scene, or reference", i)
		}
		images, err := stringSlice(groupMap["images"])
		if err != nil {
			return nil, fmt.Errorf("[%d].images: %w", i, err)
		}
		if len(images) == 0 {
			return nil, fmt.Errorf("[%d].images requires at least one image", i)
		}
		groups = append(groups, videoReferenceGroup{Role: role, Images: images})
	}
	return groups, nil
}

func stringSlice(value any) ([]string, error) {
	var values []string
	switch typed := value.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for i, raw := range typed {
			value, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("[%d] must be a string", i)
			}
			values = append(values, value)
		}
	case string:
		values = append(values, typed)
	default:
		return nil, errors.New("must be an array of strings")
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out, nil
}

func videoReferenceGroups(args map[string]any, resolved bool) ([]videoReferenceGroup, error) {
	key := "reference_groups"
	if resolved {
		key = "_resolved_reference_groups"
	}
	return parseVideoReferenceGroups(args[key])
}

func videoReferenceImageRefs(args map[string]any) ([]string, error) {
	groups, err := videoReferenceGroups(args, false)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, group := range groups {
		refs = append(refs, group.Images...)
	}
	return refs, nil
}

func resolveVideoReferenceGroups(ctx *sdk.AppCtx, args map[string]any) ([]videoReferenceGroup, error) {
	groups, err := videoReferenceGroups(args, false)
	if err != nil {
		return nil, err
	}
	resolved := make([]videoReferenceGroup, 0, len(groups))
	for _, group := range groups {
		images := make([]string, 0, len(group.Images))
		for _, ref := range group.Images {
			image, err := resolveSourceImage(ctx, ref)
			if err != nil {
				return nil, err
			}
			images = append(images, image)
		}
		resolved = append(resolved, videoReferenceGroup{Role: group.Role, Images: images})
	}
	return resolved, nil
}

func validateVeniceVideoReferences(model string, groups []videoReferenceGroup, flatRefs []string) error {
	profile := veniceReferenceProfile(model)
	total := len(flatRefs)
	for i, group := range groups {
		total += len(group.Images)
		if profile.Style == "elements" && group.Role == videoReferenceRoleIdentity &&
			len(group.Images) > profile.MaxIdentityImages {
			return fmt.Errorf(
				"reference_groups[%d] supports at most %d identity images (one frontal and up to %d additional angles)",
				i, profile.MaxIdentityImages, profile.MaxIdentityImages-1,
			)
		}
	}
	if total > profile.MaxImages {
		return fmt.Errorf("model supports at most %d reference images, got %d", profile.MaxImages, total)
	}
	if profile.Style == "elements" {
		sceneCount := 0
		for _, group := range groups {
			if group.Role != videoReferenceRoleIdentity {
				sceneCount += len(group.Images)
			}
		}
		if sceneCount > profile.MaxSceneImages {
			return fmt.Errorf("model supports at most %d scene/reference images, got %d", profile.MaxSceneImages, sceneCount)
		}
	}
	return nil
}

func normalizeVeniceReferencePrompt(args map[string]any) error {
	model := strArg(args, "model", "")
	if !isReferenceToVideoModel(model) {
		return nil
	}
	groups, err := videoReferenceGroups(args, false)
	if err != nil {
		return err
	}
	profile := veniceReferenceProfile(model)
	if len(groups) == 0 && profile.Style == "elements" && len(sourceImageRefs(args)) > 0 {
		groups = []videoReferenceGroup{{
			Role:   videoReferenceRoleIdentity,
			Images: sourceImageRefs(args),
		}}
	}

	prompt := strings.TrimSpace(strArg(args, "prompt", ""))
	lowerPrompt := strings.ToLower(prompt)
	if profile.Style == "elements" {
		identityCount := 0
		for _, group := range groups {
			if group.Role == videoReferenceRoleIdentity {
				identityCount++
			}
		}
		if identityCount == 0 || strings.Contains(lowerPrompt, "@element") {
			return nil
		}
		tokens := make([]string, 0, identityCount)
		for i := 1; i <= identityCount; i++ {
			tokens = append(tokens, fmt.Sprintf("@Element%d", i))
		}
		args["prompt"] = strings.TrimSpace(prompt + " Use " + strings.Join(tokens, " and ") +
			" as the subject identity reference and preserve the exact identity.")
		return nil
	}

	imageOffset := 0
	var identityTokens []string
	for _, group := range groups {
		if group.Role == videoReferenceRoleIdentity {
			for i := range group.Images {
				identityTokens = append(identityTokens, veniceFlatReferenceToken(model, imageOffset+i+1))
			}
		}
		imageOffset += len(group.Images)
	}
	if len(identityTokens) == 0 {
		return nil
	}
	for _, token := range identityTokens {
		if strings.Contains(lowerPrompt, strings.ToLower(token)) {
			return nil
		}
	}
	args["prompt"] = strings.TrimSpace(prompt + " " + strings.Join(identityTokens, " and ") +
		" show the same subject; preserve that subject's exact identity.")
	return nil
}

func veniceFlatReferenceToken(model string, index int) string {
	lower := strings.ToLower(model)
	switch {
	case strings.HasPrefix(lower, "happyhorse-1-0-"):
		return fmt.Sprintf("character%d", index)
	case strings.HasPrefix(lower, "happyhorse-"):
		return fmt.Sprintf("[Image %d]", index)
	default:
		return fmt.Sprintf("@Image%d", index)
	}
}

func buildVeniceReferenceArgs(model string, args, out map[string]any) error {
	profile := veniceReferenceProfile(model)
	groups, err := videoReferenceGroups(args, true)
	if err != nil {
		return err
	}
	flatRefs := resolvedSourceImages(args)

	if len(groups) == 0 && len(flatRefs) > 0 {
		if profile.Style == "elements" {
			groups = []videoReferenceGroup{{
				Role:   videoReferenceRoleIdentity,
				Images: flatRefs,
			}}
			flatRefs = nil
		}
	}
	if err := validateVeniceVideoReferences(model, groups, flatRefs); err != nil {
		return err
	}

	if profile.Style == "flat" {
		urls := make([]string, 0)
		for _, group := range groups {
			for _, ref := range group.Images {
				urls = append(urls, ensureDataURL(ref))
			}
		}
		for _, ref := range flatRefs {
			urls = append(urls, ensureDataURL(ref))
		}
		if len(urls) > 0 {
			out["reference_image_urls"] = urls
		}
		return nil
	}

	elements := make([]map[string]any, 0)
	sceneURLs := make([]string, 0)
	for _, group := range groups {
		if group.Role == videoReferenceRoleIdentity {
			element := map[string]any{
				"frontal_image_url": ensureDataURL(group.Images[0]),
			}
			if len(group.Images) > 1 {
				refs := make([]string, 0, len(group.Images)-1)
				for _, ref := range group.Images[1:] {
					refs = append(refs, ensureDataURL(ref))
				}
				element["reference_image_urls"] = refs
			}
			elements = append(elements, element)
			continue
		}
		for _, ref := range group.Images {
			sceneURLs = append(sceneURLs, ensureDataURL(ref))
		}
	}
	for _, ref := range flatRefs {
		sceneURLs = append(sceneURLs, ensureDataURL(ref))
	}
	if len(elements) > 0 {
		out["elements"] = elements
	}
	if len(sceneURLs) > 0 {
		out["scene_image_urls"] = sceneURLs
	}
	return nil
}
