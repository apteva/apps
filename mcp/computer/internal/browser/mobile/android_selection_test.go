package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/computer/internal/browser/api"
)

// pageRow renders one list row: a clickable container with a name and, when
// radio is "true"/"false", a RadioButton in that checked state.
func pageRow(y int, name, radio string) string {
	row := fmt.Sprintf(`<node package="com.example" class="android.widget.LinearLayout" clickable="true" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[0,%d][100,%d]">`, y, y+30)
	row += fmt.Sprintf(`<node package="com.example" class="android.widget.TextView" text="%s" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[5,%d][70,%d]"/>`, name, y+5, y+25)
	if radio != "" {
		row += fmt.Sprintf(`<node package="com.example" class="android.widget.RadioButton" enabled="true" checkable="true" checked="%s" selected="false" scrollable="false" bounds="[75,%d][95,%d]"/>`, radio, y+5, y+25)
	} else {
		row += fmt.Sprintf(`<node package="com.example" class="android.widget.ImageView" content-desc="Info" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[75,%d][95,%d]"/>`, y+5, y+25)
	}
	return row + `</node>`
}

// pageList is a "choose a Page" screen: header, then a scrollable list.
func pageList(rows ...string) string {
	return `<hierarchy rotation="0"><node package="com.example" class="android.widget.FrameLayout" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[0,0][100,200]">` +
		`<node package="com.example" resource-id="title" class="android.widget.TextView" text="Choose a Page" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[0,0][100,20]"/>` +
		`<node package="com.example" resource-id="pages" class="androidx.recyclerview.widget.RecyclerView" enabled="true" checkable="false" checked="false" selected="false" scrollable="true" bounds="[0,20][100,200]">` +
		strings.Join(rows, "") + `</node></node></hierarchy>`
}

func defaultPages(firstChecked, secondChecked string) string {
	return pageList(pageRow(20, "Alpha Studio", firstChecked), pageRow(55, "Beta Bakery", secondChecked), pageRow(90, "Gamma Garage", "false"), pageRow(125, "Connected by Dana", ""))
}

type fakeDevice struct {
	screens []string // successive UI sources; the last one repeats
	reads   int
	taps    []map[string]any
	swipes  []map[string]any
	onTap   func()
}

func (f *fakeDevice) computer(t *testing.T, caps ...string) *Computer {
	t.Helper()
	c, _ := New(api.SessionTarget{}, func(_ context.Context, tool string, args map[string]any, out any) error {
		switch tool {
		case "mobile_observe":
			screen := f.screens[min(f.reads, len(f.screens)-1)]
			f.reads++
			b, _ := json.Marshal(sdk.MCPResult{StructuredContent: map[string]any{"source": screen}})
			return json.Unmarshal(b, out)
		case "mobile_action":
			switch args["action"] {
			case "tap":
				f.taps = append(f.taps, args)
				if f.onTap != nil {
					f.onTap()
				}
			case "swipe":
				f.swipes = append(f.swipes, args)
			}
		}
		return nil
	})
	if len(caps) == 0 {
		caps = []string{"source", "source_only", "selection_state"}
	}
	c.lease = lease{ID: "s", Status: "active", ExpiresAt: time.Now().Add(time.Hour).Unix(), Device: device{Platform: "android", Driver: "adb", Capabilities: caps}}
	c.display = api.DisplaySize{Width: 100, Height: 200}
	c.ttl = 900
	return c
}

