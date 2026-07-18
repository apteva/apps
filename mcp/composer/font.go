package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font/gofont/gomono"
)

const (
	composerFontFilename = "composer-go-mono.ttf"
	composerFontToken    = "__APTEVA_COMPOSER_FONT_FILE__"
)

// Composer always renders V1 text with a bundled font file. Font family names
// remain valid composition metadata, but never become a host Fontconfig
// dependency: local and remote executors use the same Go Mono bytes.
func writeComposerFont(dir string) (string, error) {
	path := filepath.Join(dir, composerFontFilename)
	if err := os.WriteFile(path, gomono.TTF, 0o600); err != nil {
		return "", fmt.Errorf("write bundled Composer font: %w", err)
	}
	return path, nil
}

func materializeComposerFontArgs(args []string, fontPath string) []string {
	if strings.TrimSpace(fontPath) == "" {
		return args
	}
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = strings.ReplaceAll(arg, composerFontToken, escDrawText(fontPath))
	}
	return out
}

func argsUseComposerFont(args []string) bool {
	for _, arg := range args {
		if strings.Contains(arg, composerFontToken) {
			return true
		}
	}
	return false
}

func (a *App) handleRenderFont(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(gomono.TTF)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Disposition", `inline; filename="`+composerFontFilename+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(gomono.TTF)
}
