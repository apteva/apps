package mobile

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Limits for native_read_list. A read never taps; it returns the visible text
// of one named scrollable list, swiping through it when asked.
const (
	listDefaultItems   = 20
	listMaxItems       = 50
	listMaxSwipes      = 10
	listMaxRowStrings  = 40
	listMaxStringBytes = 4096
	listMaxBytes       = 64 << 10
	listTimeLimit      = 25 * time.Second
)

// Why a list read stopped.
const (
	ListStopEnd          = "end_of_list"
	ListStopMaxItems     = "max_items"
	ListStopMaxSwipes    = "max_swipes"
	ListStopTimeLimit    = "time_limit"
	ListStopSizeLimit    = "size_limit"
	ListStopScreenChange = "screen_changed"
)

// ListReadOptions configures native_read_list.
type ListReadOptions struct {
	// Container names the one visible scrollable list (resource id, text,
	// description or class).
	Container string
	// Rows is "items" (default), "clickable" or "text".
	Rows string
	// MaxItems bounds the rows returned (1-50, default 20). Ignored by "text".
	MaxItems int
	// MaxSwipes bounds paging (0-10, default 0: the current screen only).
	MaxSwipes int
	// Direction is "down" (default) or "up".
	Direction string
	// Checked are selection guards, rechecked on every screen.
	Checked map[string]bool
}

// ListRow is one entry's visible text and labels in screen order.
type ListRow struct {
	Texts []string `json:"texts"`
	// Partial marks a row cut off by the list's edge (shown only when no
	// later screen will show it whole) or taller than the list.
	Partial bool `json:"partial,omitempty"`
	// Truncated marks a row that had more than the per-row string limit.
	Truncated bool `json:"truncated,omitempty"`
	// cutTop and cutBottom mark a row touching the list's top or bottom edge.
	cutTop, cutBottom bool
}

// ListRead is the result of native_read_list.
type ListRead struct {
	Rows    []ListRow `json:"rows,omitempty"`
	Text    []string  `json:"text,omitempty"`
	Stopped string    `json:"stopped"`
	Swipes  int       `json:"swipes"`
	Screens int       `json:"screens"`
	// Detail explains a screen_changed stop.
	Detail string `json:"detail,omitempty"`
}

// NativeReadList reads the visible text of one named Android list. Screen
// markers, value guards and checked guards are checked on every screen; text
// entry and password fields are never read.
func (c *Computer) NativeReadList(app string, required []string, values map[string]string, opts ListReadOptions) (ListRead, error) {
	if c.lease.Device.Platform != "android" {
		return ListRead{}, errors.New("native_read_list is Android-only for now")
	}
	if !c.supportsSelectionState() {
		return ListRead{}, errors.New("native_read_list requires Mobile 0.8.0 or later (selection_state)")
	}
	if app == "" || len(app) > 255 || len(required) < 1 || len(required) > 12 || len(values) > 12 || len(opts.Checked) > 12 {
		return ListRead{}, errors.New("native guards require an app and 1-12 screen markers")
	}
	for _, marker := range required {
		if marker == "" || len(marker) > 500 {
			return ListRead{}, errors.New("invalid native screen marker")
		}
	}
	if strings.TrimSpace(opts.Container) == "" || len(opts.Container) > 500 {
		return ListRead{}, errors.New("native_read_list needs native_container")
	}
	mode := opts.Rows
	if mode == "" {
		mode = "items"
	}
	if mode != "items" && mode != "clickable" && mode != "text" {
		return ListRead{}, errors.New("native_rows must be items, clickable or text")
	}
	maxItems := opts.MaxItems
	if maxItems == 0 {
		maxItems = listDefaultItems
	}
	if maxItems < 1 || maxItems > listMaxItems {
		return ListRead{}, fmt.Errorf("max_items must be 1-%d", listMaxItems)
	}
	if opts.MaxSwipes < 0 || opts.MaxSwipes > listMaxSwipes {
		return ListRead{}, fmt.Errorf("native_read_list max_swipes must be 0-%d", listMaxSwipes)
	}
	direction := opts.Direction
	if direction == "" {
		direction = "down"
	}
	if direction != "down" && direction != "up" {
		return ListRead{}, errors.New("native_read_list direction must be down or up")
	}

	deadline := time.Now().Add(listTimeLimit)
	out := ListRead{}
	var prev []ListRow // the previous screen's rows, for dropping repeats
	size := 0
	last := ""
	for {
		screen, err := c.listScreen(app, required, values, opts.Checked, opts.Container, mode)
		if err != nil {
			if out.Screens == 0 {
				return ListRead{}, err
			}
			out.Stopped, out.Detail = ListStopScreenChange, err.Error()
			return out, nil
		}
		unchanged := out.Screens > 0 && screen.signature == last
		if unchanged {
			// A feed may still be loading after the swipe: look once more
			// before calling it the end. Never swipe again for this.
			if err := c.sleep(scrollSettle); err != nil {
				return ListRead{}, err
			}
			if again, err := c.listScreen(app, required, values, opts.Checked, opts.Container, mode); err == nil {
				screen = again
				unchanged = screen.signature == last
			} else {
				out.Stopped, out.Detail = ListStopScreenChange, err.Error()
				return out, nil
			}
		}
		final := unchanged || out.Swipes == opts.MaxSwipes || time.Now().After(deadline)
		out.Screens++
		last = screen.signature

		if mode == "text" {
			out.Text = stitch(out.Text, screen.text, direction)
			size = 0
			for _, s := range out.Text {
				size += len(s)
			}
			if size > listMaxBytes {
				out.Text = trimToBytes(out.Text, listMaxBytes, direction)
				out.Stopped = ListStopSizeLimit
				return out, nil
			}
		} else {
			fresh, kept := newRows(screen.rows, prev, final, out.Screens == 1, direction)
			prev = kept
			if direction == "up" {
				slices.Reverse(fresh)
			}
			for _, row := range fresh {
				if len(out.Rows) == maxItems {
					break
				}
				n := 0
				for _, s := range row.Texts {
					n += len(s)
				}
				if size+n > listMaxBytes {
					out.Stopped = ListStopSizeLimit
					finishRows(&out, direction)
					return out, nil
				}
				size += n
				out.Rows = append(out.Rows, row)
			}
			if len(out.Rows) == maxItems {
				out.Stopped = ListStopMaxItems
				finishRows(&out, direction)
				return out, nil
			}
		}
		switch {
		case unchanged:
			out.Stopped = ListStopEnd
		case out.Swipes == opts.MaxSwipes:
			out.Stopped = ListStopMaxSwipes
		case time.Now().After(deadline):
			out.Stopped = ListStopTimeLimit
		}
		if out.Stopped != "" {
			if mode != "text" {
				finishRows(&out, direction)
			}
			return out, nil
		}
		if err := c.swipeList(screen.box, direction); err != nil {
			return ListRead{}, err
		}
		out.Swipes++
	}
}

