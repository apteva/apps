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
	"unicode/utf8"
)

// NativeOptions carries the Android selection guards and list-scroll
// settings. The zero value preserves the original NativeGuarded behaviour.
type NativeOptions struct {
	// Checked maps a selector to the checked state it must have before any
	// action. Supports row-scoped selectors ("Row text >> RadioButton").
	Checked map[string]bool
	// State is native_select's desired end state (default true).
	State *bool
	// Container names the one scrollable list native_scroll_to swipes inside.
	Container string
	// Direction is native_scroll_to's direction: "down" (default) or "up".
	Direction string
	// MaxSwipes bounds native_scroll_to (1-20, default 8).
	MaxSwipes int
}

func (o NativeOptions) selectionUsed(action string) bool {
	return len(o.Checked) > 0 || action == "native_select" || action == "native_scroll_to"
}

// ErrNativeTargetAbsent reports that no visible element matched the target.
// A visible but disabled target is a different error.
var ErrNativeTargetAbsent = errors.New("native target is not visible")

// NativeGuarded returns only a selected field, never the full private UI tree.
func (c *Computer) NativeGuarded(action, app string, required []string, target, text string, values map[string]string, duration int) (string, error) {
	return c.NativeGuardedWithOptions(action, app, required, target, text, values, duration, NativeOptions{})
}

