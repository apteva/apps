package som

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/domsnapshot"
	"github.com/chromedp/cdproto/page"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

var axInteractiveRoles = map[string]int{
	"textbox": 5, "searchbox": 5, "combobox": 5, "spinbutton": 5,
	"button":   4,
	"link":     3,
	"checkbox": 2, "radio": 2, "switch": 2, "menuitem": 2,
	"menuitemcheckbox": 2, "menuitemradio": 2, "option": 2,
	"tab": 2, "treeitem": 2, "slider": 2,
}

// ShouldAugmentAX keeps hosted-browser screenshots fast while still covering
// the primary closed-DOM failure mode. Consent UI commonly leaves policy links
// visible to page JavaScript while encapsulating its action buttons.
func ShouldAugmentAX(elements []Element) bool {
	if len(elements) == 0 {
		return true
	}
	hasActionControl := false
	hasConsentSignal := false
	for _, element := range elements {
		tag := strings.ToLower(element.Tag)
		if tag == "iframe" {
			return true
		}
		role := strings.ToLower(element.Role)
		if tag == "button" || tag == "input" || tag == "textarea" || tag == "select" ||
			role == "button" || role == "checkbox" || role == "radio" || role == "switch" || role == "combobox" {
			hasActionControl = true
		}
		text := strings.ToLower(element.Text)
		for _, signal := range []string{"cookie", "consent", "customise", "customize", "reject", "decline"} {
			if strings.Contains(text, signal) {
				hasConsentSignal = true
				break
			}
		}
	}
	return hasConsentSignal && !hasActionControl
}

// EnumerateViaAX complements EnumScript with Chrome's accessibility tree.
// Unlike page JavaScript, the AX tree crosses closed shadow roots used by
// consent frameworks and other encapsulated controls.
func EnumerateViaAX(ctx context.Context, viewportWidth, viewportHeight int) []Element {
	var nodes []*accessibility.Node
	var documents []*domsnapshot.DocumentSnapshot
	var snapshotStrings []string
	var frameTree *page.FrameTree
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		if err := accessibility.Enable().Do(ctx); err != nil {
			return fmt.Errorf("accessibility.Enable: %w", err)
		}
		var err error
		frameTree, err = page.GetFrameTree().Do(ctx)
		if err != nil {
			return fmt.Errorf("Page.getFrameTree: %w", err)
		}
		nodes, err = accessibility.GetFullAXTree().WithFrameID(frameTree.Frame.ID).Do(ctx)
		if err != nil {
			return fmt.Errorf("GetFullAXTree: %w", err)
		}
		if len(nodes) == 0 {
			return fmt.Errorf("GetFullAXTree returned no nodes")
		}
		if documents, snapshotStrings, err = domsnapshot.CaptureSnapshot(nil).Do(ctx); err != nil {
			return fmt.Errorf("DOMSnapshot.captureSnapshot: %w", err)
		}
		return nil
	})); err != nil {
		if os.Getenv("APTEVA_AX_DEBUG") == "1" {
			fmt.Fprintf(os.Stderr, "[AX] enumerate failed: %v\n", err)
		}
		return nil
	}
	boxes := snapshotBoxes(documents)
	hasModal, modalMembers := snapshotModalMembers(documents, snapshotStrings, boxes, viewportWidth, viewportHeight)

	out := make([]Element, 0)
	for _, node := range nodes {
		if node == nil || node.Ignored || node.Role == nil || node.BackendDOMNodeID == 0 {
			continue
		}
		role := axStringValue(node.Role)
		if _, ok := axInteractiveRoles[role]; !ok {
			continue
		}
		if hasModal && !modalMembers[node.BackendDOMNodeID] {
			continue
		}
		name := ""
		if node.Name != nil {
			name = axStringValue(node.Name)
		}

		box, ok := boxes[node.BackendDOMNodeID]
		if !ok {
			continue
		}
		x, y, w, h := box.X, box.Y, box.W, box.H
		if w < 4 || h < 4 || x+w < 0 || y+h < 0 || x > viewportWidth || y > viewportHeight {
			continue
		}
		if len(name) > 40 {
			name = name[:40]
		}
		disabled := axPropertyBool(node, accessibility.PropertyNameDisabled)
		loading := axPropertyBool(node, accessibility.PropertyNameBusy)
		effect := destructiveEffectForText(name)
		out = append(out, Element{
			ID: fmt.Sprintf("ax_%d", node.BackendDOMNodeID),
			X:  x, Y: y, W: w, H: h,
			Tag: role, Role: role, Text: strings.TrimSpace(name), AccessibleName: strings.TrimSpace(name),
			Disabled: disabled, Loading: loading, Dangerous: effect != "", DestructiveEffect: effect,
		})
	}
	out = filterAXByOcclusion(ctx, out)
	return append(out, enumerateChildFrames(ctx, frameTree, viewportWidth, viewportHeight)...)
}

