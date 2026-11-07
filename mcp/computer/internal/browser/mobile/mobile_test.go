package mobile

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

type fakeMobile struct {
	calls                  []map[string]any
	tool                   []string
	width, height          int
	failObserve, failClose bool
	renews                 int
}

func (f *fakeMobile) call(ctx context.Context, tool string, args map[string]any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.calls = append(f.calls, args)
	f.tool = append(f.tool, tool)
	var result any
	switch tool {
	case "mobile_session":
		if args["action"] == "close" && f.failClose {
			return errors.New("offline")
		}
		if args["action"] == "renew" {
			f.renews++
		}
		result = map[string]any{"session": lease{ID: "ses_test", Status: "active", Device: device{ID: "iphone", Platform: "ios"}, ExpiresAt: time.Now().Add(15 * time.Minute).Unix()}}
	case "mobile_observe":
		if f.failObserve {
			return errors.New("capture failed")
		}
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, f.width, f.height))); err != nil {
			return err
		}
		result = sdk.MCPResult{Content: []sdk.MCPContent{{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(b.Bytes())}}}
	default:
		result = map[string]any{"ok": true}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
func openFake(t *testing.T) (*Computer, *fakeMobile) {
	t.Helper()
	f := &fakeMobile{width: 400, height: 800}
	c, err := New(api.SessionTarget{Kind: "mobile", DeviceID: "iphone"}, f.call)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.OpenSession(api.OpenOptions{}); err != nil {
		t.Fatal(err)
	}
	return c, f
}
func TestInputTranslation(t *testing.T) {
	c, f := openFake(t)
	for _, tc := range []struct {
		a    api.Action
		want map[string]any
	}{
		{api.Action{Type: "click", X: 100, Y: 600}, map[string]any{"action": "tap", "x": 0.25, "y": 0.75}},
		{api.Action{Type: "scroll", Direction: "down", Amount: 200}, map[string]any{"action": "swipe", "x": 0.5, "y": 0.5, "to_x": 0.5, "to_y": 0.25}},
		{api.Action{Type: "swipe", X: 100, Y: 600, ToX: 300, ToY: 200}, map[string]any{"action": "swipe", "to_x": 0.75, "to_y": 0.25}},
		{api.Action{Type: "type", Text: "hello"}, map[string]any{"action": "text", "text": "hello"}},
		{api.Action{Type: "home"}, map[string]any{"action": "key", "key": "home"}},
	} {
		if err := c.ExecuteAction(tc.a); err != nil {
			t.Fatal(err)
		}
		got := f.calls[len(f.calls)-1]
		for key, want := range tc.want {
			if got[key] != want {
				t.Errorf("%s: %s = %v, want %v", tc.a.Type, key, got[key], want)
			}
		}
	}
	if c.CurrentURL() != "" {
		t.Fatal("native navigation must not claim an observed URL")
	}
}
func TestInvalidInputDoesNotDispatch(t *testing.T) {
	c, f := openFake(t)
	for _, a := range []api.Action{
		{Type: "click", X: 400, Y: 1}, {Type: "click", X: -1}, {Type: "key", Key: "back"}, {Type: "reload"},
		{Type: "click", Selector: "button"}, {Type: "click", ExpectedText: "Delete"},
	} {
		before := len(f.calls)
		if err := c.ExecuteAction(a); err == nil {
			t.Fatalf("accepted %+v", a)
		}
		for _, tool := range f.tool[before:] {
			if tool == "mobile_action" {
				t.Fatalf("invalid action dispatched %+v", a)
			}
		}
	}
	f.width, f.height = 800, 400
	if err := c.ExecuteAction(api.Action{Type: "click", X: 100, Y: 100}); err == nil || !strings.Contains(err.Error(), "dimensions changed") {
		t.Fatalf("rotation: %v", err)
	}
}
func TestOpenFailureClosesLeaseAndCloseRetries(t *testing.T) {
	f := &fakeMobile{width: 400, height: 800, failObserve: true}
	c, _ := New(api.SessionTarget{Kind: "mobile"}, f.call)
	if err := c.OpenSession(api.OpenOptions{}); err == nil {
		t.Fatal("expected capture failure")
	}
	if !c.closed || f.calls[len(f.calls)-1]["action"] != "close" {
		t.Fatal("failed open leaked lease")
	}
	c, f = openFake(t)
	f.failClose = true
	if c.Close() == nil {
		t.Fatal("expected close failure")
	}
	f.failClose = false
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	n := len(f.calls)
	if err := c.Close(); err != nil || len(f.calls) != n {
		t.Fatal("close not idempotent")
	}
}
func TestLeaseRenewalExpiryAndCancellation(t *testing.T) {
	c, f := openFake(t)
	c.lease.ExpiresAt = time.Now().Add(time.Minute).Unix()
	if _, err := c.Screenshot(); err != nil {
		t.Fatal(err)
	}
	if f.renews != 1 {
		t.Fatal("lease was not renewed")
	}
	c.lease.ExpiresAt = time.Now().Add(-time.Second).Unix()
	n := len(f.calls)
	if err := c.ExecuteAction(api.Action{Type: "type", Text: "x"}); err == nil {
		t.Fatal("expired lease used")
	}
	if len(f.calls) != n {
		t.Fatal("expired lease must not dispatch or renew")
	}
	c.lease.ExpiresAt = time.Now().Add(time.Hour).Unix()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	defer c.BindRequest(ctx)()
	if _, err := c.Screenshot(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	// Cleanup gets an independent context even after the caller cancels.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}
func TestCapabilitiesDoNotPromiseBrowserIsolation(t *testing.T) {
	c, _ := openFake(t)
	caps := c.Capabilities()
	if caps.TargetKind != "mobile" || !caps.Context.Reuse || !caps.Context.Persistence || caps.Context.Isolation || caps.Context.SnapshotRestore {
		t.Fatalf("invalid guarantees: %+v", caps)
	}
}