func TestRowScopedSelector(t *testing.T) {
	nodes, err := androidTree(defaultPages("true", "false"), "com.example", 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	n, err := matchAndroidChecked(nodes, []string{"Choose a Page"}, "Beta Bakery >> android.widget.RadioButton", false, false, nil, nil)
	if err != nil || n.kind != "android.widget.RadioButton" || n.y != 60 || n.checked {
		t.Fatalf("radio in the named row: %+v %v", n, err)
	}
	if _, err := matchAndroidChecked(nodes, nil, "Connected by Dana >> android.widget.RadioButton", false, false, nil, nil); !errors.Is(err, ErrNativeTargetAbsent) {
		t.Fatalf("row without a radio must be absent: %v", err)
	}
	if _, err := matchAndroidChecked(nodes, nil, "Missing Page >> android.widget.RadioButton", false, false, nil, nil); !errors.Is(err, ErrNativeTargetAbsent) {
		t.Fatalf("unknown row must be absent: %v", err)
	}
	dup, _ := androidTree(pageList(pageRow(20, "Beta Bakery", "true"), pageRow(55, "Beta Bakery", "false")), "com.example", 100, 200)
	if _, err := matchAndroidChecked(dup, nil, "Beta Bakery >> android.widget.RadioButton", false, false, nil, nil); err == nil || !strings.Contains(err.Error(), "one row") {
		t.Fatalf("two rows with the same name must fail: %v", err)
	}
	// A title outside any clickable row must not widen the scope to the screen.
	if _, err := matchAndroidChecked(nodes, nil, "Choose a Page >> android.widget.RadioButton", false, false, nil, nil); err == nil || !strings.Contains(err.Error(), "clickable row") {
		t.Fatalf("ROW without a clickable row must fail closed: %v", err)
	}
	// The row itself may be the checkable element (CheckedTextView rows).
	self := pageList(`<node package="com.example" class="android.widget.CheckedTextView" text="Solo" clickable="true" enabled="true" checkable="true" checked="true" selected="false" scrollable="false" bounds="[0,20][100,50]"/>`)
	selfNodes, _ := androidTree(self, "com.example", 100, 200)
	if n, err := matchAndroidChecked(selfNodes, nil, "Solo >> android.widget.CheckedTextView", false, false, nil, nil); err != nil || !n.checked {
		t.Fatalf("row-as-checkable: %+v %v", n, err)
	}
	// Existing " > " paths keep working.
	if _, err := matchAndroidChecked(nodes, nil, "pages > android.widget.LinearLayout", false, false, nil, nil); err == nil {
		t.Fatal("ambiguous direct-child path must still fail closed")
	}
}

func TestCheckedGuard(t *testing.T) {
	nodes, _ := androidTree(defaultPages("true", "false"), "com.example", 100, 200)
	ok := map[string]bool{"Alpha Studio >> android.widget.RadioButton": true, "Beta Bakery >> android.widget.RadioButton": false}
	if _, err := matchAndroidChecked(nodes, []string{"Choose a Page"}, "", false, false, nil, ok); err != nil {
		t.Fatalf("matching guard: %v", err)
	}
	// The default-selection trap: requiring Beta to be the selected Page.
	trap := map[string]bool{"Beta Bakery >> android.widget.RadioButton": true}
	if _, err := matchAndroidChecked(nodes, []string{"Choose a Page"}, "", false, false, nil, trap); err == nil {
		t.Fatal("guard must fail when another row is the default selection")
	}
	// Old Mobile: no selection attributes at all must never read as unchecked.
	old := strings.NewReplacer(` checkable="true"`, "", ` checkable="false"`, "", ` checked="true"`, "", ` checked="false"`, "").Replace(defaultPages("true", "false"))
	oldNodes, _ := androidTree(old, "com.example", 100, 200)
	if _, err := matchAndroidChecked(oldNodes, nil, "", false, false, nil, map[string]bool{"Beta Bakery >> android.widget.RadioButton": false}); err == nil || !strings.Contains(err.Error(), "lacks selection state") {
		t.Fatalf("missing state must fail closed: %v", err)
	}
}

func TestNativeSelect(t *testing.T) {
	target := "Beta Bakery >> android.widget.RadioButton"
	t.Run("already selected", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{defaultPages("false", "true")}}
		v, err := dev.computer(t).NativeGuardedWithOptions("native_select", "com.example", []string{"Choose a Page"}, target, "", nil, 0, NativeOptions{})
		if err != nil || v != "unchanged" || len(dev.taps) != 0 {
			t.Fatalf("v=%q err=%v taps=%d", v, err, len(dev.taps))
		}
	})
	t.Run("changed after a late update", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{defaultPages("true", "false")}}
		dev.onTap = func() {
			// First read after the tap still shows the old state.
			dev.screens = []string{defaultPages("true", "false"), defaultPages("false", "true")}
			dev.reads = 0
		}
		v, err := dev.computer(t).NativeGuardedWithOptions("native_select", "com.example", []string{"Choose a Page"}, target, "", nil, 0,
			NativeOptions{Checked: map[string]bool{"Alpha Studio >> android.widget.RadioButton": true}})
		if err != nil || v != "changed" || len(dev.taps) != 1 {
			t.Fatalf("v=%q err=%v taps=%d", v, err, len(dev.taps))
		}
		if x, y := dev.taps[0]["x"].(float64), dev.taps[0]["y"].(float64); x != 0.85 || y != 0.35 {
			t.Fatalf("tap must hit the radio centre, got %v,%v", x, y)
		}
	})
	t.Run("not confirmed", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{defaultPages("true", "false")}}
		_, err := dev.computer(t).NativeGuardedWithOptions("native_select", "com.example", []string{"Choose a Page"}, target, "", nil, 0, NativeOptions{})
		if err == nil || !strings.Contains(err.Error(), "not confirmed") || len(dev.taps) != 1 {
			t.Fatalf("err=%v taps=%d (exactly one tap, never replayed)", err, len(dev.taps))
		}
	})
	t.Run("guard blocks before the tap", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{defaultPages("true", "false")}}
		_, err := dev.computer(t).NativeGuardedWithOptions("native_select", "com.example", []string{"Choose a Page"}, target, "", nil, 0,
			NativeOptions{Checked: map[string]bool{"Gamma Garage >> android.widget.RadioButton": true}})
		if err == nil || len(dev.taps) != 0 {
			t.Fatalf("err=%v taps=%d", err, len(dev.taps))
		}
	})
	t.Run("not checkable", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{defaultPages("true", "false")}}
		_, err := dev.computer(t).NativeGuardedWithOptions("native_select", "com.example", []string{"Choose a Page"}, "Beta Bakery", "", nil, 0, NativeOptions{})
		if err == nil || !strings.Contains(err.Error(), "checkable") || len(dev.taps) != 0 {
			t.Fatalf("err=%v taps=%d", err, len(dev.taps))
		}
	})
	t.Run("old mobile", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{defaultPages("true", "false")}}
		_, err := dev.computer(t, "source", "source_only").NativeGuardedWithOptions("native_select", "com.example", []string{"Choose a Page"}, target, "", nil, 0, NativeOptions{})
		if err == nil || !strings.Contains(err.Error(), "Mobile 0.8.0") || dev.reads != 0 {
			t.Fatalf("err=%v reads=%d", err, dev.reads)
		}
	})
}