type frameRect struct {
	Src string  `json:"src"`
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
	W   float64 `json:"w"`
	H   float64 `json:"h"`
}

const frameRectsScript = `(function(){return Array.from(document.querySelectorAll('iframe')).map(function(el){var r=el.getBoundingClientRect();return {src:el.src||'',x:r.left,y:r.top,w:r.width,h:r.height};});})()`

// enumerateChildFrames evaluates the normal DOM enumerator in each frame's
// isolated world. CDP can enter cross-origin frames even when page JavaScript
// cannot; translated coordinates remain in the root viewport click space.
func enumerateChildFrames(ctx context.Context, tree *page.FrameTree, viewportWidth, viewportHeight int) []Element {
	if tree == nil || tree.Frame == nil || len(tree.ChildFrames) == 0 {
		return nil
	}
	var out []Element
	framesVisited := 0
	var walk func(*page.FrameTree, float64, float64)
	walk = func(parent *page.FrameTree, offsetX, offsetY float64) {
		if parent == nil || parent.Frame == nil || len(parent.ChildFrames) == 0 || framesVisited >= 10 {
			return
		}
		var rects []frameRect
		if err := evaluateInFrame(ctx, parent.Frame.ID, frameRectsScript, &rects); err != nil {
			return
		}
		used := make([]bool, len(rects))
		for _, child := range parent.ChildFrames {
			if framesVisited >= 10 || child == nil || child.Frame == nil {
				break
			}
			rectIndex := -1
			for i, rect := range rects {
				if !used[i] && rect.Src == child.Frame.URL {
					rectIndex = i
					break
				}
			}
			if rectIndex < 0 {
				continue
			}
			used[rectIndex] = true
			rect := rects[rectIndex]
			childOffsetX, childOffsetY := offsetX+rect.X, offsetY+rect.Y
			if rect.W >= 4 && rect.H >= 4 && childOffsetX+rect.W > 0 && childOffsetY+rect.H > 0 && childOffsetX < float64(viewportWidth) && childOffsetY < float64(viewportHeight) {
				framesVisited++
				var elements []Element
				if err := evaluateInFrame(ctx, child.Frame.ID, EnumScript, &elements); err == nil {
					for i := range elements {
						elements[i].X += int(childOffsetX)
						elements[i].Y += int(childOffsetY)
					}
					out = append(out, elements...)
				}
				walk(child, childOffsetX, childOffsetY)
			}
		}
	}
	walk(tree, 0, 0)
	return out
}

func evaluateInFrame(ctx context.Context, frameID cdp.FrameID, expression string, dst any) error {
	var raw []byte
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		worldID, err := page.CreateIsolatedWorld(frameID).WithWorldName("apteva-som").Do(ctx)
		if err != nil {
			return err
		}
		result, exception, err := cdpruntime.Evaluate(expression).WithContextID(worldID).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exception != nil {
			return fmt.Errorf("frame evaluation failed: %s", exception.Text)
		}
		if result == nil {
			return fmt.Errorf("frame evaluation returned no result")
		}
		raw = append(raw[:0], result.Value...)
		return nil
	}))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// snapshotModalMembers returns the backend IDs structurally contained by a
