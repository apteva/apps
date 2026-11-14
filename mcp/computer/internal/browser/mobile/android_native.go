package mobile

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// NativeGuarded returns only a selected field, never the full private UI tree.
func (c *Computer) NativeGuarded(action, app string, required []string, target, text string, values map[string]string, duration int) (string, error) {
	if !c.supportsNativeScreen() {
		return "", errors.New("native guards require an Android ADB or native iOS session with UI source")
	}
	if app == "" || len(app) > 255 || len(required) < 1 || len(required) > 12 || len(values) > 12 || len(target) > 500 {
		return "", errors.New("native guards require an app and 1-12 screen markers")
	}
	for _, marker := range required {
		if marker == "" || len(marker) > 500 {
			return "", errors.New("invalid native screen marker")
		}
	}
	if action == "native_wait" {
		if duration < 1 || duration > 28000 || target != "" || text != "" {
			return "", errors.New("native_wait requires a bounded duration and screen markers only")
		}
		ctx, cancel := context.WithTimeout(c.ctx(), time.Duration(duration)*time.Millisecond)
		defer cancel()
		defer c.BindRequest(ctx)()
		for {
			_, err := c.NativeGuarded("native_assert", app, required, "", "", values, 0)
			if !errors.Is(err, ErrNativeScreenMarkerAbsent) {
				return "", err
			}
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", errors.New("native screen wait timed out; no input replayed")
			case <-timer.C:
			}
		}
	}
	if c.lease.Device.Platform == "android" {
		return c.androidNative(action, app, required, target, text, values)
	}
	for field, expected := range values {
		source, err := c.nativeSource()
		if err != nil {
			return "", err
		}
		node, err := matchNativeScreen(source, app, required, field, false)
		if err != nil || node.kind == "SecureTextField" || node.value != expected {
			return "", errors.New("native field value guard did not match")
		}
	}
	if action == "native_read" {
		if target == "" || text != "" {
			return "", errors.New("native_read requires one target and no input text")
		}
		source, err := c.nativeSource()
		if err != nil {
			return "", err
		}
		node, err := matchNativeScreen(source, app, required, target, false)
		if err != nil {
			return "", err
		}
		if node.kind == "SecureTextField" || len(node.value) > 4096 {
			return "", errors.New("native field is private or exceeds the read limit")
		}
		return node.value, nil
	}
	return "", c.NativeScreen(action, app, required, target, text)
}

type androidNode struct {
	id, kind, value, desc, pkg                     string
	x, y, w, h                                     int
	enabled, visible, password, focused, clickable bool
	parent                                         int
}

func (n androidNode) matches(target string, badged bool) bool {
	for _, v := range []string{n.id, n.value, n.desc} {
		if v == target || (badged && strings.HasPrefix(v, target+", ")) {
			return true
		}
	}
	return false
}

func androidTree(source, app string, width, height int) ([]androidNode, error) {
	d := xml.NewDecoder(strings.NewReader(source))
	nodes := []androidNode{}
	stack := []int{}
	depth, roots := 0, 0
	for {
		t, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid Android UI source")
		}
		switch tag := t.(type) {
		case xml.EndElement:
			if tag.Name.Local == "node" && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			depth--
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				if tag.Name.Local != "hierarchy" || roots != 1 {
					return nil, errors.New("unexpected Android UI root")
				}
				continue
			}
			if tag.Name.Local != "node" {
				return nil, errors.New("unexpected Android UI element")
			}
			a := map[string]string{}
			for _, v := range tag.Attr {
				a[v.Name.Local] = v.Value
			}
			if depth == 2 && a["package"] != app {
				return nil, errors.New("unexpected foreground Android app")
			}
			n := androidNode{id: a["resource-id"], kind: a["class"], value: a["text"], desc: a["content-desc"], pkg: a["package"], enabled: a["enabled"] == "true", visible: a["visible-to-user"] != "false", password: a["password"] == "true", focused: a["focused"] == "true", clickable: a["clickable"] == "true", parent: -1}
			x2, y2 := 0, 0
			if count, e := fmt.Sscanf(a["bounds"], "[%d,%d][%d,%d]", &n.x, &n.y, &x2, &y2); e != nil || count != 4 {
				n.visible = false
			}
			n.w, n.h = x2-n.x, y2-n.y
			n.visible = n.visible && n.pkg == app && n.x >= 0 && n.y >= 0 && n.w > 0 && n.h > 0 && x2 <= width && y2 <= height
			if len(stack) > 0 {
				n.parent = stack[len(stack)-1]
				n.visible = n.visible && nodes[n.parent].visible
				// Android's enabled flag belongs to each View. A disabled layout
				// container can contain enabled, actionable account-row buttons.
			}
			nodes = append(nodes, n)
			stack = append(stack, len(nodes)-1)
		}
	}
	if depth != 0 || roots != 1 || len(nodes) == 0 {
		return nil, errors.New("incomplete Android UI source")
	}
	return nodes, nil
}

