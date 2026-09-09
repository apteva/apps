package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func hashBytes(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

var contentBakeSlots = make(chan struct{}, 2)

func validateSprite(b []byte, s AssetSpec) (image.Image, error) {
	cfg, e := png.DecodeConfig(bytes.NewReader(b))
	if e != nil {
		return nil, errors.New("sprite source must be a valid PNG")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || cfg.Width*cfg.Height > 16<<20 {
		return nil, errors.New("sprite exceeds 4096x4096")
	}
	if (s.Width > 0 && s.Width != cfg.Width) || (s.Height > 0 && s.Height != cfg.Height) {
		return nil, errors.New("source canvas does not match specification")
	}
	for _, f := range s.Frames {
		if f.X+f.Width > cfg.Width || f.Y+f.Height > cfg.Height {
			return nil, errors.New("frame lies outside source canvas")
		}
	}
	img, e := png.Decode(bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	transparent, visible := false, false
	palette := map[uint32]bool{}
	for _, c := range s.Palette {
		value, _ := strconv.ParseUint(c[1:], 16, 32)
		palette[uint32(value)] = true
	}
	for y := 0; y < cfg.Height; y++ {
		for x := 0; x < cfg.Width; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			transparent = transparent || c.A < 255
			visible = visible || c.A > 0
			if c.A > 0 && len(palette) > 0 && !palette[uint32(c.R)<<16|uint32(c.G)<<8|uint32(c.B)] {
				return nil, errors.New("source uses colors outside the pinned palette")
			}
		}
	}
	if !visible {
		return nil, errors.New("sprite is entirely transparent")
	}
	if s.RequireAlpha && !transparent {
		return nil, errors.New("sprite requires transparency but source is opaque")
	}
	return img, nil
}
func validAudio(b []byte) bool {
	return len(b) >= 12 && (string(b[:4]) == "OggS" || (string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE") || string(b[:3]) == "ID3" || (b[0] == 255 && b[1]&0xe0 == 0xe0))
}

type ContentFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Size   int    `json:"size"`
}
type AssetRendition struct {
	Font          *FontSpec        `json:"font,omitempty"`
	ID            string           `json:"id"`
	Version       string           `json:"version"`
	Asset         string           `json:"asset"`
	Kind          string           `json:"kind"`
	Target        string           `json:"target"`
	EngineVersion string           `json:"engine_version"`
	Platform      string           `json:"platform"`
	Baker         string           `json:"baker"`
	Scale         int              `json:"scale"`
	Width         int              `json:"width,omitempty"`
	Height        int              `json:"height,omitempty"`
	PixelsPerUnit float64          `json:"pixels_per_unit,omitempty"`
	Frames        []AssetFrame     `json:"frames,omitempty"`
	Animations    []AssetAnimation `json:"animations,omitempty"`
	Files         []ContentFile    `json:"files"`
}

func renditionGet(ctx *sdk.AppCtx, s GameScope, id string) (AssetRendition, error) {
	var r AssetRendition
	var raw string
	e := ctx.AppDB().QueryRow(`SELECT document FROM game_renditions WHERE project_id=? AND game_id=? AND id=?`, s.ProjectID, s.GameID, id).Scan(&raw)
	if e != nil {
		return r, errors.New("rendition not found for this game")
	}
	e = json.Unmarshal([]byte(raw), &r)
	return r, e
}
func bakeSprite(img image.Image, spec AssetSpec, scale int) ([]byte, []AssetFrame, int, int, error) {
	frames := append([]AssetFrame(nil), spec.Frames...)
	if len(frames) == 0 {
		frames = []AssetFrame{{Name: "default", Width: img.Bounds().Dx(), Height: img.Bounds().Dy(), PivotX: 0.5, PivotY: 0.5, DurationMS: 100}}
	}
	// Stable input order, shelf packing, two extruded pixels per side. No rotation
	// or trimming: pivots and frame canvas are preserved exactly across engines.
	const pad = 2
	x, y, rowHeight, width := pad, pad, 0, 0
	out := make([]AssetFrame, len(frames))
	for i, f := range frames {
		w, h := f.Width*scale, f.Height*scale
		if w+2*pad > 4096 || h+2*pad > 4096 {
			return nil, nil, 0, 0, errors.New("scaled frame exceeds atlas limit")
		}
		if x+w+pad > 4096 {
			x = pad
			y += rowHeight + 2*pad
			rowHeight = 0
		}
		if y+h+pad > 4096 {
			return nil, nil, 0, 0, errors.New("atlas exceeds 4096x4096; split the spriteset")
		}
		g := f
		g.X = x
		g.Y = y
		g.Width = w
		g.Height = h
		out[i] = g
		x += w + 2*pad
		if h > rowHeight {
			rowHeight = h
		}
		if x > width {
			width = x - pad
		}
	}
	height := y + rowHeight + pad
	atlas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for i, f := range frames {
		g := out[i]
		for yy := -pad; yy < g.Height+pad; yy++ {
			for xx := -pad; xx < g.Width+pad; xx++ {
				sx := max(0, min(g.Width-1, xx)) / scale
				sy := max(0, min(g.Height-1, yy)) / scale
				atlas.Set(g.X+xx, g.Y+yy, img.At(f.X+sx, f.Y+sy))
			}
		}
	}
	var buf bytes.Buffer
	e := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, atlas)
	return buf.Bytes(), out, width, height, e
}
func bakeAudio(b []byte, target string) ([]byte, string, string, error) {
	exe, e := exec.LookPath("ffmpeg")
	if e != nil {
		return nil, "", "", errors.New("audio baking requires ffmpeg on the Games runner")
	}
	call, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	version, e := exec.CommandContext(call, exe, "-version").Output()
	if e != nil {
		return nil, "", "", e
	}
	first := strings.SplitN(string(version), "\n", 2)[0]
	dir, e := os.MkdirTemp("", "games-audio-")
	if e != nil {
		return nil, "", "", e
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "master")
	if e = os.WriteFile(input, b, 0600); e != nil {
		return nil, "", "", e
	}
	probe, e := exec.LookPath("ffprobe")
	if e != nil {
		return nil, "", "", errors.New("audio baking requires ffprobe")
	}
	probeOut, e := exec.CommandContext(call, probe, "-v", "error", "-protocol_whitelist", "file,pipe", "-show_entries", "format=duration", "-of", "json", input).Output()
	if e != nil {
		return nil, "", "", errors.New("cannot inspect audio duration")
	}
	var info struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if e = json.Unmarshal(probeOut, &info); e != nil {
		return nil, "", "", e
	}
	duration, e := strconv.ParseFloat(info.Format.Duration, 64)
	if e != nil || duration <= 0 || duration > 600 {
		return nil, "", "", errors.New("audio duration must be between zero and 600 seconds")
	}
	ext, codec := "ogg", "libvorbis"
	if target == "generic" {
		ext, codec = "mp3", "libmp3lame"
	}
	output := filepath.Join(dir, "output."+ext)
	cmd := exec.CommandContext(call, exe, "-nostdin", "-v", "error", "-protocol_whitelist", "file,pipe", "-i", input, "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact", "-vn", "-ac", "2", "-ar", "44100", "-c:a", codec, "-b:a", "160k", output)
	if out, e := cmd.CombinedOutput(); e != nil {
		return nil, "", "", fmt.Errorf("audio bake failed: %s: %w", string(out[:min(len(out), 1000)]), e)
	}
	data, e := os.ReadFile(output)
	return data, ext, first, e
}
func assetBake(ctx *sdk.AppCtx, s GameScope, args map[string]any) (any, error) {
	select {
	case contentBakeSlots <- struct{}{}:
		defer func() { <-contentBakeSlots }()
	default:
		return nil, errors.New("bake workers busy; retry later")
	}
	v, e := assetVersionGet(ctx, s, txt(args["version_id"]))
	if e != nil {
		return nil, e
	}
	target, engine, platform := txt(args["target"]), txt(args["engine_version"]), txt(args["platform"])
	if (target != "generic" && target != "unity" && target != "godot") || !contentSlug.MatchString(platform) || len(engine) == 0 || len(engine) > 64 {
		return nil, errors.New("target generic/unity/godot, engine_version and platform required")
	}
	scale := int(number(args["scale"]))
	if scale == 0 {
		scale = 1
	}
	if scale < 1 || scale > 4 {
		return nil, errors.New("scale must be 1-4")
	}
	r := AssetRendition{Version: v.ID, Asset: v.AssetID, Kind: v.Kind, Target: target, EngineVersion: engine, Platform: platform, Baker: "games-assets-1", Scale: scale, Animations: v.Spec.Animations, Files: []ContentFile{}}
	var b []byte
	mime, ext := "application/json", "json"
	spec := v.Spec
	for _, dep := range spec.Dependencies {
		style, e := assetVersionGet(ctx, s, dep.Version)
		if e != nil {
			return nil, e
		}
		if style.Kind == "style" {
			if len(spec.Palette) > 0 && contentJSON(spec.Palette) != contentJSON(style.Spec.Palette) {
				return nil, errors.New("asset palette conflicts with pinned style")
			}
			spec.Palette = style.Spec.Palette
		}
	}
	if v.Kind == "style" || v.Kind == "rig" || v.Kind == "material" || v.Kind == "stream" || v.Kind == "locale" || v.Kind == "table" {
		b = []byte(contentJSON(dataSpec(v.Kind, v.Spec)))
	} else {
		if v.Source == "" {
			return nil, errors.New("recipe has no approved source bytes; generate or import a source first")
		}
		b, mime, e = contentBlob(ctx, s, v.Source)
		if e != nil {
			return nil, e
		}
	}
	switch v.Kind {
	case "sprite", "spriteset", "tileset":
		img, e := validateSprite(b, spec)
		if e != nil {
			return nil, e
		}
		b, r.Frames, r.Width, r.Height, e = bakeSprite(img, spec, scale)
		if e != nil {
			return nil, e
		}
		mime, ext = "image/png", "png"
		r.PixelsPerUnit = spec.PixelsPerUnit
		if r.PixelsPerUnit == 0 {
			r.PixelsPerUnit = 100
		}
		r.PixelsPerUnit *= float64(scale)
	case "sfx", "music":
		var encoder string
		b, ext, encoder, e = bakeAudio(b, target)
		if e != nil {
			return nil, e
		}
		r.Baker += "/" + encoder
		mime = "audio/ogg"
		if ext == "mp3" {
			mime = "audio/mpeg"
		}
	case "font":
		r.Font = v.Spec.Font
		ext, e = fontFormat(b)
		if e != nil {
			return nil, e
		}
		mime = "font/" + ext
	case "blob":
		ext = "bin"
	}
	sha := contentSHA(b)
	r.Files = append(r.Files, ContentFile{Path: v.AssetID + "/" + sha + "." + ext, SHA256: sha, MIME: mime, Size: len(b)})
	r.ID = studioHash(r)
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = contentBlobPut(tx, s, b, mime); e != nil {
		return nil, e
	}
	_, e = tx.Exec(`INSERT OR IGNORE INTO game_renditions VALUES(?,?,?,?,?)`, s.ProjectID, s.GameID, r.ID, v.ID, contentJSON(r))
	if e != nil {
		return nil, e
	}
	return r, tx.Commit()
}
