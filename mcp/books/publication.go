package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Contributor struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

type BookPrice struct {
	Marketplace string  `json:"marketplace"`
	Currency    string  `json:"currency"`
	Amount      float64 `json:"amount"`
}

type PrintSettings struct {
	TrimWidthIn    float64 `json:"trim_width_in"`
	TrimHeightIn   float64 `json:"trim_height_in"`
	InnerMarginIn  float64 `json:"inner_margin_in"`
	OuterMarginIn  float64 `json:"outer_margin_in"`
	TopMarginIn    float64 `json:"top_margin_in"`
	BottomMarginIn float64 `json:"bottom_margin_in"`
	FontSizePt     float64 `json:"font_size_pt"`
	LineHeightPt   float64 `json:"line_height_pt"`
	Bleed          bool    `json:"bleed"`
	Paper          string  `json:"paper"`
	Ink            string  `json:"ink"`
	Binding        string  `json:"binding"`
}

type PublicationMetadata struct {
	Publisher             string        `json:"publisher"`
	Imprint               string        `json:"imprint"`
	SeriesName            string        `json:"series_name"`
	SeriesNumber          string        `json:"series_number"`
	Edition               string        `json:"edition"`
	PublicationDate       string        `json:"publication_date"`
	EbookISBN             string        `json:"ebook_isbn"`
	PrintISBN             string        `json:"print_isbn"`
	Contributors          []Contributor `json:"contributors"`
	Categories            []string      `json:"categories"`
	Keywords              []string      `json:"keywords"`
	Audience              string        `json:"audience"`
	MinAge                int           `json:"min_age"`
	MaxAge                int           `json:"max_age"`
	RightsStatement       string        `json:"rights_statement"`
	Territories           []string      `json:"territories"`
	AIDisclosure          string        `json:"ai_disclosure"`
	ReadingDirection      string        `json:"reading_direction"`
	CopyrightYear         int           `json:"copyright_year"`
	AccessibilitySummary  string        `json:"accessibility_summary"`
	AccessibilityFeatures []string      `json:"accessibility_features"`
	AccessibilityHazards  []string      `json:"accessibility_hazards"`
	Prices                []BookPrice   `json:"prices"`
	Print                 PrintSettings `json:"print"`
}

func defaultPublication() PublicationMetadata {
	return PublicationMetadata{
		Territories:           []string{"WORLD"},
		ReadingDirection:      "ltr",
		AccessibilityFeatures: []string{"tableOfContents", "structuralNavigation", "readingOrder"},
		AccessibilityHazards:  []string{"none"},
		Print: PrintSettings{
			TrimWidthIn: 6, TrimHeightIn: 9,
			InnerMarginIn: 0.75, OuterMarginIn: 0.5,
			TopMarginIn: 0.65, BottomMarginIn: 0.65,
			FontSizePt: 11, LineHeightPt: 15,
			Paper: "white", Ink: "black", Binding: "paperback",
		},
	}
}

func normalizePublication(p PublicationMetadata) PublicationMetadata {
	d := defaultPublication()
	if p.Territories == nil {
		p.Territories = d.Territories
	}
	if p.ReadingDirection == "" {
		p.ReadingDirection = d.ReadingDirection
	}
	if p.CopyrightYear == 0 {
		p.CopyrightYear = time.Now().Year()
	}
	if p.Print.TrimWidthIn <= 0 {
		p.Print.TrimWidthIn = d.Print.TrimWidthIn
	}
	if p.Print.TrimHeightIn <= 0 {
		p.Print.TrimHeightIn = d.Print.TrimHeightIn
	}
	if p.Print.InnerMarginIn <= 0 {
		p.Print.InnerMarginIn = d.Print.InnerMarginIn
	}
	if p.Print.OuterMarginIn <= 0 {
		p.Print.OuterMarginIn = d.Print.OuterMarginIn
	}
	if p.Print.TopMarginIn <= 0 {
		p.Print.TopMarginIn = d.Print.TopMarginIn
	}
	if p.Print.BottomMarginIn <= 0 {
		p.Print.BottomMarginIn = d.Print.BottomMarginIn
	}
	if p.Print.FontSizePt <= 0 {
		p.Print.FontSizePt = d.Print.FontSizePt
	}
	if p.Print.LineHeightPt <= 0 {
		p.Print.LineHeightPt = d.Print.LineHeightPt
	}
	if p.Print.Paper == "" {
		p.Print.Paper = d.Print.Paper
	}
	if p.Print.Ink == "" {
		p.Print.Ink = d.Print.Ink
	}
	if p.Print.Binding == "" {
		p.Print.Binding = d.Print.Binding
	}
	if p.Contributors == nil {
		p.Contributors = []Contributor{}
	}
	if p.Categories == nil {
		p.Categories = []string{}
	}
	if p.Keywords == nil {
		p.Keywords = []string{}
	}
	if p.Prices == nil {
		p.Prices = []BookPrice{}
	}
	if p.AccessibilityFeatures == nil {
		p.AccessibilityFeatures = d.AccessibilityFeatures
	}
	if p.AccessibilityHazards == nil {
		p.AccessibilityHazards = d.AccessibilityHazards
	}
	return p
}

func decodePublication(raw string, p *PublicationMetadata) {
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), p)
	}
	*p = normalizePublication(*p)
}