// visible explicit modal. This prevents AX from reintroducing background page
// controls that the JavaScript enumerator correctly suppressed.
func snapshotModalMembers(documents []*domsnapshot.DocumentSnapshot, stringsTable []string, boxes map[cdp.BackendNodeID]axBox, viewportWidth, viewportHeight int) (bool, map[cdp.BackendNodeID]bool) {
	members := make(map[cdp.BackendNodeID]bool)
	hasModal := false
	for _, document := range documents {
		if document == nil || document.Nodes == nil {
			continue
		}
		modalIndexes := make(map[int]bool)
		for index, backendID := range document.Nodes.BackendNodeID {
			attrs := snapshotAttributes(document.Nodes, index, stringsTable)
			name := strings.ToLower(snapshotString(document.Nodes.NodeName, index, stringsTable))
			box, laidOut := boxes[backendID]
			if laidOut && box.W >= 100 && box.H >= 80 && snapshotNodeQualifiesAsModal(attrs, name, box, viewportWidth, viewportHeight) {
				modalIndexes[index] = true
				hasModal = true
			}
		}
		if len(modalIndexes) == 0 {
			continue
		}
		for index, backendID := range document.Nodes.BackendNodeID {
			for current := index; current >= 0 && current < len(document.Nodes.ParentIndex); current = int(document.Nodes.ParentIndex[current]) {
				if modalIndexes[current] {
					members[backendID] = true
					break
				}
				parent := document.Nodes.ParentIndex[current]
				if parent < 0 || int(parent) == current {
					break
				}
			}
		}
	}
	return hasModal, members
}

func snapshotNodeQualifiesAsModal(attrs map[string]string, name string, box axBox, viewportWidth, viewportHeight int) bool {
	_, nativeOpen := attrs["open"]
	if attrs["aria-modal"] == "true" || (name == "dialog" && nativeOpen) {
		return true
	}
	if attrs["role"] != "dialog" {
		return false
	}
	centerX := box.X + box.W/2
	centerY := box.Y + box.H/2
	return centerX >= 0 && centerX <= viewportWidth && centerY >= 0 && centerY <= viewportHeight
}

func snapshotAttributes(nodes *domsnapshot.NodeTreeSnapshot, index int, stringsTable []string) map[string]string {
	out := make(map[string]string)
	if nodes == nil || index < 0 || index >= len(nodes.Attributes) {
		return out
	}
	attrs := nodes.Attributes[index]
	for i := 0; i+1 < len(attrs); i += 2 {
		nameIndex, valueIndex := int(attrs[i]), int(attrs[i+1])
		if nameIndex >= 0 && nameIndex < len(stringsTable) && valueIndex >= 0 && valueIndex < len(stringsTable) {
			out[strings.ToLower(stringsTable[nameIndex])] = strings.ToLower(stringsTable[valueIndex])
		}
	}
	return out
}

func snapshotString(indexes []domsnapshot.StringIndex, index int, stringsTable []string) string {
	if index < 0 || index >= len(indexes) {
		return ""
	}
	stringIndex := int(indexes[index])
	if stringIndex < 0 || stringIndex >= len(stringsTable) {
		return ""
	}
	return stringsTable[stringIndex]
}

type axBox struct{ X, Y, W, H int }

// snapshotBoxes maps every laid-out backend node to its viewport rectangle in
// one CDP response. This avoids one GetBoxModel round trip per AX node, which
// is prohibitively slow against hosted browsers.
func snapshotBoxes(documents []*domsnapshot.DocumentSnapshot) map[cdp.BackendNodeID]axBox {
	boxes := make(map[cdp.BackendNodeID]axBox)
	for _, document := range documents {
		if document == nil || document.Nodes == nil || document.Layout == nil {
			continue
		}
		for layoutIndex, nodeIndex := range document.Layout.NodeIndex {
			if nodeIndex < 0 || int(nodeIndex) >= len(document.Nodes.BackendNodeID) || layoutIndex >= len(document.Layout.Bounds) {
				continue
			}
			bounds := document.Layout.Bounds[layoutIndex]
			if len(bounds) < 4 {
				continue
			}
			boxes[document.Nodes.BackendNodeID[nodeIndex]] = axBox{
				X: int(bounds[0] - document.ScrollOffsetX),
				Y: int(bounds[1] - document.ScrollOffsetY),
				W: int(bounds[2]),
				H: int(bounds[3]),
			}
		}
	}
	return boxes
}

