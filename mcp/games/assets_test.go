package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type contentFake struct {
	*studioFake
	calls   int
	fail    bool
	data    []byte
	request map[string]any
	corrupt bool
}

func (f *contentFake) CallAppResult(app, tool string, in map[string]any, out any) error {
	if app != "media-studio" && app != "storage" {
		return f.studioFake.CallAppResult(app, tool, in, out)
	}
	if in["_project_id"] != "test-proj" {
		return errors.New("project scope missing")
	}
	var result any
	switch tool {
	case "files_get_content":
		result = map[string]any{"id": in["id"], "content_base64": base64.StdEncoding.EncodeToString(f.data), "content_type": "image/png", "size_bytes": len(f.data)}
	case "media_generate":
		f.calls++
		f.request = in
		if f.fail {
			return errors.New("lost reply")
		}
		result = map[string]any{"_meta": map[string]any{"generation_id": 19}}
	case "media_asset_source":
		sha := hashBytes(f.data)
		if f.corrupt {
			sha = strings.Repeat("0", 64)
		}
		result = map[string]any{"generation_id": 19, "request_json": contentJSON(f.request), "provider": "fixture", "model": "fixture-model", "sha256": sha, "size_bytes": len(f.data), "content_base64": base64.StdEncoding.EncodeToString(f.data), "content_type": "image/png"}
	default:
		return errors.New("unexpected tool")
	}
	b, _ := json.Marshal(result)
	return json.Unmarshal(b, out)
}
func contentFixture(t *testing.T) (*sdk.AppCtx, *contentFake, GameScope) {
	t.Helper()
	f := &contentFake{studioFake: &studioFake{fakeAuth: newFakeAuth(t), bindings: map[string]any{"auth": int64(2), "storage": int64(5), "media-studio": int64(6)}}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(f))
	if e := initializeGames(ctx); e != nil {
		t.Fatal(e)
	}
	if e := initializeStudio(ctx); e != nil {
		t.Fatal(e)
	}
	if e := initializeContent(ctx); e != nil {
		t.Fatal(e)
	}
	out, e := gameAction(ctx, "create", map[string]any{"slug": "content-game", "name": "Content game"})
	if e != nil {
		t.Fatal(e)
	}
	return ctx, f, out.(map[string]any)["game"].(*Game).Scope()
}
func spriteBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for y := 1; y < 3; y++ {
		for x := 1; x < 7; x++ {
			img.SetNRGBA(x, y, color.NRGBA{255, 0, 0, 255})
		}
	}
	var buf bytes.Buffer
	if e := png.Encode(&buf, img); e != nil {
		t.Fatal(e)
	}
	return buf.Bytes()
}
func spriteSpec() AssetSpec {
	return AssetSpec{License: "Owned test artwork", Width: 8, Height: 4, RequireAlpha: true, PixelsPerUnit: 4, Frames: []AssetFrame{{Name: "left", Width: 4, Height: 4, PivotX: .5, PivotY: 1, DurationMS: 100}, {Name: "right", X: 4, Width: 4, Height: 4, PivotX: .5, PivotY: 1, DurationMS: 200}}, Animations: []AssetAnimation{{Name: "idle", Frames: []string{"left", "right"}, Loop: true}}}
}
func saveSprite(t *testing.T, ctx *sdk.AppCtx, s GameScope) AssetVersion {
	t.Helper()
	out, e := assetSave(ctx, s, map[string]any{"asset_id": "hero", "kind": "spriteset", "name": "Hero", "expected_parent": "", "spec": spriteSpec()}, spriteBytes(t), "image/png", map[string]any{"method": "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	return out.(AssetVersion)
}
func bakeFixture(t *testing.T, ctx *sdk.AppCtx, s GameScope, v AssetVersion, target string) AssetRendition {
	t.Helper()
	out, e := assetBake(ctx, s, map[string]any{"version_id": v.ID, "target": target, "engine_version": map[string]string{"godot": "4.5.1", "unity": "6000.0", "generic": "1"}[target], "platform": "desktop"})
	if e != nil {
		t.Fatal(e)
	}
	return out.(AssetRendition)
}
func freezeFixture(t *testing.T, ctx *sdk.AppCtx, s GameScope, rs ...AssetRendition) string {
	t.Helper()
	ids := []string{}
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	out, e := contentFreeze(ctx, s, map[string]any{"rendition_ids": ids})
	if e != nil {
		t.Fatal(e)
	}
	return out.(map[string]any)["digest"].(string)
}
func TestAssetsVersionsIsolationAndConflicts(t *testing.T) {
	ctx, _, s := contentFixture(t)
	v := saveSprite(t, ctx, s)
	if _, e := assetVersionGet(ctx, GameScope{ProjectID: s.ProjectID, GameID: "other"}, v.ID); e == nil {
		t.Fatal("cross-game version read")
	}
	args := map[string]any{"asset_id": "hero", "kind": "spriteset", "name": "New name", "expected_parent": v.ID, "spec": spriteSpec(), "source": v.Source}
	out, e := assetImport(ctx, s, args)
	if e != nil {
		t.Fatal(e)
	}
	v2 := out.(AssetVersion)
	if v.ID == v2.ID || v2.Parent != v.ID {
		t.Fatal("not immutable")
	}
	old, e := assetVersionGet(ctx, s, v.ID)
	if e != nil || old.Name != "Hero" {
		t.Fatalf("old name mutated: %#v %v", old, e)
	}
	if _, e = assetImport(ctx, s, args); e == nil {
		t.Fatal("stale parent accepted")
	}
	if e = initializeContent(ctx); e != nil {
		t.Fatal("restart migration", e)
	}
}
func TestAssetsSourceValidationAndBaking(t *testing.T) {
	ctx, _, s := contentFixture(t)
	v := saveSprite(t, ctx, s)
	r := bakeFixture(t, ctx, s, v, "godot")
	r2 := bakeFixture(t, ctx, s, v, "godot")
	if r.ID != r2.ID {
		t.Fatal("nondeterministic bake")
	}
	b, _, e := contentBlob(ctx, s, r.Files[0].SHA256)
	if e != nil {
		t.Fatal(e)
	}
	img, e := png.Decode(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Frames) != 2 || r.Frames[1].DurationMS != 200 || r.Frames[1].PivotY != 1 {
		t.Fatal("lost frame metadata")
	}
	c := color.NRGBAModel.Convert(img.At(r.Frames[0].X+1, r.Frames[0].Y+1)).(color.NRGBA)
	if c.R != 255 || c.A != 255 {
		t.Fatal("pixels changed")
	}
	invalid := spriteSpec()
	invalid.Frames[0].X = 100
	if _, e = validateSprite(spriteBytes(t), invalid); e == nil {
		t.Fatal("out of bounds accepted")
	}
	invalid = spriteSpec()
	invalid.Palette = []string{"#00ff00"}
	if _, e = validateSprite(spriteBytes(t), invalid); e == nil {
		t.Fatal("palette mismatch accepted")
	}
	if _, e = validateSprite([]byte("not png"), spriteSpec()); e == nil {
		t.Fatal("bad format accepted")
	}
	img2 := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img2.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	png.Encode(&buf, img2)
	if _, e = validateSprite(buf.Bytes(), AssetSpec{RequireAlpha: true}); e == nil {
		t.Fatal("opaque image accepted")
	}
}
func TestAssetsManifestReviewRollbackAndRetainedBytes(t *testing.T) {
	ctx, f, s := contentFixture(t)
	v := saveSprite(t, ctx, s)
	godot := bakeFixture(t, ctx, s, v, "godot")
	unity := bakeFixture(t, ctx, s, v, "unity")
	digest := freezeFixture(t, ctx, s, godot, unity)
	if digest != freezeFixture(t, ctx, s, unity, godot) {
		t.Fatal("manifest order changes digest")
	}
	args := map[string]any{"environment": "prod", "digest": digest, "expected_head": ""}
	if _, e := contentPromote(ctx, s, args); e == nil {
		t.Fatal("unreviewed production release")
	}
	if _, e := contentApprove(context.Background(), ctx, s, map[string]any{"digest": digest, "note": "Reviewed sprite animation"}); e != nil {
		t.Fatal(e)
	}
	if _, e := contentPromote(ctx, s, args); e != nil {
		t.Fatal(e)
	}
	if _, e := contentPromote(ctx, s, args); e == nil {
		t.Fatal("stale head overwrite")
	}
	f.data = nil
	archive, e := contentZip(ctx, s, digest)
	if e != nil {
		t.Fatal(e)
	}
	reader, e := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if e != nil {
		t.Fatal(e)
	}
	if len(reader.File) != 4 {
		t.Fatalf("files=%d", len(reader.File))
	}
	m, e := contentManifestGet(ctx, s, digest)
	if e != nil || len(m.Assets) != 2 {
		t.Fatal("missing engine renditions", e)
	}
	if strings.Contains(contentJSON(m), "fixture") || strings.Contains(contentJSON(m), "recipe") {
		t.Fatal("private generation metadata in runtime manifest")
	}
	if _, e = contentPromote(ctx, s, map[string]any{"environment": "prod", "digest": digest, "expected_head": digest}); e != nil {
		t.Fatal("rollback to retained manifest", e)
	}
}
func TestAssetsPinStyleAndRejectMixedVersions(t *testing.T) {
	ctx, _, s := contentFixture(t)
	v := saveSprite(t, ctx, s)
	r := bakeFixture(t, ctx, s, v, "godot")
	args := map[string]any{"asset_id": "hero", "kind": "spriteset", "name": "Hero", "expected_parent": v.ID, "spec": spriteSpec(), "source": v.Source}
	out, e := assetImport(ctx, s, args)
	if e != nil {
		t.Fatal(e)
	}
	r2 := bakeFixture(t, ctx, s, out.(AssetVersion), "unity")
	if _, e = contentFreeze(ctx, s, map[string]any{"rendition_ids": []string{r.ID, r2.ID}}); e == nil {
		t.Fatal("mixed versions accepted")
	}
	bad := spriteSpec()
	bad.Dependencies = []AssetDependency{{Asset: "absent", Version: strings.Repeat("a", 64)}}
	args["spec"] = bad
	args["expected_parent"] = out.(AssetVersion).ID
	if _, e = assetImport(ctx, s, args); e == nil {
		t.Fatal("missing dependency accepted")
	}
}
func TestAssetsGenerationNoDoubleChargeAndRecovery(t *testing.T) {
	ctx, f, s := contentFixture(t)
	spec := spriteSpec()
	spec.Recipe = map[string]any{"prompt": "two frames", "options": map[string]any{"output_format": "jpeg"}}
	out, e := assetSave(ctx, s, map[string]any{"asset_id": "hero", "kind": "spriteset", "name": "Hero", "expected_parent": "", "spec": spec}, nil, "", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	v := out.(AssetVersion)
	f.data = spriteBytes(t)
	f.fail = true
	args := map[string]any{"version_id": v.ID, "request_key": "job-one"}
	first, e := contentGenerate(ctx, s, args)
	if e != nil {
		t.Fatal(e)
	}
	if first.(map[string]any)["status"] != "unknown" {
		t.Fatal(first)
	}
	if _, e = contentGenerate(ctx, s, args); e != nil {
		t.Fatal(e)
	}
	if f.calls != 1 {
		t.Fatal("duplicate provider call")
	}
	if object(f.request["options"])["output_format"] != "png" {
		t.Fatal("sprite requested JPEG")
	}
	out, e = contentGenerationSync(ctx, s, map[string]any{"request_key": "job-one", "generation_id": 19})
	if e != nil {
		t.Fatal(e)
	}
	if out.(map[string]any)["version"].(AssetVersion).Source == "" {
		t.Fatal("source absent")
	}
	if _, e = contentGenerationSync(ctx, s, map[string]any{"request_key": "job-one"}); e != nil {
		t.Fatal(e)
	}
	if f.calls != 1 {
		t.Fatal("sync regenerated")
	}
}
func TestAssetsGenerationRejectsWrongReceipt(t *testing.T) {
	ctx, f, s := contentFixture(t)
	spec := spriteSpec()
	spec.Recipe = map[string]any{"prompt": "sprite"}
	out, e := assetSave(ctx, s, map[string]any{"asset_id": "hero", "kind": "sprite", "name": "Hero", "expected_parent": "", "spec": spec}, nil, "", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	f.data = spriteBytes(t)
	if _, e = contentGenerate(ctx, s, map[string]any{"version_id": out.(AssetVersion).ID, "request_key": "one"}); e != nil {
		t.Fatal(e)
	}
	f.request["cache_key"] = "wrong"
	if _, e = contentGenerationSync(ctx, s, map[string]any{"request_key": "one"}); e == nil {
		t.Fatal("wrong request accepted")
	}
}
func TestAssetsAudioRenditions(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg not installed")
	}
	ctx, _, s := contentFixture(t)
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
	out, e := assetSave(ctx, s, map[string]any{"asset_id": "click", "kind": "sfx", "name": "Click", "expected_parent": "", "spec": AssetSpec{License: "Test"}}, b.Bytes(), "audio/wav", map[string]any{})
	if e != nil {
		t.Fatal(e)
	}
	r := bakeFixture(t, ctx, s, out.(AssetVersion), "godot")
	if r.Files[0].MIME != "audio/ogg" {
		t.Fatal(r)
	}
	r2 := bakeFixture(t, ctx, s, out.(AssetVersion), "generic")
	if r2.Files[0].MIME != "audio/mpeg" {
		t.Fatal(r2)
	}
}
func TestAssetsExportFixture(t *testing.T) {
	dir := os.Getenv("GAMES_CONTENT_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set GAMES_CONTENT_FIXTURE_DIR to export a real engine fixture")
	}
	ctx, _, s := contentFixture(t)
	v := saveSprite(t, ctx, s)
	r := bakeFixture(t, ctx, s, v, "godot")
	u := bakeFixture(t, ctx, s, v, "unity")
	digest := freezeFixture(t, ctx, s, r, u)
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
		name := filepath.Join(dir, f.Name)
		os.MkdirAll(filepath.Dir(name), 0755)
		if e = os.WriteFile(name, data, 0644); e != nil {
			t.Fatal(e)
		}
	}
	t.Log("Frozen engine fixture", digest)
}

func TestAssetsImmutableRecordsAndPlayerDelivery(t *testing.T) {
	f := newFixture(t)
	if e := initializeContent(f.ctx); e != nil {
		t.Fatal(e)
	}
	v := saveSprite(t, f.ctx, f.pid)
	rendition := bakeFixture(t, f.ctx, f.pid, v, "generic")
	digest := freezeFixture(t, f.ctx, f.pid, rendition)
	for _, table := range []string{"game_asset_versions", "game_content_manifests", "game_renditions", "game_content_blobs"} {
		if _, e := f.ctx.AppDB().Exec("DELETE FROM "+table+" WHERE game_id=?", f.pid.GameID); e == nil {
			t.Fatal("immutable delete succeeded", table)
		}
	}
	_, token := f.loginDevice(t, "content-player", "Content Player")
	path := map[string]string{"game_id": f.pid.GameID, "digest": digest, "public": "true"}
	url := "/v2/games/" + f.pid.GameID + "/content/" + digest + "?project_id=test-proj"
	w := doReq(f.app.handleContentBytes, "GET", url, nil, path)
	if w.Code != 401 {
		t.Fatalf("anonymous=%d %s", w.Code, w.Body.String())
	}
	w = doReq(f.app.handleContentBytes, "GET", url, nil, path, "Authorization", "Bearer "+token)
	if w.Code != 404 {
		t.Fatalf("draft exposed: %d", w.Code)
	}
	if _, e := contentApprove(context.Background(), f.ctx, f.pid, map[string]any{"digest": digest, "note": "Reviewed fixture"}); e != nil {
		t.Fatal(e)
	}
	if _, e := contentPromote(f.ctx, f.pid, map[string]any{"digest": digest, "environment": "prod", "expected_head": ""}); e != nil {
		t.Fatal(e)
	}
	w = doReq(f.app.handleContentBytes, "GET", url+"&download=manifest", nil, path, "Authorization", "Bearer "+token)
	if w.Code != 200 || hashBytes(w.Body.Bytes()) != digest {
		t.Fatalf("exact manifest unavailable %d %s", w.Code, w.Body.String())
	}
	path["sha"] = v.Source
	w = doReq(f.app.handleContentBytes, "GET", url, nil, path, "Authorization", "Bearer "+token)
	if w.Code != 404 {
		t.Fatalf("source master exposed: %d", w.Code)
	}
	path["sha"] = rendition.Files[0].SHA256
	w = doReq(f.app.handleContentBytes, "GET", url, nil, path, "Authorization", "Bearer "+token)
	if w.Code != 200 || hashBytes(w.Body.Bytes()) != path["sha"] {
		t.Fatal("rendition unavailable")
	}
}
