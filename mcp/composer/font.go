package main

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
)

const (
	composerFontInterRegular  = "inter-regular"
	composerFontInterMedium   = "inter-medium"
	composerFontInterSemibold = "inter-semibold"
	composerFontInterBold     = "inter-bold"
	composerFontMonoRegular   = "go-mono-regular"
	composerFontMonoBold      = "go-mono-bold"
)

//go:embed fonts/Inter-Regular.ttf
var interRegularTTF []byte

//go:embed fonts/Inter-Medium.ttf
var interMediumTTF []byte

//go:embed fonts/Inter-SemiBold.ttf
var interSemiboldTTF []byte

//go:embed fonts/Inter-Bold.ttf
var interBoldTTF []byte

type composerFontFace struct {
	ID       string
	Family   string
	Weight   int
	Filename string
	Token    string
	Data     []byte
}

var composerFontFaces = []composerFontFace{
	{ID: composerFontInterRegular, Family: "Inter", Weight: 400, Filename: "composer-inter-regular.ttf", Token: "__APTEVA_COMPOSER_FONT_INTER_REGULAR__", Data: interRegularTTF},
	{ID: composerFontInterMedium, Family: "Inter", Weight: 500, Filename: "composer-inter-medium.ttf", Token: "__APTEVA_COMPOSER_FONT_INTER_MEDIUM__", Data: interMediumTTF},
	{ID: composerFontInterSemibold, Family: "Inter", Weight: 600, Filename: "composer-inter-semibold.ttf", Token: "__APTEVA_COMPOSER_FONT_INTER_SEMIBOLD__", Data: interSemiboldTTF},
	{ID: composerFontInterBold, Family: "Inter", Weight: 700, Filename: "composer-inter-bold.ttf", Token: "__APTEVA_COMPOSER_FONT_INTER_BOLD__", Data: interBoldTTF},
	{ID: composerFontMonoRegular, Family: "Go Mono", Weight: 400, Filename: "composer-go-mono-regular.ttf", Token: "__APTEVA_COMPOSER_FONT_GO_MONO_REGULAR__", Data: gomono.TTF},
	{ID: composerFontMonoBold, Family: "Go Mono", Weight: 700, Filename: "composer-go-mono-bold.ttf", Token: "__APTEVA_COMPOSER_FONT_GO_MONO_BOLD__", Data: gomonobold.TTF},
}

func composerFontFor(fontSpec *TextFont) composerFontFace {
	family := "Inter"
	weight := 400
	if fontSpec != nil {
		if strings.TrimSpace(fontSpec.Family) != "" {
			family = resolvedComposerFontFamily(fontSpec.Family)
		}
		if fontSpec.Weight > 0 {
			weight = fontSpec.Weight
		}
	}
	faceID := composerFontInterRegular
	if family == "Go Mono" {
		faceID = composerFontMonoRegular
		if weight >= 600 {
			faceID = composerFontMonoBold
		}
	} else {
		switch {
		case weight >= 650:
			faceID = composerFontInterBold
		case weight >= 550:
			faceID = composerFontInterSemibold
		case weight >= 450:
			faceID = composerFontInterMedium
		}
	}
	face, _ := composerFontFaceByID(faceID)
	return face
}

func resolvedComposerFontFamily(requested string) string {
	normalized := strings.ToLower(strings.TrimSpace(requested))
	normalized = strings.Trim(normalized, `"'`)
	for _, part := range strings.Split(normalized, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"'`)
		switch part {
		case "go mono", "monospace", "courier", "courier new":
			return "Go Mono"
		case "inter":
			return "Inter"
		}
	}
	return "Inter"
}

func composerFontSubstitutionWarning(requested string) string {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return ""
	}
	resolved := resolvedComposerFontFamily(requested)
	if strings.EqualFold(strings.Trim(requested, `"'`), resolved) {
		return ""
	}
	return fmt.Sprintf("%s unavailable; rendering with %s.", requested, resolved)
}

func v1TypographyWarnings(edit *Edit) []string {
	if edit == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, clip := range textOverlayClips(edit) {
		if clip.Asset.Font != nil {
			if warning := composerFontSubstitutionWarning(clip.Asset.Font.Family); warning != "" {
				seen[warning] = true
			}
		}
		if clip.Asset.Style != nil {
			if clip.Asset.Style.LetterSpacing != 0 {
				seen["V1 letter_spacing is not rendered; the value was ignored."] = true
			}
			if clip.Asset.Style.LineHeight != 0 {
				seen["V1 line_height is not rendered; the value was ignored."] = true
			}
		}
	}
	warnings := make([]string, 0, len(seen))
	for warning := range seen {
		warnings = append(warnings, warning)
	}
	sort.Strings(warnings)
	return warnings
}

func composerFontFaceByID(id string) (composerFontFace, bool) {
	for _, face := range composerFontFaces {
		if face.ID == id {
			return face, true
		}
	}
	return composerFontFace{}, false
}

func composerFontFacesInArgs(args []string) []composerFontFace {
	used := make([]composerFontFace, 0, len(composerFontFaces))
	for _, face := range composerFontFaces {
		for _, arg := range args {
			if strings.Contains(arg, face.Token) {
				used = append(used, face)
				break
			}
		}
	}
	return used
}

func writeComposerFonts(dir string, faces []composerFontFace) (map[string]string, error) {
	paths := make(map[string]string, len(faces))
	for _, face := range faces {
		path := filepath.Join(dir, face.Filename)
		if err := os.WriteFile(path, face.Data, 0o600); err != nil {
			return nil, fmt.Errorf("write bundled Composer font %s: %w", face.ID, err)
		}
		paths[face.ID] = path
	}
	return paths, nil
}

func materializeComposerFontArgs(args []string, paths map[string]string) []string {
	if len(paths) == 0 {
		return args
	}
	out := make([]string, len(args))
	copy(out, args)
	for _, face := range composerFontFaces {
		path := strings.TrimSpace(paths[face.ID])
		if path == "" {
			continue
		}
		for i := range out {
			out[i] = strings.ReplaceAll(out[i], face.Token, escDrawText(path))
		}
	}
	return out
}

func argsUseComposerFont(args []string) bool {
	return len(composerFontFacesInArgs(args)) > 0
}

func (a *App) handleRenderFont(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	faceID := strings.TrimSpace(r.URL.Query().Get("face"))
	if faceID == "" {
		faceID = composerFontInterRegular
	}
	face, ok := composerFontFaceByID(faceID)
	if !ok {
		http.Error(w, "unknown Composer font face", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Content-Length", strconv.Itoa(len(face.Data)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Disposition", `inline; filename="`+face.Filename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(face.Data)
}
