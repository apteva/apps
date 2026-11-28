package mobile

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/computer/internal/browser/api"
)

const attrs = `enabled="true" checkable="false" checked="false" selected="false" scrollable="false"`

// post renders one feed entry 40px tall at y: an author, a body, and a Like
// button whose label repeats its text. Like Android, bounds are clipped to the
// 200px screen and children past the edge are left out.
func post(y int, author, body string) string {
	clip := func(v int) int { return min(v, 200) }
	out := fmt.Sprintf(`<node package="com.example" class="android.view.View" %s bounds="[0,%d][100,%d]">`, attrs, y, clip(y+40))
	if y+12 <= 200 {
		out += fmt.Sprintf(`<node package="com.example" class="android.widget.TextView" text="%s" %s bounds="[5,%d][95,%d]"/>`, author, attrs, y+2, y+12)
	}
	if y+28 <= 200 {
		out += fmt.Sprintf(`<node package="com.example" class="android.widget.TextView" text="%s" content-desc="%s" %s bounds="[5,%d][95,%d]"/>`, body, body, attrs, y+14, y+28)
	}
	if y+38 <= 200 {
		out += fmt.Sprintf(`<node package="com.example" class="android.widget.Button" text="Like" content-desc="Like" clickable="true" %s bounds="[5,%d][40,%d]"/>`, attrs, y+30, y+38)
	}
	return out + `</node>`
}

// feed is a screen with an account header and a scrollable list [0,40]-[100,200]
// whose entries sit inside one single-child wrapper.
func feed(account string, entries ...string) string {
	return `<hierarchy rotation="0">` +
		fmt.Sprintf(`<node package="com.example" class="android.widget.FrameLayout" %s bounds="[0,0][100,200]">`, attrs) +
		fmt.Sprintf(`<node package="com.example" resource-id="account" class="android.widget.TextView" text="%s" %s bounds="[0,0][100,20]"/>`, account, attrs) +
		`<node package="com.example" resource-id="composer" class="android.widget.EditText" text="draft I am typing" focused="false" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[0,20][100,38]"/>` +
		`<node package="com.example" resource-id="feed" class="androidx.compose.ui.platform.ComposeView" enabled="true" checkable="false" checked="false" selected="false" scrollable="true" bounds="[0,40][100,200]">` +
		fmt.Sprintf(`<node package="com.example" class="android.view.View" %s bounds="[0,40][100,200]">`, attrs) +
		strings.Join(entries, "") +
		`</node></node></node></hierarchy>`
}

func listDevice(screens ...string) *fakeDevice {
	return &fakeDevice{screens: screens}
}

func readList(t *testing.T, f *fakeDevice, opts ListReadOptions) (ListRead, error) {
	t.Helper()
	c := f.computer(t)
	if opts.Container == "" {
		opts.Container = "feed"
	}
	return c.NativeReadList("com.example", []string{"account"}, map[string]string{"account": "me"}, opts)
}

func texts(rows []ListRow) [][]string {
	var out [][]string
	for _, r := range rows {
		out = append(out, r.Texts)
	}
	return out
}

