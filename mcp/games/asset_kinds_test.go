package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/image/font/gofont/goregular"
)

func testWAV() []byte {
	var b bytes.Buffer
	pcm := make([]byte, 4410*2)
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(pcm)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(44100))
	binary.Write(&b, binary.LittleEndian, uint32(88200))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}
func allKindVersions(t *testing.T, ctx *sdk.AppCtx, s GameScope) []AssetVersion {
	t.Helper()
	out := []AssetVersion{}
	save := func(id, kind string, spec AssetSpec, b []byte) AssetVersion {
		t.Helper()
		spec.License = "Test fixture"
		v, e := assetSave(ctx, s, map[string]any{"asset_id": id, "name": id, "kind": kind, "spec": spec, "expected_parent": ""}, b, "application/octet-stream", map[string]any{"method": "fixture"})
		if e != nil {
			t.Fatalf("%s: %v", kind, e)
		}
		out = append(out, v.(AssetVersion))
		return v.(AssetVersion)
	}
	style := save("style", "style", AssetSpec{Palette: []string{"#ff0000"}, Rules: "Red silhouettes"}, nil)
	spec := spriteSpec()
	spec.Dependencies = []AssetDependency{{Asset: style.AssetID, Version: style.ID}}
	sprite := save("sprite", "sprite", spec, spriteBytes(t))
	save("spriteset", "spriteset", spec, spriteBytes(t))
	save("tileset", "tileset", spec, spriteBytes(t))
	save("rig", "rig", AssetSpec{Dependencies: []AssetDependency{{Asset: sprite.AssetID, Version: sprite.ID}}, Rig: &RigSpec{Bones: []RigBone{{Name: "root"}}, Attachments: []RigAttachment{{Bone: "root", Asset: sprite.AssetID, Frame: "left"}}, Clips: []RigClip{{Name: "idle", DurationMS: 1000, Loop: true, Tracks: []RigTrack{{Bone: "root", Keys: []RigKey{{TimeMS: 0}, {TimeMS: 1000, X: 1}}}}}}}}, nil)
	save("material", "material", AssetSpec{Dependencies: []AssetDependency{{Asset: sprite.AssetID, Version: sprite.ID}}, Material: &MaterialSpec{Model: "sprite2d", Texture: sprite.AssetID, Tint: []float64{1, .5, .5, 1}}}, nil)
	save("font", "font", AssetSpec{Font: &FontSpec{Family: "Go Regular"}}, goregular.TTF)
	sfx := save("sfx", "sfx", AssetSpec{}, testWAV())
	music := save("music", "music", AssetSpec{}, testWAV())
	save("stream", "stream", AssetSpec{Dependencies: []AssetDependency{{Asset: sfx.AssetID, Version: sfx.ID}, {Asset: music.AssetID, Version: music.ID}}, Stream: &StreamSpec{Mode: "sequence", Tracks: []StreamTrack{{Asset: sfx.AssetID, Gain: 1}, {Asset: music.AssetID, Gain: .5, Loop: true}}}}, nil)
	save("locale", "locale", AssetSpec{Locale: &LocaleSpec{Language: "en", Entries: map[string]LocaleEntry{"start": {Text: "Start"}, "coins": {Plural: map[string]string{"one": "One coin", "other": "{count} coins"}}}}}, nil)
	save("table", "table", AssetSpec{Table: &TableSpec{SchemaVersion: 1, Key: "id", Columns: []TableColumn{{Name: "id", Type: "string", Required: true}, {Name: "health", Type: "integer", Required: true}}, Rows: []map[string]any{{"id": "goblin", "health": 10}}}}, nil)
	save("blob", "blob", AssetSpec{}, []byte("opaque fixture"))
	return out
}
func TestAssetsAllThirteenKinds(t *testing.T) {
	ctx, _, s := contentFixture(t)
	versions := allKindVersions(t, ctx, s)
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, e := exec.LookPath(tool); e != nil {
			t.Skip("all-kind bake integration requires " + tool)
		}
	}
	if len(versions) != 13 {
		t.Fatal(len(versions))
	}
	renditions := []AssetRendition{}
	for _, v := range versions {
		r := bakeFixture(t, ctx, s, v, "godot")
		renditions = append(renditions, r)
	}
	digest := freezeFixture(t, ctx, s, renditions...)
	m, e := contentManifestGet(ctx, s, digest)
	if e != nil || len(m.Assets) != 13 {
		t.Fatal(e)
	}
	if _, e = contentFreeze(ctx, s, map[string]any{"rendition_ids": []string{renditions[4].ID}}); e == nil {
		t.Fatal("rig without sprite rendition accepted")
	}
	if dir := os.Getenv("GAMES_ALL_KINDS_FIXTURE_DIR"); dir != "" {
		b, e := contentZip(ctx, s, digest)
		if e != nil {
			t.Fatal(e)
		}
		zr, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range zr.File {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(r)
			r.Close()
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(dir, f.Name)
			if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(path, data, 0644); e != nil {
				t.Fatal(e)
			}
		}
		t.Log("All kinds manifest", digest)
	}
}
func TestAssetsTypedValidation(t *testing.T) {
	cases := []struct {
		kind string
		spec AssetSpec
	}{
		{"rig", AssetSpec{Rig: &RigSpec{Bones: []RigBone{{Name: "child", Parent: "missing"}}}}},
		{"material", AssetSpec{Material: &MaterialSpec{Model: "sprite2d", Tint: []float64{1, 1, 1, 2}}}},
		{"font", AssetSpec{Font: &FontSpec{Family: "Test", Fallbacks: []string{"missing"}}}},
		{"stream", AssetSpec{Stream: &StreamSpec{Mode: "sequence", Tracks: []StreamTrack{{Asset: "missing", Gain: 1}}}}},
		{"locale", AssetSpec{Locale: &LocaleSpec{Language: "en", Entries: map[string]LocaleEntry{"coins": {Plural: map[string]string{"one": "one"}}}}}},
		{"table", AssetSpec{Table: &TableSpec{SchemaVersion: 1, Key: "id", Columns: []TableColumn{{Name: "id", Type: "string", Required: true}}, Rows: []map[string]any{{"id": "duplicate"}, {"id": "duplicate"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			if e := validateKind(tc.kind, tc.spec); e == nil {
				t.Fatal("invalid specification accepted")
			}
		})
	}
	if _, e := fontFormat([]byte("not a font")); e == nil {
		t.Fatal("invalid font accepted")
	}
	b := append([]byte(nil), goregular.TTF...)
	binary.BigEndian.PutUint32(b[20:24], 0xffffffff)
	if _, e := fontFormat(b); e == nil {
		t.Fatal("font table overflow accepted")
	}
}
func TestAssetsStorageAndArchivedMutations(t *testing.T) {
	ctx, f, s := contentFixture(t)
	f.data = spriteBytes(t)
	args := map[string]any{"asset_id": "hero", "name": "Hero", "kind": "sprite", "spec": spriteSpec(), "expected_parent": "", "storage_id": 7}
	if _, e := assetImport(ctx, s, args); e != nil {
		t.Fatal(e)
	}
	args["source"] = "another"
	if _, e := assetImport(ctx, s, args); e == nil {
		t.Fatal("ambiguous source")
	}
	if _, e := ctx.AppDB().Exec(`UPDATE games SET status='archived' WHERE project_id=? AND id=?`, s.ProjectID, s.GameID); e != nil {
		t.Fatal(e)
	}
	args["game_id"] = s.GameID
	args["_project_id"] = s.ProjectID
	if _, e := contentAction(nil, ctx, "save", args); e == nil {
		t.Fatal(errors.New("archived game mutation"))
	}
}

func TestAssetsGodotImport(t *testing.T) {
	godot := os.Getenv("GAMES_GODOT_BIN")
	if godot == "" {
		t.Skip("set GAMES_GODOT_BIN to run native engine validation")
	}
	dir := t.TempDir()
	t.Setenv("GAMES_ALL_KINDS_FIXTURE_DIR", filepath.Join(dir, "GameContent"))
	TestAssetsAllThirteenKinds(t)
	if e := os.WriteFile(filepath.Join(dir, "project.godot"), []byte("config_version=5\n[application]\nconfig/name=\"Games Content Test\"\n[rendering]\nrenderer/rendering_method=\"gl_compatibility\"\n"), 0644); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"import_content.gd", "content_sprite.gd"} {
		b, e := os.ReadFile(filepath.Join("engines/godot/addons/apteva_games", name))
		if e != nil {
			t.Fatal(e)
		}
		dest := filepath.Join(dir, "addons/apteva_games", name)
		os.MkdirAll(filepath.Dir(dest), 0755)
		if e = os.WriteFile(dest, b, 0644); e != nil {
			t.Fatal(e)
		}
	}
	run := func(args ...string) string {
		t.Helper()
		call, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		out, e := exec.CommandContext(call, godot, args...).CombinedOutput()
		if e != nil {
			t.Fatalf("Godot failed: %v\n%s", e, out)
		}
		if strings.Contains(string(out), "SCRIPT ERROR") || strings.Contains(string(out), "ERROR:") {
			t.Fatalf("Godot reported error despite exit code: %s", out)
		}
		return string(out)
	}
	run("--headless", "--editor", "--path", dir, "--import")
	out := run("--headless", "--path", dir, "--script", "addons/apteva_games/import_content.gd", "--", "--content=res://GameContent", "--platform=desktop")
	if !strings.Contains(out, "(13 resources)") {
		t.Fatal("missing all-kind import receipt", out)
	}
	// Load native resources again in a fresh engine process, checking timing,
	// pivots, typed data and font/audio decoding rather than only file existence.
	script := `extends SceneTree
func _initialize():
 var locked=JSON.parse_string(FileAccess.get_file_as_string("res://GameContent/content.lock.json"))
 var root="res://GameContent/imports/"+locked.digest+"/"
 var frames=load(root+"sprite.tres")
 assert(frames is SpriteFrames)
 assert(frames.get_frame_count("idle")==2)
 assert(frames.get_frame_duration("idle",1)==200)
 assert(frames.get_frame_texture("idle",0).get_meta("pivot")==Vector2(0.5,1))
 assert(load(root+"tileset-tiles.tres") is TileSet)
 assert(load(root+"font.tres") is Font)
 assert(load(root+"sfx.tres") is AudioStream)
 assert(load(root+"material.tres") is ShaderMaterial)
 assert(load(root+"rig.tres").get_meta("data").bones[0].name=="root")
 assert(load(root+"stream.tres").get_meta("data").tracks.size()==2)
 assert(load(root+"locale.tres").get_meta("data").entries.start.text=="Start")
 assert(load(root+"table.tres").get_meta("data").rows[0].health==10)
 print("CONTENT_RESOURCE_CHECKS_PASSED")
 quit(0)
`
	if e := os.WriteFile(filepath.Join(dir, "verify.gd"), []byte(script), 0644); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(run("--headless", "--path", dir, "--script", "verify.gd"), "CONTENT_RESOURCE_CHECKS_PASSED") {
		t.Fatal("resource checks did not complete")
	}
}
