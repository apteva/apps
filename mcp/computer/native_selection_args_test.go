package main

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
	backends "github.com/apteva/apps/mcp/computer/internal/browser"
	"github.com/apteva/apps/mcp/computer/internal/browser/mobile"
)

type nativeSelectionComp struct {
	*fakeComp
	action string
	target string
	opts   mobile.NativeOptions
	value  string
}

func (n *nativeSelectionComp) NativeGuardedWithOptions(action, _ string, _ []string, target, _ string, _ map[string]string, _ int, opts mobile.NativeOptions) (string, error) {
	n.action, n.target, n.opts = action, target, opts
	return n.value, nil
}

func TestComputerUseNativeSelectionArgs(t *testing.T) {
	comp := &nativeSelectionComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 100, Height: 200}}, value: "changed"}
	app := appWithSession("br_native", comp, "mobile")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	base := func(extra map[string]any) map[string]any {
		args := map[string]any{"session_id": "br_native", "native_app": "com.example", "native_required": []any{"Choose a Page"}}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}

	out, err := app.toolComputerUse(ctx, base(map[string]any{
		"action": "native_select", "native_target": "Beta >> android.widget.RadioButton", "native_state": true,
		"native_checked": map[string]any{"Alpha >> android.widget.RadioButton": true},
	}))
	if err != nil {
		t.Fatalf("native_select: %v", err)
	}
	if out.(map[string]any)["native_value"] != "changed" || comp.opts.State == nil || !*comp.opts.State || !comp.opts.Checked["Alpha >> android.widget.RadioButton"] {
		t.Fatalf("select args: %#v opts=%#v", out, comp.opts)
	}

	comp.value = "visible"
	out, err = app.toolComputerUse(ctx, base(map[string]any{
		"action": "native_scroll_to", "native_target": "Delta", "native_container": "pages", "direction": "up", "max_swipes": 5,
	}))
	if err != nil || out.(map[string]any)["target_visible"] != true || comp.opts.Container != "pages" || comp.opts.Direction != "up" || comp.opts.MaxSwipes != 5 {
		t.Fatalf("scroll_to args: %#v %v opts=%#v", out, err, comp.opts)
	}

	for name, extra := range map[string]map[string]any{
		"checked not bool":         {"action": "native_assert", "native_checked": map[string]any{"x": "yes"}},
		"checked not object":       {"action": "native_assert", "native_checked": []any{"x"}},
		"container outside scroll": {"action": "native_tap", "native_target": "x", "native_container": "pages"},
		"state not bool":           {"action": "native_select", "native_target": "x", "native_state": "true"},
	} {
		if _, err := app.toolComputerUse(ctx, base(extra)); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	many := map[string]any{}
	for i := 0; i < 13; i++ {
		many[strings.Repeat("x", i+1)] = true
	}
	if _, err := app.toolComputerUse(ctx, base(map[string]any{"action": "native_assert", "native_checked": many})); err == nil {
		t.Fatal("more than 12 checked guards must be rejected")
	}
}

type nativeListComp struct {
	*nativeSelectionComp
	opts   mobile.ListReadOptions
	values map[string]string
}

func (n *nativeListComp) NativeReadList(_ string, _ []string, values map[string]string, opts mobile.ListReadOptions) (mobile.ListRead, error) {
	n.opts, n.values = opts, values
	return mobile.ListRead{Rows: []mobile.ListRow{{Texts: []string{"Ana", "Hello"}}}, Stopped: mobile.ListStopEnd, Swipes: 2, Screens: 3}, nil
}

func TestComputerUseNativeReadListArgs(t *testing.T) {
	comp := &nativeListComp{nativeSelectionComp: &nativeSelectionComp{fakeComp: &fakeComp{display: backends.DisplaySize{Width: 100, Height: 200}}}}
	app := appWithSession("br_list", comp, "mobile")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	base := func(extra map[string]any) map[string]any {
		args := map[string]any{"session_id": "br_list", "native_app": "com.example", "native_required": []any{"Home"}, "native_values": map[string]any{"account": "me"}}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}
	out, err := app.toolComputerUse(ctx, base(map[string]any{
		"action": "native_read_list", "native_container": "feed", "native_rows": "clickable", "max_items": 7, "max_swipes": 0, "direction": "up",
	}))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	rows, _ := m["rows"].([]mobile.ListRow)
	if m["stopped"] != mobile.ListStopEnd || len(rows) != 1 || rows[0].Texts[1] != "Hello" || m["swipes"] != 2 {
		t.Fatalf("out = %#v", m)
	}
	if comp.opts.Container != "feed" || comp.opts.Rows != "clickable" || comp.opts.MaxItems != 7 || comp.opts.MaxSwipes != 0 || comp.opts.Direction != "up" || comp.values["account"] != "me" {
		t.Fatalf("opts = %#v values = %#v", comp.opts, comp.values)
	}
	for name, extra := range map[string]map[string]any{
		"target":          {"action": "native_read_list", "native_container": "feed", "native_target": "x"},
		"rows elsewhere":  {"action": "native_tap", "native_target": "x", "native_rows": "items"},
		"items elsewhere": {"action": "native_read", "native_target": "x", "max_items": 3},
	} {
		if _, err := app.toolComputerUse(ctx, base(extra)); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}