func TestReadListItemsCurrentScreen(t *testing.T) {
	// Three whole posts and one cut off by the bottom edge.
	f := listDevice(feed("me", post(42, "Ana", "Hello"), post(84, "Ben", "Launch day"), post(126, "Cai", "New job"), post(168, "Dee", "Cut off")))
	res, err := readList(t, f, ListReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != ListStopMaxSwipes || res.Swipes != 0 || len(f.swipes) != 0 || len(f.taps) != 0 {
		t.Fatalf("stopped=%s swipes=%d device swipes=%d taps=%d", res.Stopped, res.Swipes, len(f.swipes), len(f.taps))
	}
	want := [][]string{{"Ana", "Hello", "Like"}, {"Ben", "Launch day", "Like"}, {"Cai", "New job", "Like"}}
	got := texts(res.Rows)
	if len(got) != 4 || !slices.EqualFunc(got[:3], want, slices.Equal) {
		t.Fatalf("rows = %v", got)
	}
	// The single-screen read is also the last one, so the cut row is kept
	// and flagged.
	if !res.Rows[3].Partial || res.Rows[0].Partial {
		t.Fatalf("partial flags = %+v", res.Rows)
	}
	for _, r := range res.Rows {
		for _, s := range r.Texts {
			if strings.Contains(s, "draft") {
				t.Fatal("text-entry field outside the list was read")
			}
		}
	}
}

func TestReadListPagesUntilEnd(t *testing.T) {
	s1 := feed("me", post(42, "Ana", "A"), post(84, "Ben", "B"), post(126, "Cai", "C"), post(168, "Dee", "D"))
	s2 := feed("me", post(42, "Cai", "C"), post(84, "Dee", "D"), post(126, "Eve", "E"), post(168, "Fay", "F"))
	f := listDevice(s1, s2) // s2 repeats: the list ended
	res, err := readList(t, f, ListReadOptions{MaxSwipes: 5})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range res.Rows {
		names = append(names, r.Texts[0])
	}
	if !slices.Equal(names, []string{"Ana", "Ben", "Cai", "Dee", "Eve", "Fay"}) {
		t.Fatalf("rows = %v", names)
	}
	if res.Stopped != ListStopEnd || res.Swipes != 2 || len(f.taps) != 0 {
		t.Fatalf("stopped=%s swipes=%d taps=%d", res.Stopped, res.Swipes, len(f.taps))
	}
	// Dee was cut off on the first screen and whole on the second; Fay is cut
	// off on the last screen, so only it is partial.
	for _, r := range res.Rows {
		if r.Partial != (r.Texts[0] == "Fay") {
			t.Fatalf("partial flags = %+v", res.Rows)
		}
	}
}

func TestReadListLimits(t *testing.T) {
	s1 := feed("me", post(42, "Ana", "A"), post(84, "Ben", "B"), post(126, "Cai", "C"))
	s2 := feed("me", post(42, "Dee", "D"), post(84, "Eve", "E"), post(126, "Fay", "F"))
	res, err := readList(t, listDevice(s1, s2), ListReadOptions{MaxSwipes: 3, MaxItems: 4})
	if err != nil || res.Stopped != ListStopMaxItems || len(res.Rows) != 4 {
		t.Fatalf("err=%v stopped=%s rows=%d", err, res.Stopped, len(res.Rows))
	}
	s3 := feed("me", post(42, "Gus", "G"), post(84, "Hal", "H"), post(126, "Ivy", "I"))
	res, err = readList(t, listDevice(s1, s2, s3), ListReadOptions{MaxSwipes: 1})
	if err != nil || res.Stopped != ListStopMaxSwipes || len(res.Rows) != 6 || res.Swipes != 1 {
		t.Fatalf("err=%v stopped=%s rows=%d swipes=%d", err, res.Stopped, len(res.Rows), res.Swipes)
	}
	for _, opts := range []ListReadOptions{{MaxSwipes: 11}, {MaxItems: 51}, {Rows: "all"}, {Direction: "left"}, {Container: "nope"}} {
		if _, err := readList(t, listDevice(s1), opts); err == nil {
			t.Fatalf("%+v: want an error", opts)
		}
	}
}

func TestReadListRepeatsKeptWithinOneScreen(t *testing.T) {
	// Two identical posts on one screen are two rows; across screens a
	// repeat of the previous screen is dropped.
	s1 := feed("me", post(42, "Promoted", "Ad"), post(84, "Promoted", "Ad"), post(126, "Ben", "B"))
	s2 := feed("me", post(42, "Ben", "B"), post(84, "Cai", "C"), post(126, "Dee", "D"))
	res, err := readList(t, listDevice(s1, s2), ListReadOptions{MaxSwipes: 1})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range res.Rows {
		names = append(names, r.Texts[0])
	}
	if !slices.Equal(names, []string{"Promoted", "Promoted", "Ben", "Cai", "Dee"}) {
		t.Fatalf("rows = %v", names)
	}
}

func TestReadListClickableRows(t *testing.T) {
	row := func(y int, name string) string {
		return fmt.Sprintf(`<node package="com.example" class="android.widget.LinearLayout" clickable="true" %s bounds="[0,%d][100,%d]">`, attrs, y, y+30) +
			fmt.Sprintf(`<node package="com.example" class="android.widget.TextView" text="%s" %s bounds="[5,%d][70,%d]"/>`, name, attrs, y+5, y+25) +
			fmt.Sprintf(`<node package="com.example" class="android.widget.Button" content-desc="Connect" clickable="true" %s bounds="[75,%d][95,%d]"/>`, attrs, y+5, y+25) +
			`</node>`
	}
	screen := feed("me", row(45, "Ana Lopez"), row(80, "Ben Ito"))
	res, err := readList(t, listDevice(screen), ListReadOptions{Rows: "clickable"})
	if err != nil {
		t.Fatal(err)
	}
	got := texts(res.Rows)
	want := [][]string{{"Ana Lopez", "Connect"}, {"Ben Ito", "Connect"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("rows = %v", got)
	}
}

func TestReadListTextStitchesScreens(t *testing.T) {
	line := func(y int, s string) string {
		return fmt.Sprintf(`<node package="com.example" class="android.widget.TextView" text="%s" %s bounds="[0,%d][100,%d]"/>`, s, attrs, y, y+20)
	}
	s1 := feed("me", line(45, "Ana Lopez"), line(70, "Engineer"), line(95, "About"), line(120, "Builds things"), line(145, "Experience"))
	s2 := feed("me", line(45, "Builds things"), line(70, "Experience"), line(95, "Acme, 2020"), line(120, "Education"),
		`<node package="com.example" class="android.widget.EditText" text="my typed note" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[0,150][100,170]"/>`,
		`<node package="com.example" class="android.widget.EditText" password="true" text="secret" enabled="true" checkable="false" checked="false" selected="false" scrollable="false" bounds="[0,175][100,195]"/>`)
	res, err := readList(t, listDevice(s1, s2), ListReadOptions{Rows: "text", MaxSwipes: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Ana Lopez", "Engineer", "About", "Builds things", "Experience", "Acme, 2020", "Education"}
	if !slices.Equal(res.Text, want) || res.Stopped != ListStopEnd {
		t.Fatalf("text=%v stopped=%s", res.Text, res.Stopped)
	}
}

func TestReadListGuardsEveryScreen(t *testing.T) {
	s1 := feed("me", post(42, "Ana", "A"), post(84, "Ben", "B"))
	other := feed("someone-else", post(42, "Cai", "C"), post(84, "Dee", "D"))
	res, err := readList(t, listDevice(s1, other), ListReadOptions{MaxSwipes: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped != ListStopScreenChange || len(res.Rows) != 2 || res.Rows[1].Texts[0] != "Ben" {
		t.Fatalf("stopped=%s rows=%v", res.Stopped, texts(res.Rows))
	}
	// A wrong account on the first screen reads nothing.
	if _, err := readList(t, listDevice(other), ListReadOptions{}); err == nil {
		t.Fatal("want an account guard error")
	}
}

func TestReadListAndroidOnly(t *testing.T) {
	c := (&fakeDevice{screens: []string{feed("me")}}).computer(t)
	c.lease.Device.Platform = "ios"
	if _, err := c.NativeReadList("com.example", []string{"account"}, nil, ListReadOptions{Container: "feed"}); err == nil || !strings.Contains(err.Error(), "Android-only") {
		t.Fatalf("err = %v", err)
	}
}

func TestAndroidInputLimits(t *testing.T) {
	resizable := []string{"replace_text", "replace_text_resizable"}
	old := []string{"replace_text"}
	long := strings.Repeat("a", 300)
	cases := []struct {
		action, text string
		caps         []string
		ok           bool
	}{
		{"native_type", "hello", nil, true},
		{"native_type", "Café", nil, false},
		{"native_type", strings.Repeat("a", 257), nil, false},
		{"native_replace", "Café ’ 💬", resizable, true},
		{"native_replace", "Café", old, false},
		{"native_replace", long, resizable, true},
		{"native_replace", long, old, false},
		{"native_replace", "plain ascii", old, true},
		{"native_replace", "tab\tok", resizable, true},
		{"native_replace", "two\nlines", resizable, false},
		{"native_replace", "bell\a", resizable, false},
		{"native_replace", strings.Repeat("a", 4096), resizable, true},
		{"native_replace", strings.Repeat("a", 4097), resizable, false},
		{"native_replace", "\xff", resizable, false},
		{"native_replace", "x", nil, false},
		{"native_clear", "", old, true},
	}
	for _, tc := range cases {
		if err := checkAndroidInput(tc.action, tc.text, tc.caps); (err == nil) != tc.ok {
			t.Errorf("%s %q caps=%v: err=%v", tc.action, tc.text, tc.caps, err)
		}
	}
}

// commentScreen is a comment box with no ID whose placeholder is its text
// until something is typed. After typing it may wrap and grow upward.
func commentScreen(text string, focused bool, top int, left int) string {
	return `<hierarchy rotation="0"><node package="com.example" class="android.widget.FrameLayout" enabled="true" bounds="[0,0][100,200]">` +
		`<node package="com.example" resource-id="account" class="android.widget.TextView" text="me" enabled="true" bounds="[0,0][100,20]"/>` +
		fmt.Sprintf(`<node package="com.example" class="android.widget.EditText" text="%s" focused="%t" clickable="true" enabled="true" bounds="[%d,%d][90,180]"/>`, text, focused, left, top) +
		`</node></hierarchy>`
}

func TestReplaceConfirmedAfterPlaceholderDisappears(t *testing.T) {
	for _, mode := range []string{"grows", "moved", "unchanged"} {
		t.Run(mode, func(t *testing.T) {
			typed := "Great post — congrats! 🎉"
			source := commentScreen("Add a comment...", false, 160, 10)
			var sent map[string]any
			c, _ := New(api.SessionTarget{}, func(_ context.Context, tool string, args map[string]any, out any) error {
				switch tool {
				case "mobile_observe":
					b, _ := json.Marshal(sdk.MCPResult{StructuredContent: map[string]any{"source": source}})
					return json.Unmarshal(b, out)
				case "mobile_action":
					switch args["action"] {
					case "tap":
						source = commentScreen("Add a comment...", true, 160, 10)
					case "text":
						sent = args
						switch mode {
						case "grows":
							source = commentScreen(typed, true, 130, 10)
						case "moved":
							source = commentScreen(typed, true, 130, 40)
						}
					}
				}
				return nil
			})
			c.lease = lease{ID: "s", Status: "active", ExpiresAt: time.Now().Add(time.Hour).Unix(), Device: device{Platform: "android", Driver: "adb", Capabilities: []string{"source", "replace_text", "replace_text_resizable"}}}
			c.display = api.DisplaySize{Width: 100, Height: 200}
			c.ttl = 900
			_, err := c.NativeGuarded("native_replace", "com.example", []string{"account", "Add a comment..."}, "Add a comment... >> android.widget.EditText", typed, map[string]string{"account": "me"}, 0)
			if (err == nil) != (mode == "grows") {
				t.Fatalf("err = %v", err)
			}
			if sent == nil || sent["text"] != typed || sent["replace"] != true || sent["expected_current"] != "Add a comment..." {
				t.Fatalf("text action = %v", sent)
			}
		})
	}
}

func TestReadListKeepsFirstScreenTopRow(t *testing.T) {
	// A header card flush with the list's top edge on the first screen is
	// never shown again once the list scrolls down, so it is kept, flagged.
	s1 := feed("me", post(40, "Account", "Card"), post(84, "Ben", "B"), post(126, "Cai", "C"))
	s2 := feed("me", post(42, "Cai", "C"), post(84, "Dee", "D"), post(126, "Eve", "E"))
	res, err := readList(t, listDevice(s1, s2), ListReadOptions{MaxSwipes: 3})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range res.Rows {
		names = append(names, r.Texts[0])
	}
	if !slices.Equal(names, []string{"Account", "Ben", "Cai", "Dee", "Eve"}) || !res.Rows[0].Partial || res.Rows[1].Partial {
		t.Fatalf("rows = %v %+v", names, res.Rows)
	}
}

func TestStitchToleratesLinesOnOneScreen(t *testing.T) {
	cases := []struct {
		name            string
		acc, next, want []string
		direction       string
	}{
		{"exact overlap", []string{"A", "B", "C", "D"}, []string{"C", "D", "E"}, []string{"A", "B", "C", "D", "E"}, "down"},
		// A card's dismiss control at its top edge scrolls out between screens.
		{"line only on the earlier screen",
			[]string{"Intro", "Try it", "Free month", "Dismiss card", "Analytics", "23 views", "3 impressions", "Show all"},
			[]string{"Try it", "Free month", "Analytics", "23 views", "3 impressions", "Show all", "About", "Bio"},
			[]string{"Intro", "Try it", "Free month", "Dismiss card", "Analytics", "23 views", "3 impressions", "Show all", "About", "Bio"}, "down"},
		{"line only on the later screen", []string{"A", "B", "C", "D", "E"}, []string{"C", "Late label", "D", "E", "F", "G"}, []string{"A", "B", "C", "D", "E", "F", "G"}, "down"},
		{"next already read", []string{"A", "B", "C", "D"}, []string{"B", "C", "D"}, []string{"A", "B", "C", "D"}, "down"},
		{"no overlap", []string{"A", "B"}, []string{"C", "D"}, []string{"A", "B", "C", "D"}, "down"},
		// Controls repeated in every entry must not pass for an overlap.
		{"repeated controls", []string{"Post 1", "Like", "Body 1", "Like"}, []string{"Post 2", "Like", "Body 2", "Like"}, []string{"Post 1", "Like", "Body 1", "Like", "Post 2", "Like", "Body 2", "Like"}, "down"},
		{"up with a line only on one screen", []string{"C", "D", "E", "F"}, []string{"A", "B", "C", "Late label", "D"}, []string{"A", "B", "C", "D", "E", "F"}, "up"},
		{"up exact", []string{"C", "D", "E"}, []string{"A", "B", "C", "D"}, []string{"A", "B", "C", "D", "E"}, "up"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acc := append([]string(nil), c.acc...)
			got := stitch(acc, c.next, c.direction)
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
			if !slices.Equal(acc, c.acc) {
				t.Fatal("stitch changed its input")
			}
		})
	}
}

func TestReadListTextSkipsControlThatScrolledOut(t *testing.T) {
	line := func(y int, s string) string {
		return fmt.Sprintf(`<node package="com.example" class="android.widget.TextView" text="%s" %s bounds="[0,%d][100,%d]"/>`, s, attrs, y, y+12)
	}
	s1 := feed("me", line(45, "Ana Lopez"), line(60, "Try it"), line(75, "Free month"), line(90, "Dismiss card"), line(105, "Analytics"), line(120, "23 views"), line(135, "3 impressions"), line(150, "Show all"))
	s2 := feed("me", line(45, "Try it"), line(60, "Free month"), line(75, "Analytics"), line(90, "23 views"), line(105, "3 impressions"), line(120, "Show all"), line(135, "About"), line(150, "Builds things"))
	res, err := readList(t, listDevice(s1, s2), ListReadOptions{Rows: "text", MaxSwipes: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Ana Lopez", "Try it", "Free month", "Dismiss card", "Analytics", "23 views", "3 impressions", "Show all", "About", "Builds things"}
	if !slices.Equal(res.Text, want) {
		t.Fatalf("text=%q", res.Text)
	}
}
