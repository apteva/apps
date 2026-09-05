package main

import (
	"net/url"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestComputerAppBrowserControlStateMetadata(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	sc := tk.SpawnSidecar(t, ".")
	html := `<label>Native checkbox<input type=checkbox checked></label><button role=switch aria-label="Off switch" aria-checked=false>Off</button><button role=checkbox aria-label="Mixed checkbox" aria-checked=mixed>Mixed</button><label>Publish time<input type=time value="19:00"></label><label>Title<input value="Draft title"></label><label>Password<input type=password value="do-not-expose-password"></label><button>Plain button</button>`
	opened := sc.MCP("browser_session", map[string]any{"action": "open", "backend": "local", "url": "data:text/html," + url.PathEscape(html)})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sid})
	shot := sc.MCP("computer_use", map[string]any{"action": "screenshot", "session_id": sid, "include_som": true})
	targets := mapsFromAny(shot["som"])
	if !boolFromAny(findLiveTarget(t, targets, "Native checkbox", true)["checked"]) {
		t.Fatal("native checked state missing")
	}
	off := findLiveTarget(t, targets, "Off switch", true)
	if got, ok := off["checked"].(bool); !ok || got {
		t.Fatal("explicit unchecked state missing")
	}
	mixed := findLiveTarget(t, targets, "Mixed checkbox", true)
	if !boolFromAny(mixed["indeterminate"]) || mixed["checked"] != nil {
		t.Fatalf("mixed state collapsed: %v", mixed)
	}
	if findLiveTarget(t, targets, "Plain button", true)["checked"] != nil {
		t.Fatal("button misclassified as checkbox")
	}
	clock := findLiveTarget(t, targets, "Publish time", true)
	if stringValue(clock["current_value"]) != "19:00" || boolFromAny(clock["date_like"]) {
		t.Fatalf("time readback: %v", clock)
	}
	if stringValue(findLiveTarget(t, targets, "Title", true)["current_value"]) != "Draft title" {
		t.Fatal("title readback missing")
	}
	if strings.Contains(mustJSON(shot["som"]), "do-not-expose-password") {
		t.Fatal("password value exposed in semantics")
	}
	_, err := sc.MCPRaw("tools/call", map[string]any{"name": "computer_use", "arguments": map[string]any{"action": "set_checked", "session_id": sid, "target_id": off["id"], "som_revision": shot["som_revision"], "checked": true, "observation": "som_delta"}})
	// The fixture has no click handler: native button state must not be invented.
	if err == nil || !strings.Contains(err.Error(), "final state did not match") {
		t.Fatal("unhandled switch click was incorrectly verified")
	}
}

func TestComputerAppBrowserOccludedControls(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	sc := tk.SpawnSidecar(t, ".")
	html := `<style>body{margin:0}button{width:200px;height:40px}#covered{position:absolute;left:20px;top:20px}#block{position:absolute;left:0;top:0;width:240px;height:100px;background:#333;z-index:3}#behind{position:absolute;left:0;top:120px;width:240px;height:160px;overflow:auto}#pane{position:absolute;left:0;top:120px;width:240px;height:160px;background:#eee;z-index:3}#partial{position:absolute;left:300px;top:20px}#half{position:absolute;left:300px;top:20px;width:110px;height:40px;background:#333;z-index:3}#pass{position:absolute;left:300px;top:100px}#decoration{pointer-events:none;position:absolute;left:300px;top:100px;width:200px;height:40px;background:#ddd8;z-index:3}#shadow{position:absolute;left:300px;top:180px}iframe{position:absolute;left:600px;top:20px;width:260px;height:120px;border:0}</style><button id=covered>Covered button</button><div id=block></div><div id=behind aria-label="Hidden settings"><button>Hidden audience</button><div style="height:1000px"></div></div><div id=pane><button>Foreground button</button></div><button id=partial>Partly visible</button><div id=half></div><button id=pass>Pass through</button><div id=decoration></div><div style="position:absolute;left:600px;top:200px;width:240px;height:120px;overflow:auto" aria-label="Publishing settings"><div style="height:350px"></div><label>Set publish date<input type=checkbox></label></div><div id=shadow></div><iframe srcdoc="<button style='width:180px;height:50px'>Frame button</button>"></iframe><script>document.querySelector('#shadow').attachShadow({mode:'open'}).innerHTML='<button style="width:200px;height:40px">Shadow button</button>';</script>`
	opened := sc.MCP("browser_session", map[string]any{"action": "open", "backend": "local", "url": "data:text/html," + url.PathEscape(html)})
	sid := stringValue(opened["session_id"])
	if sid == "" {
		t.Fatalf("open: %v", opened)
	}
	defer sc.MCP("browser_session", map[string]any{"action": "close", "session_id": sid})
	shot := sc.MCP("computer_use", map[string]any{"action": "screenshot", "session_id": sid, "include_som": true})
	targets := mapsFromAny(shot["som"])
	for _, name := range []string{"Covered button", "Hidden audience"} {
		if findLiveTargetOptional(targets, name, true) != nil {
			t.Errorf("covered control was advertised: %s", name)
		}
	}
	for _, name := range []string{"Foreground button", "Partly visible", "Pass through", "Shadow button", "Frame button"} {
		if findLiveTargetOptional(targets, name, true) == nil {
			t.Errorf("reachable control was lost: %s", name)
		}
	}
	foundHints := false
	for _, region := range mapsFromAny(shot["scroll_regions"]) {
		if region["name"] == "Publishing settings" && strings.Contains(mustJSON(region["content_hints"]), "Set publish date") {
			foundHints = true
		}
		if region["name"] == "Hidden settings" {
			t.Fatal("covered scroll region advertised")
		}
	}
	if !foundHints {
		t.Fatal("below-viewport control name missing from panel hints")
	}
}