// finishRows puts rows read while scrolling up back into top-to-bottom order.
func finishRows(out *ListRead, direction string) {
	if direction == "up" {
		slices.Reverse(out.Rows)
	}
}

type listScreen struct {
	box       androidNode
	signature string
	rows      []ListRow
	text      []string
}

// listScreen reads one screen: it checks every guard, finds the container
// and groups what is visible inside it.
func (c *Computer) listScreen(app string, required []string, values map[string]string, checked map[string]bool, container, mode string) (listScreen, error) {
	source, err := c.nativeSource()
	if err != nil {
		return listScreen{}, err
	}
	nodes, err := androidTree(source, app, c.display.Width, c.display.Height)
	if err != nil {
		return listScreen{}, err
	}
	if _, err := matchAndroidChecked(nodes, required, "", false, false, values, checked); err != nil {
		return listScreen{}, err
	}
	box, signature, err := scrollContainer(nodes, container)
	if err != nil {
		return listScreen{}, err
	}
	index := -1
	for i, n := range nodes {
		if n.visible && (n.matches(container, false) || n.kind == container) {
			index = i
			break
		}
	}
	screen := listScreen{box: box, signature: signature}
	if mode == "text" {
		screen.text = subtreeTexts(nodes, index, false)
		return screen, nil
	}
	for _, entry := range listEntries(nodes, index, mode) {
		texts := subtreeTexts(nodes, entry, true)
		if len(texts) == 0 {
			continue
		}
		row := ListRow{Texts: texts}
		if len(row.Texts) > listMaxRowStrings {
			row.Texts, row.Truncated = row.Texts[:listMaxRowStrings], true
		}
		n := nodes[entry]
		top, bottom := n.y <= box.y, n.y+n.h >= box.y+box.h
		if top && bottom {
			// Taller than the list: no screen shows it whole, so keep it.
			row.Partial = true
		} else if top || bottom {
			// Android clips bounds, so a row touching the list's edge may be
			// cut off. It is reported, flagged, only when no later screen
			// will show it whole (see newRows).
			row.Partial, row.cutTop, row.cutBottom = true, top, bottom
		}
		screen.rows = append(screen.rows, row)
	}
	return screen, nil
}

