package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"golang.org/x/image/font/sfnt"
	"math"
	"regexp"
)

type RigBone struct {
	Name     string  `json:"name"`
	Parent   string  `json:"parent,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Rotation float64 `json:"rotation"`
}
type RigAttachment struct {
	Bone  string `json:"bone"`
	Asset string `json:"asset"`
	Frame string `json:"frame"`
}
type RigKey struct {
	TimeMS   int     `json:"time_ms"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Rotation float64 `json:"rotation"`
}
type RigTrack struct {
	Bone string   `json:"bone"`
	Keys []RigKey `json:"keys"`
}
type RigClip struct {
	Name       string     `json:"name"`
	DurationMS int        `json:"duration_ms"`
	Loop       bool       `json:"loop"`
	Tracks     []RigTrack `json:"tracks"`
}
type RigSpec struct {
	Bones       []RigBone       `json:"bones"`
	Attachments []RigAttachment `json:"attachments"`
	Clips       []RigClip       `json:"clips,omitempty"`
}
type MaterialSpec struct {
	Model   string    `json:"model"`
	Texture string    `json:"texture,omitempty"`
	Tint    []float64 `json:"tint"`
}
type FontSpec struct {
	Family    string   `json:"family"`
	Fallbacks []string `json:"fallbacks,omitempty"`
}
type StreamTrack struct {
	Asset string  `json:"asset"`
	Gain  float64 `json:"gain"`
	Loop  bool    `json:"loop"`
}
type StreamSpec struct {
	Mode   string        `json:"mode"`
	Tracks []StreamTrack `json:"tracks"`
}
type LocaleEntry struct {
	Text    string            `json:"text"`
	Plural  map[string]string `json:"plural,omitempty"`
	Context string            `json:"context,omitempty"`
}
type LocaleSpec struct {
	Language string                 `json:"language"`
	Entries  map[string]LocaleEntry `json:"entries"`
}
type TableColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}
type TableSpec struct {
	SchemaVersion int              `json:"schema_version"`
	Key           string           `json:"key"`
	Columns       []TableColumn    `json:"columns"`
	Rows          []map[string]any `json:"rows"`
}