func filterAXByOcclusion(ctx context.Context, candidates []Element) []Element {
	if len(candidates) == 0 {
		return candidates
	}
	type rect struct{ X, Y, W, H int }
	rects := make([]rect, len(candidates))
	for i, candidate := range candidates {
		rects[i] = rect{candidate.X, candidate.Y, candidate.W, candidate.H}
	}
	payload, err := json.Marshal(rects)
	if err != nil {
		return candidates
	}
	script := fmt.Sprintf(`(function(){
  var rs = %s;
  function isUsefulInteractive(el) {
    if (!el) return false;
    var t = el.tagName;
    if (t === 'A' || t === 'BUTTON' || t === 'INPUT' || t === 'TEXTAREA' || t === 'SELECT') return true;
    if (el.getAttribute('role') || el.hasAttribute('onclick')) return true;
    var ti = el.getAttribute('tabindex');
    return ti !== null && ti !== '-1';
  }
  var keep = [];
  for (var i = 0; i < rs.length; i++) {
    var r = rs[i], cx = r.X + r.W / 2, cy = r.Y + r.H / 2;
    var top = document.elementFromPoint(cx, cy);
    if (!top || !isUsefulInteractive(top)) { keep.push(i); continue; }
    var tr = top.getBoundingClientRect();
    if (Math.abs(tr.left - r.X) < 6 && Math.abs(tr.top - r.Y) < 6) keep.push(i);
  }
  return keep;
})()`, string(payload))
	var keep []int
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &keep)); err != nil {
		return candidates
	}
	out := make([]Element, 0, len(keep))
	for _, index := range keep {
		if index >= 0 && index < len(candidates) {
			out = append(out, candidates[index])
		}
	}
	return out
}

func axStringValue(value *accessibility.Value) string {
	if value == nil || len(value.Value) == 0 {
		return ""
	}
	var result string
	if err := json.Unmarshal(value.Value, &result); err != nil {
		return ""
	}
	return result
}

func axPropertyBool(node *accessibility.Node, name accessibility.PropertyName) bool {
	if node == nil {
		return false
	}
	for _, property := range node.Properties {
		if property == nil || property.Name != name || property.Value == nil || len(property.Value.Value) == 0 {
			continue
		}
		var result bool
		if json.Unmarshal(property.Value.Value, &result) == nil {
			return result
		}
	}
	return false
}

func destructiveEffectForText(value string) string {
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	for _, item := range []struct {
		words  []string
		effect string
	}{
		{[]string{"publish"}, "immediate_publish"},
		{[]string{"delete", "destroy", "erase"}, "destructive_delete"},
		{[]string{"send", "post"}, "immediate_send"},
		{[]string{"pay", "payout", "purchase", "buy", "checkout", "place order"}, "financial_action"},
		{[]string{"withdraw", "withdrawal"}, "financial_action"},
		{[]string{"schedule", "set publish date"}, "schedule_publish"},
	} {
		for _, word := range item.words {
			if value == word || strings.HasPrefix(value, word+" ") || strings.HasSuffix(value, " "+word) || strings.Contains(value, " "+word+" ") {
				return item.effect
			}
		}
	}
	return ""
}

// MergeAX appends AX-only targets while preserving JS target ordering and
// removing duplicates whose centers differ only by CDP rounding.
func MergeAX(jsElements, axElements []Element) []Element {
	const dedupRadius = 12
	centers := make([][2]int, 0, len(jsElements)+len(axElements))
	for _, element := range jsElements {
		centers = append(centers, [2]int{element.X + element.W/2, element.Y + element.H/2})
	}
	merged := append([]Element(nil), jsElements...)
	for _, element := range axElements {
		cx, cy := element.X+element.W/2, element.Y+element.H/2
		duplicate := false
		for _, center := range centers {
			dx, dy := cx-center[0], cy-center[1]
			if dx < 0 {
				dx = -dx
			}
			if dy < 0 {
				dy = -dy
			}
			if dx <= dedupRadius && dy <= dedupRadius {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		merged = append(merged, element)
		centers = append(centers, [2]int{cx, cy})
	}
	for i := range merged {
		merged[i].Label = i + 1
	}
	return merged
}