// listEntries returns the node indexes that are rows of the container.
func listEntries(nodes []androidNode, container int, mode string) []int {
	children := func(parent int) []int {
		var out []int
		for i := container + 1; i < len(nodes); i++ {
			if nodes[i].parent == parent && nodes[i].visible {
				out = append(out, i)
			}
		}
		return out
	}
	if mode == "clickable" {
		var out []int
		for i := container + 1; i < len(nodes); i++ {
			n := nodes[i]
			if !n.visible || !n.clickable || !withinRow(nodes, i, container) {
				continue
			}
			outer := true
			for p := n.parent; p >= 0 && p != container; p = nodes[p].parent {
				if nodes[p].clickable {
					outer = false
					break
				}
			}
			if outer {
				out = append(out, i)
			}
		}
		return out
	}
	// items: skip single-child wrappers between the list and its entries.
	start := container
	for {
		kids := children(start)
		if len(kids) != 1 || len(children(kids[0])) == 0 {
			return kids
		}
		start = kids[0]
	}
}

// readable reports whether a node's text may be returned: never a password
// field and never a text-entry field, so drafts and typed text stay private.
func readable(n androidNode) bool {
	if n.password {
		return false
	}
	for _, suffix := range []string{"EditText", "AutoCompleteTextView", "SearchView$SearchAutoComplete"} {
		if strings.HasSuffix(n.kind, suffix) {
			return false
		}
	}
	return true
}

// subtreeTexts returns the visible text and labels under root in screen
// (document) order. dedupe drops a string already seen in this subtree,
// because the same string often appears as both text and label.
func subtreeTexts(nodes []androidNode, root int, dedupe bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if len(s) > listMaxStringBytes {
			s = truncateUTF8(s, listMaxStringBytes)
		}
		if dedupe && seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for i := root; i < len(nodes); i++ {
		// Nodes are in document order, so a subtree is one contiguous run.
		if i != root && !withinRow(nodes, i, root) {
			break
		}
		n := nodes[i]
		if !n.visible || !readable(n) {
			continue
		}
		add(n.value)
		if n.desc != n.value {
			add(n.desc)
		}
	}
	return out
}

// newRows drops rows that repeat the previous screen's rows (consecutive
// screens overlap) and edge-cut rows that a later screen will show whole. A
// cut row is kept, flagged, on the last screen, and on the first screen when
// it is cut at the edge the list moves away from (the top when scrolling
// down), since paging never shows it again. It also returns the rows to
// compare the next screen with: a cut row not reported is left out, so a
// later screen can still report it.
func newRows(rows, prev []ListRow, final, first bool, direction string) (fresh, kept []ListRow) {
	used := make([]bool, len(prev))
	for _, row := range rows {
		cut := row.cutTop || row.cutBottom
		repeat := false
		for j, p := range prev {
			if used[j] {
				continue
			}
			if sameTexts(row.Texts, p.Texts) || (cut && subsetOf(row.Texts, p.Texts)) {
				used[j], repeat = true, true
				break
			}
		}
		if repeat {
			kept = append(kept, row)
			continue
		}
		behind := (direction == "up" && row.cutBottom) || (direction != "up" && row.cutTop)
		if cut && !final && !(first && behind) {
			continue
		}
		fresh = append(fresh, row)
		kept = append(kept, row)
	}
	return fresh, kept
}

func sameTexts(a, b []string) bool { return slices.Equal(a, b) }

func subsetOf(a, b []string) bool {
	if len(a) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, s := range b {
		have[s] = true
	}
	for _, s := range a {
		if !have[s] {
			return false
		}
	}
	return true
}

// stitch joins the next screen's text onto what was read so far where the two
// overlap, so text shown on both screens appears once.
func stitch(acc, next []string, direction string) []string {
	if len(acc) == 0 {
		return append([]string(nil), next...)
	}
	if direction == "up" {
		// Scrolling up reveals text above: next's tail overlaps acc's head.
		for k := min(len(acc), len(next)); k > 0; k-- {
			if slices.Equal(next[len(next)-k:], acc[:k]) {
				return append(append([]string(nil), next[:len(next)-k]...), acc...)
			}
		}
		return append(append([]string(nil), next...), acc...)
	}
	for k := min(len(acc), len(next)); k > 0; k-- {
		if slices.Equal(acc[len(acc)-k:], next[:k]) {
			return append(acc, next[k:]...)
		}
	}
	return append(acc, next...)
}

// trimToBytes keeps text within limit, dropping from the end that was read
// last.
func trimToBytes(text []string, limit int, direction string) []string {
	total := 0
	if direction == "up" {
		for i := len(text) - 1; i >= 0; i-- {
			total += len(text[i])
			if total > limit {
				return text[i+1:]
			}
		}
		return text
	}
	for i, s := range text {
		total += len(s)
		if total > limit {
			return text[:i]
		}
	}
	return text
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && (s[n]&0xC0) == 0x80 {
		n--
	}
	return s[:n]
}

// swipeList moves the list 40% in direction, the same distance as
// native_scroll_to, so consecutive screens overlap.
func (c *Computer) swipeList(box androidNode, direction string) error {
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
		return err
	}
	return c.sleep(scrollSettle)
}
