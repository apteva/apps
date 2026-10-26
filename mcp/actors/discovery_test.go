package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"reflect"
	"strings"
	"testing"
)

func readExportFixture(t *testing.T, a *App, ctx *sdk.AppCtx, args map[string]any) []byte {
	t.Helper()
	args = mergeActorMaps(args, map[string]any{"offset": 0})
	var full []byte
	digest := ""
	total := 0
	for {
		out, err := a.toolActorDefinitionExport(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		m := out.(map[string]any)
		wire, _ := json.Marshal(m)
		if len(wire) > maxActorDiscoveryBytes {
			t.Fatal("export chunk exceeded budget")
		}
		part, err := base64.StdEncoding.DecodeString(m["content_base64"].(string))
		if err != nil {
			t.Fatal(err)
		}
		full = append(full, part...)
		digest = m["sha256"].(string)
		total = m["total_bytes"].(int)
		if !m["has_more"].(bool) {
			break
		}
		args["offset"] = m["next_offset"]
	}
	sum := sha256.Sum256(full)
	if len(full) != total || hex.EncodeToString(sum[:]) != digest {
		t.Fatal("incomplete immutable export")
	}
	return full
}
func TestBoundedDiscoveryAndExplicitImmutableExport(t *testing.T) {
	ctx, a := newTestCtx(t, newFakePlatform())
	def, op := galleryGetFixture(t)
	for i := 0; i < 180; i++ {
		def["operations"].(map[string]any)[fmt.Sprintf("large_%03d", i)] = map[string]any{"steps": []any{map[string]any{"action": "wait", "duration": 1, "label": strings.Repeat("huge unrelated definition ", 120)}}}
	}
	rec := saveFixtureActor(t, ctx, a, def)
	historical := readExportFixture(t, a, ctx, map[string]any{"id": rec.ID, "revision": 1})
	if _, err := a.toolActorSave(ctx, map[string]any{"id": rec.ID, "name": rec.Name, "expected_revision": 1, "definition": testActorDefinition()}); err != nil {
		t.Fatal(err)
	}
	out, err := a.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 1})
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(out)
	if len(wire) > maxActorDiscoveryBytes || strings.Contains(string(wire), "huge unrelated definition") || strings.Contains(string(wire), `"definition":`) {
		t.Fatal("default discovery leaked executable payload")
	}
	m := out.(map[string]any)
	if m["retrieved_revision"] != 1 || m["latest_revision"] != 2 {
		t.Fatal("wrong historical summary")
	}
	all := []string{}
	after := ""
	for {
		r, err := a.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 1, "operation_after": after, "operation_limit": 37})
		if err != nil {
			t.Fatal(err)
		}
		x := r.(map[string]any)
		all = append(all, x["operations"].([]string)...)
		if x["operations_complete"] == true {
			break
		}
		after = x["next_operation_after"].(string)
	}
	if len(all) != 242 {
		t.Fatal("lost operation discovery", len(all))
	}
	r, err := a.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": 1, "operation": "gallery_post"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal([]byte(stringMustJSON(r)), &decoded)
	if !reflect.DeepEqual(decoded["operation"], op) || decoded["step_count"] != float64(45) {
		t.Fatal("historical operation incomplete")
	}
	if string(readExportFixture(t, a, ctx, map[string]any{"id": rec.ID, "revision": 1})) != string(historical) {
		t.Fatal("export substituted latest")
	}
	for _, rev := range []any{0, -1, 999, 1.5} {
		if _, err := a.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": rev}); err == nil {
			t.Fatal("invalid revision silently accepted", rev)
		}
	}
	if _, err := a.toolActorDefinitionExport(ctx.WithProject("other"), map[string]any{"id": rec.ID, "revision": 1}); err == nil {
		t.Fatal("export crossed project boundary")
	}
}
func TestActorListIsBoundedAndDoesNotLoseRows(t *testing.T) {
	ctx, a := newTestCtx(t, newFakePlatform())
	for i := 0; i < 18; i++ {
		def, _ := galleryGetFixture(t)
		raw, err := a.toolActorSave(ctx, map[string]any{"name": fmt.Sprintf("List actor %d", i), "definition": def})
		if err != nil {
			t.Fatal(err)
		}
		_ = raw
	}
	after := int64(0)
	count := 0
	for {
		out, err := a.toolActorList(ctx, map[string]any{"after": after, "limit": 3})
		if err != nil {
			t.Fatal(err)
		}
		m := out.(map[string]any)
		wire, _ := json.Marshal(m)
		if len(wire) > maxActorDiscoveryBytes || strings.Contains(string(wire), `"definition":`) {
			t.Fatal("list leaked definitions")
		}
		count += m["count"].(int)
		if m["has_more"] == false {
			break
		}
		next := m["next_cursor"].(int64)
		if next <= after {
			t.Fatal("list continuation stalled")
		}
		after = next
	}
	if count != 18 {
		t.Fatal("list lost rows", count)
	}
}
func TestOversizedOperationExportContainsEveryStep(t *testing.T) {
	ctx, a := newTestCtx(t, newFakePlatform())
	def, op := galleryGetFixture(t)
	op["large_metadata"] = strings.Repeat("x", 40000)
	rec := saveFixtureActor(t, ctx, a, def)
	rawDef, _ := json.Marshal(def)
	if _, err := ctx.AppDB().Exec(`UPDATE actors_versions SET definition_json=? WHERE actor_id=? AND revision=1`, string(rawDef), rec.ID); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"id": rec.ID, "revision": 1, "operation": "gallery_post"}
	r, err := a.toolActorGet(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	m := r.(map[string]any)
	if m["complete"] != false || m["operation"] != nil {
		t.Fatal("oversize returned executable fragment")
	}
	raw := readExportFixture(t, a, ctx, args)
	var full map[string]any
	json.Unmarshal(raw, &full)
	if full["complete"] != true || full["step_count"] != float64(45) || !reflect.DeepEqual(full["operation"], op) {
		t.Fatal("export omitted operation contract")
	}
}

