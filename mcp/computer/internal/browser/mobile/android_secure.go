package mobile

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

func (c *Computer) supportsAndroidSecure() bool {
	return c.supportsNativeScreen() && c.lease.Device.Platform == "android" && slices.Contains(c.lease.Device.Capabilities, "source_only") && slices.Contains(c.lease.Device.Capabilities, "secure_text")
}
func (c *Computer) androidSecure(app string, required []string, target, password string, values map[string]string) error {
	fail := func() error { return errors.New("Android secure input unconfirmed; inspect privately before retrying") }
	if !c.supportsAndroidSecure() || password == "" || len(password) > 4096 || strings.ContainsAny(password, "\r\n\x00") || target == "" || len(values) == 0 {
		return fail()
	}
	// Secure helper deliberately supports only exact selectors, not paths.
	for _, marker := range required {
		if strings.Contains(marker, " > ") {
			return fail()
		}
	}
	for field := range values {
		if strings.Contains(field, " > ") {
			return fail()
		}
	}
	observe := func() (androidNode, error) {
		source, err := c.nativeSource()
		if err != nil {
			return androidNode{}, err
		}
		nodes, err := androidTree(source, app, c.display.Width, c.display.Height)
		if err != nil {
			return androidNode{}, err
		}
		n, err := matchAndroid(nodes, required, target, false, false, values)
		if err != nil || !n.password || n.kind != "android.widget.EditText" {
			return androidNode{}, fail()
		}
		return n, nil
	}
	n, err := observe()
	if err != nil {
		return fail()
	}
	if !n.focused {
		var out map[string]any
		if c.invoke(c.ctx(), "mobile_action", map[string]any{"session_id": c.lease.ID, "action": "tap", "x": float64(n.x+n.w/2) / float64(c.display.Width), "y": float64(n.y+n.h/2) / float64(c.display.Height)}, &out) != nil {
			return fail()
		}
	}
	focused, err := observe()
	if err != nil || !focused.focused || focused.id != n.id || focused.kind != n.kind {
		return fail()
	}
	var out map[string]any
	err = c.invoke(c.ctx(), "mobile_action", map[string]any{"session_id": c.lease.ID, "action": "secure_text", "password": password, "expected_package": app, "expected_resource_id": focused.id, "expected_bounds": fmt.Sprintf("[%d,%d][%d,%d]", focused.x, focused.y, focused.x+focused.w, focused.y+focused.h), "native_required": required, "native_values": values}, &out)
	if err != nil || out["secure_input"] != true || out["verified"] != true {
		return fail()
	}
	return nil
}
