package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// drawingDocument is the runner's drawing-json: orthographic views as
// polylines in millimetres (y up), plus the model bounds.
type drawingDocument struct {
	Name   string        `json:"name"`
	Units  string        `json:"units"`
	Bounds Bounds        `json:"bounds"`
	Views  []drawingView `json:"views"`
}

type drawingView struct {
	Name    string         `json:"name"`
	Visible [][][2]float64 `json:"visible"`
	Hidden  [][][2]float64 `json:"hidden"`
}

type placedView struct {
	view          drawingView
	min, max      [2]float64
	offset        [2]float64 // sheet position (mm) of the view's min corner
	label         string
	width, height float64
}

type drawingSheet struct {
	doc        drawingDocument
	views      []placedView
	scale      float64 // sheet mm per model mm
	scaleLabel string
	widthMM    float64
	heightMM   float64
	title      string
}

func parseDrawing(body []byte) (*drawingDocument, error) {
	var doc drawingDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse drawing: %w", err)
	}
	if len(doc.Views) == 0 {
		return nil, errors.New("drawing has no views")
	}
	return &doc, nil
}

func viewExtent(view drawingView) ([2]float64, [2]float64, bool) {
	min := [2]float64{math.Inf(1), math.Inf(1)}
	max := [2]float64{math.Inf(-1), math.Inf(-1)}
	found := false
	for _, group := range [][][][2]float64{view.Visible, view.Hidden} {
		for _, line := range group {
			for _, point := range line {
				for k := 0; k < 2; k++ {
					min[k], max[k] = math.Min(min[k], point[k]), math.Max(max[k], point[k])
				}
				found = true
			}
		}
	}
	return min, max, found
}

// layoutDrawing places front (lower left), top (above front) and right (to
// the right of front) on an A4 or A3 landscape sheet at a standard scale.
func layoutDrawing(doc drawingDocument, title string) (*drawingSheet, error) {
	byName := map[string]drawingView{}
	for _, view := range doc.Views {
		byName[view.Name] = view
	}
	order := []string{"front", "top", "right"}
	placed := map[string]*placedView{}
	for _, name := range order {
		view, ok := byName[name]
		if !ok {
			continue
		}
		min, max, found := viewExtent(view)
		if !found {
			continue
		}
		placed[name] = &placedView{view: view, min: min, max: max, label: strings.ToUpper(name[:1]) + name[1:], width: max[0] - min[0], height: max[1] - min[1]}
	}
	front := placed["front"]
	if front == nil {
		return nil, errors.New("drawing has no front view geometry")
	}
	gap := 20.0 // sheet mm between views
	needW := func(scale float64) float64 {
		w := front.width * scale
		if right := placed["right"]; right != nil {
			w += gap + right.width*scale
		}
		return w
	}
	needH := func(scale float64) float64 {
		h := front.height * scale
		if top := placed["top"]; top != nil {
			h += gap + top.height*scale
		}
		return h
	}
	scales := []struct {
		value float64
		label string
	}{{10, "10:1"}, {5, "5:1"}, {2, "2:1"}, {1, "1:1"}, {0.5, "1:2"}, {0.2, "1:5"}, {0.1, "1:10"}, {0.05, "1:20"}, {0.02, "1:50"}, {0.01, "1:100"}}
	sheets := [][2]float64{{297, 210}, {420, 297}}
	margin, titleH := 15.0, 25.0
	for _, sheet := range sheets {
		for _, scale := range scales {
			if needW(scale.value) <= sheet[0]-2*margin && needH(scale.value) <= sheet[1]-2*margin-titleH-10 {
				out := &drawingSheet{doc: doc, scale: scale.value, scaleLabel: scale.label, widthMM: sheet[0], heightMM: sheet[1], title: title}
				originX, originY := margin, margin+titleH+10
				front.offset = [2]float64{originX, originY}
				out.views = append(out.views, *front)
				if top := placed["top"]; top != nil {
					top.offset = [2]float64{originX, originY + front.height*scale.value + gap}
					out.views = append(out.views, *top)
				}
				if right := placed["right"]; right != nil {
					right.offset = [2]float64{originX + front.width*scale.value + gap, originY}
					out.views = append(out.views, *right)
				}
				return out, nil
			}
		}
	}
	return nil, errors.New("model is too large for an A3 sheet at 1:100")
}

// sheetPoint maps a model-space view point to sheet millimetres.
func (s *drawingSheet) sheetPoint(view placedView, point [2]float64) [2]float64 {
	return [2]float64{view.offset[0] + (point[0]-view.min[0])*s.scale, view.offset[1] + (point[1]-view.min[1])*s.scale}
}

func (s *drawingSheet) notes() []string {
	size := s.doc.Bounds.Size
	return []string{
		s.title,
		fmt.Sprintf("Overall %s x %s x %s mm", trimFloat(size[0]), trimFloat(size[1]), trimFloat(size[2])),
		fmt.Sprintf("Scale %s   Units mm   Third-angle views: front, top, right", s.scaleLabel),
	}
}