func TestActorDiscoveryMCPAndEditorExport(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("bounded-discovery-fixture"))
	def, _ := galleryGetFixture(t)
	var saved map[string]any
	response := sc.POST("/actors", map[string]any{"name": "Large actor editor fixture", "definition": def}, &saved)
	if response.Status != 200 {
		t.Fatal(string(response.Body))
	}
	id := saved["actor"].(map[string]any)["id"]
	for _, name := range []string{"actors_get", "actors_list"} {
		var wire map[string]any
		response = sc.POST("/mcp", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"id": id, "revision": 1}}}, &wire)
		if response.Status != 200 || wire["error"] != nil {
			t.Fatal(string(response.Body))
		}
		result := wire["result"].(map[string]any)
		if result["isError"] == true {
			t.Fatal(result)
		}
		content := result["content"].([]any)[0].(map[string]any)["text"].(string)
		if len(content) > maxActorDiscoveryBytes || strings.Contains(content, `"definition":`) {
			t.Fatal("MCP discovery returned oversized/executable content")
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(content), &decoded); err != nil {
			t.Fatal("MCP discovery was truncated", err)
		}
		t.Logf("%s MCP text bytes=%d", name, len(content))
	}
	var page map[string]any
	response = sc.GET("/actors", &page)
	if response.Status != 200 || strings.Contains(string(response.Body), `"definition":`) {
		t.Fatal("editor discovery not migrated")
	}
	var exported map[string]any
	response = sc.GET(fmt.Sprintf("/actors/%v/definition/file?revision=1", id), &exported)
	if response.Status != 200 {
		t.Fatal(string(response.Body))
	}
	var canonical map[string]any
	json.Unmarshal([]byte(stringMustJSON(saved["actor"].(map[string]any)["definition"])), &canonical)
	if !reflect.DeepEqual(exported, canonical) {
		t.Fatal("editor export lost definition fields")
	}
	response = sc.GET(fmt.Sprintf("/actors/%v/definition/file?revision=999", id), nil)
	if response.Status == 200 {
		t.Fatal("editor missing revision silently returned latest")
	}
	response = sc.GET(fmt.Sprintf("/actors/%v/definition/file", id), nil)
	if response.Status == 200 {
		t.Fatal("editor export did not pin immutable revision")
	}
	var runs map[string]any
	sc.GET("/runs", &runs)
	if runs["total"] != float64(0) {
		t.Fatal("discovery/export executed actor")
	}
}