func finiteBound(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && math.Abs(n) <= 1000000
}
func validateKind(kind string, s AssetSpec) error {
	typed := map[string]bool{"rig": s.Rig != nil, "material": s.Material != nil, "font": s.Font != nil, "stream": s.Stream != nil, "locale": s.Locale != nil, "table": s.Table != nil}
	for k, present := range typed {
		if present && k != kind {
			return fmt.Errorf("%s specification is not valid for kind %s", k, kind)
		}
		if k == kind && !present {
			return fmt.Errorf("%s specification required", kind)
		}
	}
	dependencies := map[string]bool{}
	for _, d := range s.Dependencies {
		dependencies[d.Asset] = true
	}
	switch kind {
	case "rig":
		r := s.Rig
		if len(r.Bones) < 1 || len(r.Bones) > 64 || len(r.Attachments) > 256 || len(r.Clips) > 64 {
			return errors.New("rig requires 1-64 bones, at most 256 attachments and 64 clips")
		}
		bones := map[string]bool{}
		for _, b := range r.Bones {
			if !contentSlug.MatchString(b.Name) || bones[b.Name] || b.Parent != "" && !bones[b.Parent] || !finiteBound(b.X) || !finiteBound(b.Y) || !finiteBound(b.Rotation) {
				return errors.New("rig bones need unique names, parent-before-child order and finite transforms")
			}
			bones[b.Name] = true
		}
		for _, a := range r.Attachments {
			if !bones[a.Bone] || !dependencies[a.Asset] || !contentSlug.MatchString(a.Frame) {
				return errors.New("rig attachment requires a bone, pinned sprite dependency and frame")
			}
		}
		clips := map[string]bool{}
		for _, clip := range r.Clips {
			if !contentSlug.MatchString(clip.Name) || clips[clip.Name] || clip.DurationMS < 1 || clip.DurationMS > 600000 || len(clip.Tracks) > 64 {
				return errors.New("invalid rig clip")
			}
			clips[clip.Name] = true
			tracks := map[string]bool{}
			for _, track := range clip.Tracks {
				if !bones[track.Bone] || tracks[track.Bone] || len(track.Keys) == 0 || len(track.Keys) > 256 {
					return errors.New("invalid rig track")
				}
				tracks[track.Bone] = true
				last := -1
				for _, key := range track.Keys {
					if key.TimeMS <= last || key.TimeMS > clip.DurationMS || !finiteBound(key.X) || !finiteBound(key.Y) || !finiteBound(key.Rotation) {
						return errors.New("rig keys must have increasing times within the clip")
					}
					last = key.TimeMS
				}
			}
		}
	case "material":
		m := s.Material
		if m.Model != "sprite2d" || len(m.Tint) != 4 {
			return errors.New("material requires model sprite2d and four tint components")
		}
		for _, n := range m.Tint {
			if n < 0 || n > 1 || !finiteBound(n) {
				return errors.New("material tint must be within 0-1")
			}
		}
		if m.Texture != "" && !dependencies[m.Texture] {
			return errors.New("material texture must be a pinned dependency")
		}
	case "font":
		if len(s.Font.Family) == 0 || len(s.Font.Family) > 200 || len(s.Font.Fallbacks) > 16 {
			return errors.New("font family required, at most 16 fallbacks")
		}
		for _, f := range s.Font.Fallbacks {
			if !dependencies[f] {
				return errors.New("font fallback must be pinned")
			}
		}
	case "stream":
		st := s.Stream
		if st.Mode != "sequence" && st.Mode != "layers" {
			return errors.New("stream mode must be sequence or layers")
		}
		if len(st.Tracks) == 0 || len(st.Tracks) > 32 {
			return errors.New("stream requires 1-32 tracks")
		}
		for _, track := range st.Tracks {
			if !dependencies[track.Asset] || track.Gain < 0 || track.Gain > 4 || !finiteBound(track.Gain) {
				return errors.New("stream tracks require pinned audio assets and gain within 0-4")
			}
		}
	case "locale":
		l := s.Locale
		if !regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`).MatchString(l.Language) || len(l.Entries) == 0 || len(l.Entries) > 2000 {
			return errors.New("locale requires a language tag and 1-2000 entries")
		}
		for key, entry := range l.Entries {
			if len(key) == 0 || len(key) > 128 || len(entry.Text) > 8192 || len(entry.Context) > 2048 {
				return errors.New("invalid locale entry")
			}
			if len(entry.Plural) > 0 {
				if _, ok := entry.Plural["other"]; !ok {
					return errors.New("plural forms require other")
				}
				for form, text := range entry.Plural {
					switch form {
					case "zero", "one", "two", "few", "many", "other":
					default:
						return errors.New("invalid plural category")
					}
					if len(text) > 8192 {
						return errors.New("plural text too long")
					}
				}
			}
		}
	case "table":
		t := s.Table
		if t.SchemaVersion < 1 || len(t.Columns) == 0 || len(t.Columns) > 64 || len(t.Rows) > 2000 {
			return errors.New("table requires schema_version, 1-64 columns and at most 2000 rows")
		}
		columns := map[string]TableColumn{}
		for _, c := range t.Columns {
			if !contentSlug.MatchString(c.Name) {
				return errors.New("invalid column name")
			}
			if _, exists := columns[c.Name]; exists {
				return errors.New("duplicate table column")
			}
			switch c.Type {
			case "string", "number", "integer", "boolean", "asset":
			default:
				return errors.New("unsupported table column type")
			}
			columns[c.Name] = c
		}
		key, exists := columns[t.Key]
		if !exists || !key.Required || key.Type != "string" {
			return errors.New("table key must name a required string column")
		}
		keys := map[string]bool{}
		for _, row := range t.Rows {
			for name := range row {
				if _, ok := columns[name]; !ok {
					return errors.New("unknown table column")
				}
			}
			for name, c := range columns {
				v, present := row[name]
				if !present || v == nil {
					if c.Required {
						return errors.New("missing required table value")
					}
					continue
				}
				switch c.Type {
				case "string":
					str, ok := v.(string)
					if !ok || len(str) > 8192 {
						return errors.New("invalid string value")
					}
				case "asset":
					str, ok := v.(string)
					if !ok || !dependencies[str] {
						return errors.New("asset column must reference a pinned dependency")
					}
				case "boolean":
					if _, ok := v.(bool); !ok {
						return errors.New("invalid boolean value")
					}
				case "number", "integer":
					n, ok := v.(float64)
					if !ok || !finiteBound(n) || (c.Type == "integer" && math.Trunc(n) != n) {
						return errors.New("invalid numeric value")
					}
				}
			}
			id, _ := row[t.Key].(string)
			if id == "" || keys[id] {
				return errors.New("table key must be nonempty and unique")
			}
			keys[id] = true
		}
	}
	return nil
}
func fontFormat(b []byte) (string, error) {
	if len(b) < 12 {
		return "", errors.New("invalid font header")
	}
	ext := "ttf"
	if string(b[:4]) == "OTTO" {
		ext = "otf"
	} else if binary.BigEndian.Uint32(b[:4]) != 0x00010000 {
		return "", errors.New("font must be a standalone TTF or OTF")
	}
	n := int(binary.BigEndian.Uint16(b[4:6]))
	if n == 0 || n > 128 || len(b) < 12+16*n {
		return "", errors.New("invalid font table directory")
	}
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		record := b[12+16*i : 28+16*i]
		tag := string(record[:4])
		offset, length := uint64(binary.BigEndian.Uint32(record[8:12])), uint64(binary.BigEndian.Uint32(record[12:16]))
		if seen[tag] || offset < uint64(12+16*n) || offset+length > uint64(len(b)) {
			return "", errors.New("font table outside source bytes")
		}
		seen[tag] = true
	}
	if _, e := sfnt.Parse(b); e != nil {
		return "", errors.New("invalid font tables")
	}
	return ext, nil
}
func dataSpec(kind string, s AssetSpec) any {
	switch kind {
	case "rig":
		return s.Rig
	case "material":
		return s.Material
	case "stream":
		return s.Stream
	case "locale":
		return s.Locale
	case "table":
		return s.Table
	case "style":
		return map[string]any{"palette": s.Palette, "rules": s.Rules}
	default:
		return s
	}
}
func decodeAssetSpec(raw string) (AssetSpec, error) {
	var s AssetSpec
	e := json.Unmarshal([]byte(raw), &s)
	return s, e
}

func validateAssetDependencies(ctx *sdk.AppCtx, s GameScope, kind string, spec AssetSpec) error {
	deps := map[string]AssetVersion{}
	styles := 0
	for _, d := range spec.Dependencies {
		v, e := assetVersionGet(ctx, s, d.Version)
		if e != nil {
			return e
		}
		deps[d.Asset] = v
		if v.Kind == "style" {
			styles++
		}
	}
	if styles > 1 {
		return errors.New("choose one exact style version")
	}
	isSprite := func(v AssetVersion) bool { return v.Kind == "sprite" || v.Kind == "spriteset" || v.Kind == "tileset" }
	if spec.Rig != nil {
		for _, a := range spec.Rig.Attachments {
			v := deps[a.Asset]
			if !isSprite(v) {
				return errors.New("rig attachment must reference a sprite kind")
			}
			found := len(v.Spec.Frames) == 0 && a.Frame == "default"
			for _, f := range v.Spec.Frames {
				found = found || f.Name == a.Frame
			}
			if !found {
				return errors.New("rig attachment frame is absent from the pinned version")
			}
		}
	}
	if spec.Material != nil && spec.Material.Texture != "" && !isSprite(deps[spec.Material.Texture]) {
		return errors.New("material texture must reference a sprite kind")
	}
	if spec.Font != nil {
		for _, id := range spec.Font.Fallbacks {
			if deps[id].Kind != "font" {
				return errors.New("font fallback must reference a font")
			}
		}
	}
	if spec.Stream != nil {
		for _, track := range spec.Stream.Tracks {
			v := deps[track.Asset]
			if v.Kind != "sfx" && v.Kind != "music" {
				return errors.New("stream tracks must reference music or sfx")
			}
		}
	}
	return nil
}