func TestNativeScrollTo(t *testing.T) {
	first := defaultPages("true", "false")
	second := pageList(pageRow(20, "Gamma Garage", "false"), pageRow(55, "Connected by Dana", ""), pageRow(90, "Delta Dental", "false"))
	target := "Delta Dental >> android.widget.RadioButton"
	call := func(dev *fakeDevice, opts NativeOptions) (string, error) {
		if opts.Container == "" {
			opts.Container = "pages"
		}
		return dev.computer(t).NativeGuardedWithOptions("native_scroll_to", "com.example", []string{"Choose a Page"}, target, "", nil, 0, opts)
	}
	t.Run("found after one swipe", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{first, second}}
		v, err := call(dev, NativeOptions{})
		if err != nil || v != "visible" || len(dev.swipes) != 1 || len(dev.taps) != 0 {
			t.Fatalf("v=%q err=%v swipes=%d taps=%d", v, err, len(dev.swipes), len(dev.taps))
		}
		s := dev.swipes[0]
		if s["x"] != 0.5 || s["to_x"] != 0.5 || s["y"].(float64) <= s["to_y"].(float64) {
			t.Fatalf("down swipe must move up inside the list: %v", s)
		}
	})
	t.Run("already visible", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{second}}
		if v, err := call(dev, NativeOptions{}); err != nil || v != "visible" || len(dev.swipes) != 0 {
			t.Fatalf("v=%q err=%v swipes=%d", v, err, len(dev.swipes))
		}
	})
	t.Run("end of list", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{first}}
		_, err := call(dev, NativeOptions{})
		if !errors.Is(err, ErrNativeTargetAbsent) || !strings.Contains(err.Error(), "end of the list") || len(dev.swipes) != 1 || len(dev.taps) != 0 {
			t.Fatalf("err=%v swipes=%d", err, len(dev.swipes))
		}
	})
	t.Run("max swipes", func(t *testing.T) {
		screens := []string{}
		for i := 0; i < 5; i++ {
			screens = append(screens, pageList(pageRow(20, fmt.Sprintf("Page %d", i), "false")))
		}
		dev := &fakeDevice{screens: screens}
		_, err := call(dev, NativeOptions{MaxSwipes: 2})
		if !errors.Is(err, ErrNativeTargetAbsent) || !strings.Contains(err.Error(), "after 2 swipes") || len(dev.swipes) != 2 {
			t.Fatalf("err=%v swipes=%d", err, len(dev.swipes))
		}
	})
	t.Run("bad container", func(t *testing.T) {
		dev := &fakeDevice{screens: []string{first}}
		if _, err := call(dev, NativeOptions{Container: "title"}); err == nil || !strings.Contains(err.Error(), "not scrollable") || len(dev.swipes) != 0 {
			t.Fatalf("err=%v", err)
		}
		if _, err := call(dev, NativeOptions{MaxSwipes: 21}); err == nil || len(dev.swipes) != 0 {
			t.Fatalf("max_swipes over 20 must be rejected: %v", err)
		}
	})
}

func TestSelectionCapabilityAndIOSRejection(t *testing.T) {
	dev := &fakeDevice{screens: []string{defaultPages("true", "false")}}
	withState := dev.computer(t).Capabilities().Actions
	without := dev.computer(t, "source", "source_only").Capabilities().Actions
	has := func(list []string, a string) bool {
		for _, v := range list {
			if v == a {
				return true
			}
		}
		return false
	}
	if !has(withState, "native_select") || !has(withState, "native_scroll_to") || has(without, "native_select") || has(without, "native_scroll_to") {
		t.Fatalf("selection actions must follow selection_state: with=%v without=%v", withState, without)
	}
	ios := dev.computer(t, "source", "elements")
	ios.lease.Device.Platform = "ios"
	ios.lease.Device.Driver = "xctest"
	if _, err := ios.NativeGuardedWithOptions("native_assert", "App", []string{"x"}, "", "", nil, 0, NativeOptions{Checked: map[string]bool{"x": true}}); err == nil || !strings.Contains(err.Error(), "Android-only") {
		t.Fatalf("iOS must reject selection guards: %v", err)
	}
}