// NativeGuardedWithOptions is NativeGuarded plus Android selection guards
// (native_checked), native_select and native_scroll_to.
func (c *Computer) NativeGuardedWithOptions(action, app string, required []string, target, text string, values map[string]string, duration int, opts NativeOptions) (string, error) {
	if !c.supportsNativeScreen() {
		return "", errors.New("native guards require an Android ADB or native iOS session with UI source")
	}
	if app == "" || len(app) > 255 || len(required) < 1 || len(required) > 12 || len(values) > 12 || len(opts.Checked) > 12 || len(target) > 500 || len(opts.Container) > 500 {
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
			_, err := c.NativeGuardedWithOptions("native_assert", app, required, "", "", values, 0, NativeOptions{Checked: opts.Checked})
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
		if opts.selectionUsed(action) && !c.supportsSelectionState() {
			return "", errors.New("Android selection guards require Mobile 0.8.0 or later (selection_state)")
		}
		if action == "native_type_secure" {
			if len(opts.Checked) > 0 {
				return "", errors.New("native_checked is not supported with secure input")
			}
			return "", c.androidSecure(app, required, target, text, values)
		}
		return c.androidNative(action, app, required, target, text, values, opts)
	}
	if opts.selectionUsed(action) {
		return "", errors.New("selection guards are Android-only for now")
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
	checkable, checked, selected, scrollable       bool
	// stateKnown is false when the UI source predates selection state
	// (Mobile < 0.8.0); a missing checked attribute must never read as false.
	stateKnown bool
	parent     int
}

func (n androidNode) matches(target string, badged bool) bool {
	if target == "android.widget.EditText[password=true]" {
		return n.password && n.kind == "android.widget.EditText"
	}
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
			_, n.stateKnown = a["checkable"]
			n.checkable, n.checked, n.selected, n.scrollable = a["checkable"] == "true", a["checked"] == "true", a["selected"] == "true", a["scrollable"] == "true"
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
	return matchAndroidChecked(nodes, required, target, badged, tap, values, nil)
}

// rowOf returns the nearest clickable ancestor-or-self, the element a tap
// on the node would activate, or -1 when there is none.
func rowOf(nodes []androidNode, i int) int {
	for ; i >= 0; i = nodes[i].parent {
		if nodes[i].clickable {
			return i
		}
	}
	return -1
}

// withinRow reports whether node i is the row itself or inside it.
func withinRow(nodes []androidNode, i, row int) bool {
	for ; i >= 0; i = nodes[i].parent {
		if i == row {
			return true
		}
	}
	return false
}

// matchAndroidChecked resolves markers, value guards, checked guards and the
// target. Selectors may be exact text/id/description, a " > " direct-child
// path, or "ROW >> PART": PART (text/id/description or class) inside the one
// clickable row that ROW belongs to.
func matchAndroidChecked(nodes []androidNode, required []string, target string, badged, tap bool, values map[string]string, checked map[string]bool) (androidNode, error) {
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
	// resolve returns the visible nodes a selector names.
	resolve := func(selector string, badge bool) ([]int, error) {
		left, right, scoped := strings.Cut(selector, " >> ")
		if !scoped {
			var out []int
			for i, n := range nodes {
				if n.visible && matchesAt(i, selector, badge) {
					out = append(out, i)
				}
			}
			return out, nil
		}
		left, right = strings.TrimSpace(left), strings.TrimSpace(right)
		if left == "" || right == "" || strings.Contains(right, " >> ") {
			return nil, errors.New("native row selector must be ROW >> PART")
		}
		rows := map[int]bool{}
		for i, n := range nodes {
			if n.visible && matchesAt(i, left, false) {
				row := rowOf(nodes, i)
				if row < 0 {
					// Without a clickable row the scope would be the whole
					// screen; fail closed instead of widening it.
					return nil, errors.New("native row selector: ROW is not inside a clickable row")
				}
				rows[row] = true
			}
		}
		if len(rows) == 0 {
			return nil, nil
		}
		if len(rows) > 1 {
			return nil, errors.New("native row selector must match one row")
		}
		var row int
		for r := range rows {
			row = r
		}
		var out []int
		for i, n := range nodes {
			if n.visible && withinRow(nodes, i, row) && (n.matches(right, badge) || n.kind == right) {
				out = append(out, i)
			}
		}
		return out, nil
	}
	for _, marker := range required {
		found, err := resolve(marker, false)
		if err != nil {
			return androidNode{}, err
		}
		if len(found) == 0 {
			return androidNode{}, ErrNativeScreenMarkerAbsent
		}
	}
	find := func(selector string, forTap, badge bool) (androidNode, error) {
		candidates, err := resolve(selector, badge)
		if err != nil {
			return androidNode{}, err
		}
		if len(candidates) == 0 {
			return androidNode{}, fmt.Errorf("native target must match one visible enabled element: %w", ErrNativeTargetAbsent)
		}
		matches := map[int]bool{}
		for _, i := range candidates {
			if !nodes[i].enabled {
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
	for selector, want := range checked {
		idx, err := resolve(selector, false)
		if err != nil || len(idx) != 1 {
			return androidNode{}, errors.New("native checked guard must match one visible element")
		}
		n := nodes[idx[0]]
		if !n.stateKnown {
			return androidNode{}, errors.New("Android UI source lacks selection state; update Mobile to 0.8.0 or later")
		}
		if !n.checkable || n.checked != want {
			return androidNode{}, errors.New("native checked guard did not match")
		}
	}
	if target == "" {
		return androidNode{}, nil
	}
	return find(target, tap, badged)
}

// supportsSelectionState reports Mobile >= 0.8.0 on ADB: the UI source
// carries checkable/checked/selected/scrollable.
func (c *Computer) supportsSelectionState() bool {
	return c.supportsNativeScreen() && c.lease.Device.Platform == "android" && slices.Contains(c.lease.Device.Capabilities, "selection_state")
}

const (
	selectionConfirmWindow = 2 * time.Second
	selectionPollInterval  = 200 * time.Millisecond
	scrollSettle           = 500 * time.Millisecond
)

func (c *Computer) androidNative(action, app string, required []string, target, text string, values map[string]string, opts NativeOptions) (string, error) {
	if !c.supportsNativeScreen() {
		return "", errors.New("Android UI source is unavailable")
	}
	selecting := action == "native_select"
	scrolling := action == "native_scroll_to"
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
	} else if !tap && !typing && !selecting && !scrolling && action != "native_read" {
		return "", errors.New("unsupported Android native action")
	}
	if action != "native_assert" && target == "" {
		return "", errors.New("native action requires one exact target")
	}
	if typing {
		if err := checkAndroidInput(action, text, c.lease.Device.Capabilities); err != nil {
			return "", err
		}
	} else if text != "" {
		return "", errors.New("native read/tap/select/scroll does not accept input text")
	}
	if opts.State != nil && !selecting {
		return "", errors.New("native_state is only valid for native_select")
	}
	if !scrolling && (opts.Container != "" || opts.Direction != "" || opts.MaxSwipes != 0) {
		return "", errors.New("native_container, direction and max_swipes are only valid for native_scroll_to")
	}
	observeWith := func(checked map[string]bool) (androidNode, error) {
		source, err := c.nativeSource()
		if err != nil {
			return androidNode{}, err
		}
		nodes, err := androidTree(source, app, c.display.Width, c.display.Height)
		if err != nil {
			return androidNode{}, err
		}
		return matchAndroidChecked(nodes, required, target, action == "native_tap_badged", tap, values, checked)
	}
	observe := func() (androidNode, error) { return observeWith(opts.Checked) }
	if scrolling {
		return c.androidScrollTo(app, required, target, values, opts)
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
	if selecting {
		want := true
		if opts.State != nil {
			want = *opts.State
		}
		if !node.stateKnown {
			return "", errors.New("Android UI source lacks selection state; update Mobile to 0.8.0 or later")
		}
		if !node.checkable || !node.enabled {
			return "", errors.New("native_select target is not an enabled checkable element; scope it with ROW >> class")
		}
		if node.checked == want {
			return "unchanged", nil
		}
		var out any
		if err = c.invoke(c.ctx(), "mobile_action", map[string]any{"session_id": c.lease.ID, "action": "tap", "x": (float64(node.x) + float64(node.w)/2) / float64(c.display.Width), "y": (float64(node.y) + float64(node.h)/2) / float64(c.display.Height)}, &out); err != nil {
			return "", err
		}
		// The state often lands a frame or two after the tap. Re-read within a
		// bounded window; never tap again.
		deadline := time.Now().Add(selectionConfirmWindow)
		for {
			// Checked guards are preconditions: selecting one radio unchecks
			// another, so re-check only markers and value guards here.
			after, err := observeWith(nil)
			if err == nil && after.checked == want {
				return "changed", nil
			}
			if time.Now().After(deadline) {
				return "", errors.New("native selection tap was sent but the new state was not confirmed; inspect before retrying, no input replayed")
			}
			if err := c.sleep(selectionPollInterval); err != nil {
				return "", err
			}
		}
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
	// Confirm the text landed in the field that was typed into. Its selector
	// may no longer match (a placeholder such as "Add a comment..." disappears
	// once text is in), so find the focused field instead and check it is the
	// same one. A guard on the edited field was a precondition; every other
	// marker and account guard is rechecked.
	rest := make(map[string]string, len(values))
	for field, value := range values {
		if field != target {
			rest[field] = value
		}
	}
	source, err := c.nativeSource()
	if err != nil {
		return "", errors.New("native field value was not confirmed after input")
	}
	nodes, err := androidTree(source, app, c.display.Width, c.display.Height)
	if err != nil {
		return "", errors.New("native field value was not confirmed after input")
	}
	// A marker naming the edited field itself (often its placeholder) is
	// replaced by the focused-field check below.
	row, _, scoped := strings.Cut(target, " >> ")
	markers := make([]string, 0, len(required))
	for _, marker := range required {
		if marker == target || (scoped && marker == strings.TrimSpace(row)) || (focused.value != "" && marker == focused.value) {
			continue
		}
		markers = append(markers, marker)
	}
	if _, err := matchAndroidChecked(nodes, markers, "", false, false, rest, nil); err != nil {
		return "", fmt.Errorf("native screen changed after input; inspect before retrying: %w", err)
	}
	if !sameFocusedField(nodes, focused, text) {
		return "", errors.New("native field value was not confirmed after input")
	}
	return "", nil
}

// maxReplaceBytes matches Mobile's replacement limit.
const maxReplaceBytes = 4096

// checkAndroidInput validates text for native_type, native_replace and
// native_clear. ADB's built-in text input types only printable ASCII without
// percent, so native_type keeps that 256-byte limit. Replacement sets the
// field through Mobile's verified path, which accepts any text up to 4096
// bytes except control characters (tab allowed); line breaks are rejected.
func checkAndroidInput(action, text string, caps []string) error {
	clearing := action == "native_clear"
	if action == "native_type" {
		if len(text) == 0 || len(text) > 256 {
			return errors.New("native_type requires 1-256 bytes; use native_replace for longer or non-ASCII text")
		}
		if !plainASCII(text) {
			return errors.New("native_type accepts printable ASCII excluding percent; use native_replace for other text")
		}
		return nil
	}
	if !slices.Contains(caps, "replace_text") {
		return errors.New("Mobile does not support verified text replacement")
	}
	if (!clearing && len(text) == 0) || len(text) > maxReplaceBytes {
		return fmt.Errorf("native_replace requires 1-%d bytes", maxReplaceBytes)
	}
	if !utf8.ValidString(text) {
		return errors.New("native_replace text must be valid UTF-8")
	}
	for _, r := range text {
		if (r < 32 && r != '\t') || r == 0x7f {
			return errors.New("native_replace text cannot contain line breaks or control characters")
		}
	}
	// Long or non-ASCII text may wrap and resize the field; older Mobile
	// rejected a resized field after a correct replacement.
	if (len(text) > 256 || !plainASCII(text)) && !slices.Contains(caps, "replace_text_resizable") {
		return errors.New("native_replace with Unicode or text over 256 bytes requires Mobile 0.11.0 or later (replace_text_resizable)")
	}
	return nil
}

func plainASCII(text string) bool {
	for _, r := range text {
		if r < 32 || r > 126 || r == '%' {
			return false
		}
	}
	return true
}

// sameFocusedField reports whether exactly one focused field holds text and
// is the field typed into: same class, same resource ID when it has one, and
// otherwise the same left and right edges with an overlapping vertical range
// (the field may wrap the new text and grow up or down).
func sameFocusedField(nodes []androidNode, was androidNode, text string) bool {
	match := 0
	for _, n := range nodes {
		if !n.focused {
			continue
		}
		match++
		if match > 1 || n.password || !n.visible || n.kind != was.kind || n.value != text || !strings.HasSuffix(n.kind, "EditText") {
			return false
		}
		if was.id != "" {
			if n.id != was.id {
				return false
			}
			continue
		}
		abs := func(v int) int {
			if v < 0 {
				return -v
			}
			return v
		}
		if n.id != "" || abs(n.x-was.x) > 2 || abs((n.x+n.w)-(was.x+was.w)) > 2 {
			return false
		}
		if !(n.y < was.y+was.h && was.y < n.y+n.h) {
			return false
		}
	}
	return match == 1
}

// androidScrollTo swipes inside one scrollable container until the target is
// visible. It never taps. It stops at the end of the list (no visible change
// after a swipe) or after MaxSwipes, returning ErrNativeTargetAbsent.
func (c *Computer) androidScrollTo(app string, required []string, target string, values map[string]string, opts NativeOptions) (string, error) {
	maxSwipes := opts.MaxSwipes
	if maxSwipes == 0 {
		maxSwipes = 8
	}
	if maxSwipes < 1 || maxSwipes > 20 || strings.TrimSpace(opts.Container) == "" {
		return "", errors.New("native_scroll_to needs native_container and 1-20 swipes")
	}
	direction := opts.Direction
	if direction == "" {
		direction = "down"
	}
	if direction != "down" && direction != "up" {
		return "", errors.New("native_scroll_to direction must be down or up")
	}
	last := ""
	for swipes := 0; ; swipes++ {
		source, err := c.nativeSource()
		if err != nil {
			return "", err
		}
		nodes, err := androidTree(source, app, c.display.Width, c.display.Height)
		if err != nil {
			return "", err
		}
		_, err = matchAndroidChecked(nodes, required, target, false, false, values, opts.Checked)
		if err == nil {
			return "visible", nil
		}
		if !errors.Is(err, ErrNativeTargetAbsent) {
			return "", err
		}
		box, signature, err := scrollContainer(nodes, opts.Container)
		if err != nil {
			return "", err
		}
		if signature == last {
			return "", fmt.Errorf("native target not found before the end of the list: %w", ErrNativeTargetAbsent)
		}
		if swipes == maxSwipes {
			return "", fmt.Errorf("native target not found after %d swipes: %w", maxSwipes, ErrNativeTargetAbsent)
		}
		last = signature
		// A 40% swipe keeps consecutive views overlapping, so no row can pass
		// between two reads.
		from, to := box.y+box.h*7/10, box.y+box.h*3/10
		if direction == "up" {
			from, to = to, from
		}
		cx := (float64(box.x) + float64(box.w)/2) / float64(c.display.Width)
		var out any
		if err := c.invoke(c.ctx(), "mobile_action", map[string]any{
			"session_id": c.lease.ID, "action": "swipe",
			"x": cx, "y": float64(from) / float64(c.display.Height),
			"to_x": cx, "to_y": float64(to) / float64(c.display.Height),
		}, &out); err != nil {
			return "", err
		}
		if err := c.sleep(scrollSettle); err != nil {
			return "", err
		}
	}
}

// scrollContainer resolves the one visible scrollable element named by
// selector and a signature of what is currently visible inside it.
func scrollContainer(nodes []androidNode, selector string) (androidNode, string, error) {
	match := -1
	for i, n := range nodes {
		if !n.visible || !(n.matches(selector, false) || n.kind == selector) {
			continue
		}
		if match >= 0 {
			return androidNode{}, "", errors.New("native_container must match one visible element")
		}
		match = i
	}
	if match < 0 {
		return androidNode{}, "", errors.New("native_container is not visible")
	}
	box := nodes[match]
	if !box.stateKnown {
		return androidNode{}, "", errors.New("Android UI source lacks selection state; update Mobile to 0.8.0 or later")
	}
	if !box.scrollable {
		return androidNode{}, "", errors.New("native_container is not scrollable")
	}
	var parts []string
	for i, n := range nodes {
		if i != match && n.visible && withinRow(nodes, i, match) {
			parts = append(parts, fmt.Sprintf("%s|%s|%s|%d", n.id, n.value, n.desc, n.y-box.y))
		}
	}
	return box, strings.Join(parts, "\n"), nil
}

// sleep waits without outliving the request context.
func (c *Computer) sleep(d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.ctx().Done():
		return c.ctx().Err()
	case <-timer.C:
		return nil
	}
}