func trimFloat(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

// drawingToDXF writes an R12 DXF with VISIBLE, HIDDEN and NOTES layers.
func drawingToDXF(body []byte, title string) ([]byte, error) {
	doc, err := parseDrawing(body)
	if err != nil {
		return nil, err
	}
	sheet, err := layoutDrawing(*doc, title)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	pair := func(code int, value any) { fmt.Fprintf(&out, "%d\n%v\n", code, value) }
	num := func(value float64) string { return trimFloat(value) }
	pair(0, "SECTION")
	pair(2, "HEADER")
	pair(9, "$ACADVER")
	pair(1, "AC1009")
	pair(9, "$INSUNITS")
	pair(70, 4)
	pair(0, "ENDSEC")
	pair(0, "SECTION")
	pair(2, "TABLES")
	pair(0, "TABLE")
	pair(2, "LTYPE")
	pair(70, 2)
	pair(0, "LTYPE")
	pair(2, "CONTINUOUS")
	pair(70, 0)
	pair(3, "Solid line")
	pair(72, 65)
	pair(73, 0)
	pair(40, "0.0")
	pair(0, "LTYPE")
	pair(2, "HIDDEN")
	pair(70, 0)
	pair(3, "Hidden __ __ __")
	pair(72, 65)
	pair(73, 2)
	pair(40, "4.5")
	pair(49, "3.0")
	pair(49, "-1.5")
	pair(0, "ENDTAB")
	pair(0, "TABLE")
	pair(2, "LAYER")
	pair(70, 3)
	for _, layer := range [][3]string{{"VISIBLE", "7", "CONTINUOUS"}, {"HIDDEN", "8", "HIDDEN"}, {"NOTES", "7", "CONTINUOUS"}} {
		pair(0, "LAYER")
		pair(2, layer[0])
		pair(70, 0)
		pair(62, layer[1])
		pair(6, layer[2])
	}
	pair(0, "ENDTAB")
	pair(0, "ENDSEC")
	pair(0, "SECTION")
	pair(2, "ENTITIES")
	line := func(layer string, a, b [2]float64) {
		pair(0, "LINE")
		pair(8, layer)
		pair(10, num(a[0]))
		pair(20, num(a[1]))
		pair(30, 0)
		pair(11, num(b[0]))
		pair(21, num(b[1]))
		pair(31, 0)
	}
	text := func(at [2]float64, height float64, value string) {
		pair(0, "TEXT")
		pair(8, "NOTES")
		pair(10, num(at[0]))
		pair(20, num(at[1]))
		pair(30, 0)
		pair(40, num(height))
		pair(1, strings.NewReplacer("\n", " ", "\r", " ").Replace(value))
	}
	for _, view := range sheet.views {
		for _, group := range []struct {
			layer string
			lines [][][2]float64
		}{{"HIDDEN", view.view.Hidden}, {"VISIBLE", view.view.Visible}} {
			for _, polyline := range group.lines {
				for i := 1; i < len(polyline); i++ {
					line(group.layer, sheet.sheetPoint(view, polyline[i-1]), sheet.sheetPoint(view, polyline[i]))
				}
			}
		}
		text([2]float64{view.offset[0], view.offset[1] - 6}, 3, view.label)
	}
	for index, note := range sheet.notes() {
		text([2]float64{15, 15 + float64(2-index)*7}, 4, note)
	}
	pair(0, "ENDSEC")
	pair(0, "EOF")
	return out.Bytes(), nil
}

// drawingToPDF writes a one-page vector PDF of the same sheet.
func drawingToPDF(body []byte, title string) ([]byte, error) {
	doc, err := parseDrawing(body)
	if err != nil {
		return nil, err
	}
	sheet, err := layoutDrawing(*doc, title)
	if err != nil {
		return nil, err
	}
	pt := func(mm float64) string { return trimFloat(mm * 72 / 25.4) }
	var content bytes.Buffer
	content.WriteString("1 J 1 j\n")
	stroke := func(lines [][][2]float64, view placedView, width float64, dashed bool) {
		if len(lines) == 0 {
			return
		}
		if dashed {
			content.WriteString("[4 2] 0 d 0.5 G\n")
		} else {
			content.WriteString("[] 0 d 0 G\n")
		}
		fmt.Fprintf(&content, "%s w\n", trimFloat(width))
		for _, polyline := range lines {
			for i, point := range polyline {
				p := sheet.sheetPoint(view, point)
				op := "l"
				if i == 0 {
					op = "m"
				}
				fmt.Fprintf(&content, "%s %s %s\n", pt(p[0]), pt(p[1]), op)
			}
			content.WriteString("S\n")
		}
	}
	text := func(at [2]float64, size float64, value string) {
		escaped := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`, "\n", " ", "\r", " ").Replace(asciiOnly(value))
		fmt.Fprintf(&content, "BT /F1 %s Tf %s %s Td (%s) Tj ET\n", trimFloat(size), pt(at[0]), pt(at[1]), escaped)
	}
	for _, view := range sheet.views {
		stroke(view.view.Hidden, view, 0.4, true)
		stroke(view.view.Visible, view, 0.8, false)
		content.WriteString("0 g\n")
		text([2]float64{view.offset[0], view.offset[1] - 6}, 9, view.label)
	}
	// frame and title block
	fmt.Fprintf(&content, "[] 0 d 0 G 1 w %s %s %s %s re S\n", pt(8), pt(8), pt(sheet.widthMM-16), pt(sheet.heightMM-16))
	fmt.Fprintf(&content, "%s %s %s %s re S\n", pt(8), pt(8), pt(sheet.widthMM-16), pt(30))
	for index, note := range sheet.notes() {
		size := 9.0
		if index == 0 {
			size = 13
		}
		text([2]float64{14, 30 - float64(index+1)*7}, size, note)
	}
	stream := content.Bytes()
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>", pt(sheet.widthMM), pt(sheet.heightMM)),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream)+1, stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		fmt.Sprintf("<< /Title (%s) /Producer (Apteva Design Studio) >>", strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(asciiOnly(title))),
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects))
	for index, object := range objects {
		offsets[index] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, len(objects), xref)
	return out.Bytes(), nil
}

func asciiOnly(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= 32 && r < 127 {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('?')
		}
	}
	return builder.String()
}