func matchAndroid(nodes []androidNode, required []string, target string, badged, tap bool, values map[string]string) (androidNode, error) {
	// A direct-child path scopes repeated form controls to their owning field.
	// Every segment must match exactly; ambiguous paths still fail closed.
	matchesAt := func(index int, selector string, badge bool) bool {
		parts := strings.Split(selector, " > ")
		if len(parts) == 1 {
			return nodes[index].matches(selector, badge)
		}
		if len(parts) > 8 {
			return false
		}
		for p := len(parts) - 1; p >= 0; p-- {
			if index < 0 || parts[p] == "" || !(nodes[index].matches(parts[p], false) || nodes[index].kind == parts[p]) {
				return false
			}
			index = nodes[index].parent
		}
		return true
	}
	for _, marker := range required {
		found := false
		for i, n := range nodes {
			found = found || (n.visible && matchesAt(i, marker, false))
		}
		if !found {
			return androidNode{}, ErrNativeScreenMarkerAbsent
		}
	}
	find := func(selector string, forTap, badge bool) (androidNode, error) {
		matches := map[int]bool{}
		for i, n := range nodes {
			if !n.visible || !n.enabled || !matchesAt(i, selector, badge) {
				continue
			}
			if forTap {
				for !nodes[i].clickable && nodes[i].parent >= 0 {
					i = nodes[i].parent
				}
				if !nodes[i].clickable || !nodes[i].enabled {
					continue
				}
			}
			matches[i] = true
		}
		if forTap {
			// Compose can expose the same physical button twice, as clickable
			// parent and child. Collapse only nested targets with identical bounds;
			// separate or overlapping controls remain ambiguous.
			for child := range matches {
				for parent := nodes[child].parent; parent >= 0; parent = nodes[parent].parent {
					a, b := nodes[child], nodes[parent]
					if matches[parent] && a.x == b.x && a.y == b.y && a.w == b.w && a.h == b.h {
						delete(matches, parent)
					}
				}
			}
		}
		if len(matches) != 1 {
			return androidNode{}, errors.New("native target must match one visible enabled element")
		}
		for i := range matches {
			return nodes[i], nil
		}
		panic("unreachable")
	}
	for field, value := range values {
		n, err := find(field, false, false)
		if err != nil || n.password || n.value != value {
			return androidNode{}, errors.New("native field value guard did not match")
		}
	}
	if target == "" {
		return androidNode{}, nil
	}
	return find(target, tap, badged)
}

func (c *Computer) androidNative(action, app string, required []string, target, text string, values map[string]string) (string, error) {
	if !c.supportsNativeScreen() {
		return "", errors.New("Android UI source is unavailable")
	}
	clearing := action == "native_clear"
	typing := action == "native_type" || action == "native_replace" || clearing
	if clearing && text != "" {
		return "", errors.New("native_clear does not accept input text")
	}
	tap := action == "native_tap" || action == "native_tap_badged" || action == "native_tap_if_present"
	if action == "native_assert" {
		if target != "" || text != "" {
			return "", errors.New("native_assert accepts screen markers only")
		}
	} else if !tap && !typing && action != "native_read" {
		return "", errors.New("unsupported Android native action")
	}
	if action != "native_assert" && target == "" {
		return "", errors.New("native action requires one exact target")
	}
	if typing {
		if (!clearing && len(text) == 0) || len(text) > 256 {
			return "", errors.New("native input requires 1-256 bytes")
		}
		for _, r := range text {
			if r < 32 || r > 126 || r == '%' {
				return "", errors.New("ADB native input requires printable ASCII excluding percent")
			}
		}
		if (action == "native_replace" || clearing) && !slices.Contains(c.lease.Device.Capabilities, "replace_text") {
			return "", errors.New("Mobile does not support verified text replacement")
		}
	} else if text != "" {
		return "", errors.New("native read/tap does not accept input text")
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
		return matchAndroid(nodes, required, target, action == "native_tap_badged", tap, values)
	}
	node, err := observe()
	if err != nil {
		return "", err
	}
	if action == "native_assert" {
		return "", nil
	}
	if node.password {
		return "", errors.New("secure Android fields are unavailable through ADB native actions")
	}
	if action == "native_read" {
		if len(node.value) > 4096 {
			return "", errors.New("native field exceeds the read limit")
		}
		return node.value, nil
	}
	if typing && (!strings.HasSuffix(node.kind, "EditText") || (action == "native_type" && node.value != "")) {
		return "", errors.New("native input requires a regular editable field; use replacement for nonempty text")
	}
	var out any
	if err = c.invoke(c.ctx(), "mobile_action", map[string]any{"session_id": c.lease.ID, "action": "tap", "x": (float64(node.x) + float64(node.w)/2) / float64(c.display.Width), "y": (float64(node.y) + float64(node.h)/2) / float64(c.display.Height)}, &out); err != nil {
		return "", err
	}
	if !typing {
		return "", nil
	}
	focused, err := observe()
	if err != nil || !focused.focused || focused.id != node.id || focused.kind != node.kind || focused.value != node.value {
		return "", errors.New("native field changed after focus; no text sent")
	}
	args := map[string]any{"session_id": c.lease.ID, "action": "text", "text": text}
	if action == "native_replace" || clearing {
		args["replace"] = true
		args["expected_package"] = app
		args["expected_resource_id"] = node.id
		args["expected_current"] = focused.value
		args["expected_class"] = focused.kind
		args["expected_bounds"] = fmt.Sprintf("[%d,%d][%d,%d]", focused.x, focused.y, focused.x+focused.w, focused.y+focused.h)
	}
	if err = c.invoke(c.ctx(), "mobile_action", args, &out); err != nil {
		return "", err
	}
	changed, err := observe()
	if err != nil || changed.value != text {
		return "", errors.New("native field value was not confirmed after input")
	}
	return "", nil
}