func publicationFromValue(v any) (PublicationMetadata, error) {
	var p PublicationMetadata
	b, err := json.Marshal(v)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("invalid publication metadata: %w", err)
	}
	return p, validatePublication(p)
}

func validatePublication(p PublicationMetadata) error {
	if p.PublicationDate != "" {
		if _, err := time.Parse("2006-01-02", p.PublicationDate); err != nil {
			return errors.New("publication_date must be YYYY-MM-DD")
		}
	}
	if p.ReadingDirection != "" && p.ReadingDirection != "ltr" && p.ReadingDirection != "rtl" {
		return errors.New("reading_direction must be ltr or rtl")
	}
	if p.MinAge < 0 || p.MaxAge < 0 || (p.MaxAge > 0 && p.MinAge > p.MaxAge) {
		return errors.New("invalid audience age range")
	}
	for _, price := range p.Prices {
		if len(strings.TrimSpace(price.Currency)) != 3 || price.Amount < 0 {
			return errors.New("prices require a three-letter currency and non-negative amount")
		}
	}
	return nil
}

type ChecklistItem struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type PublicationChecklist struct {
	Platform string          `json:"platform"`
	Ready    bool            `json:"ready"`
	Items    []ChecklistItem `json:"items"`
}

func buildChecklist(book *Book, nodes []*BookNode, assets []BookAsset, platform string, validation ValidationReport) PublicationChecklist {
	if platform == "" {
		platform = "generic"
	}
	items := []ChecklistItem{}
	add := func(id, label string, ok bool, detail string) {
		status := "missing"
		if ok {
			status = "complete"
		}
		items = append(items, ChecklistItem{ID: id, Label: label, Status: status, Detail: detail})
	}
	add("manuscript", "Manuscript has content", book.ActualWordCount > 0 && len(nodes) > 0, strconv.Itoa(book.ActualWordCount)+" words")
	add("identity", "Title and author are set", strings.TrimSpace(book.Title) != "" && strings.TrimSpace(book.AuthorName) != "", "Store identity fields must match the cover")
	add("description", "Store description is set", strings.TrimSpace(book.Description) != "", "")
	add("categories", "At least one category is set", len(book.Publication.Categories) > 0, "")
	add("keywords", "Discovery keywords are set", len(book.Publication.Keywords) > 0, "")
	add("rights", "Rights and territories are set", strings.TrimSpace(book.Publication.RightsStatement) != "" && len(book.Publication.Territories) > 0, "")
	add("pricing", "At least one list price is set", len(book.Publication.Prices) > 0, "Prices are re-entered in the store portal")
	if platform != "print" {
		cover := firstAssetOfKind(assets, "cover")
		coverReady := cover != nil && cover.WidthPX >= 625 && cover.HeightPX >= 1000
		coverDetail := "JPEG or PNG, minimum 625 x 1000 px"
		if cover != nil {
			coverDetail = fmt.Sprintf("%d x %d px", cover.WidthPX, cover.HeightPX)
		}
		add("cover", "Ebook cover is attached and large enough", coverReady, coverDetail)
		imagesAccessible := true
		for _, asset := range assets {
			if asset.Kind == "interior_image" && strings.TrimSpace(asset.AltText) == "" {
				imagesAccessible = false
				break
			}
		}
		add("image_alt", "Interior images have alternative text", imagesAccessible, "")
		officialValidation := validation.Valid && validation.Validator == "W3C EPUBCheck"
		validationDetail := strings.Join(validation.Errors, "; ")
		if validationDetail == "" {
			validationDetail = validation.Validator
		}
		add("epub", "Official W3C EPUBCheck passes", officialValidation, validationDetail)
	}
	if platform == "kindle" || platform == "generic" {
		add("ai_disclosure", "AI-content disclosure is reviewed", book.Publication.AIDisclosure != "", "Choose none, assisted, or generated")
	}
	if platform == "print" || platform == "generic" {
		add("print_isbn", "Print ISBN is set", strings.TrimSpace(book.Publication.PrintISBN) != "", "KDP can assign one during title setup")
		add("print_cover", "Full-wrap print cover is attached", firstAssetOfKind(assets, "print_cover") != nil, "PDF sized after final pagination")
	}
	ready := true
	for _, item := range items {
		if item.Status != "complete" {
			ready = false
			break
		}
	}
	return PublicationChecklist{Platform: platform, Ready: ready, Items: items}
}

func checklistMarkdown(c PublicationChecklist) string {
	var b strings.Builder
	b.WriteString("# Publication checklist: " + strings.ReplaceAll(c.Platform, "_", " ") + "\n\n")
	if c.Ready {
		b.WriteString("Status: READY FOR FINAL STORE PREVIEW\n\n")
	} else {
		b.WriteString("Status: ACTION REQUIRED\n\n")
	}
	for _, item := range c.Items {
		mark := " "
		if item.Status == "complete" {
			mark = "x"
		}
		b.WriteString(fmt.Sprintf("- [%s] %s", mark, item.Label))
		if item.Detail != "" {
			b.WriteString(" - " + item.Detail)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nAlways inspect the EPUB in each store previewer and order a physical proof before release.\n")
	return b.String()
}

func metadataJSON(book *Book) []byte {
	data := map[string]any{
		"title": book.Title, "subtitle": book.Subtitle, "author": book.AuthorName,
		"description": book.Description, "language": book.Language,
		"publication": normalizePublication(book.Publication),
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	return append(b, '\n')
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
