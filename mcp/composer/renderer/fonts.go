package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/xml"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Inter (SIL OFL 1.1) and Go Mono (BSD, Bigelow & Holmes) with their licenses.
//
//go:embed fonts/*.ttf fonts/*.txt
var bundledFonts embed.FS

const bundledSans = "Inter"
const bundledMono = "Go Mono"

// Chrome on Linux finds fonts through fontconfig; a minimal host has none and
// captures blank text. macOS and Windows keep the system font stack.
var bundledFontsEnabled = runtime.GOOS == "linux"

var sansAliases = []string{"sans-serif", "system-ui", "ui-sans-serif", "-apple-system", "BlinkMacSystemFont", "Helvetica", "Helvetica Neue", "Arial", "Segoe UI", "Roboto",
	"serif", "ui-serif", "Times", "Times New Roman", "Georgia"}
var monoAliases = []string{"monospace", "ui-monospace", "Menlo", "Monaco", "Consolas", "Courier", "Courier New", "SF Mono"}

// prepareFonts writes the bundled fonts and a renderer-owned fontconfig file
// into a content-addressed directory, returning the config path. Host font
// directories remain a fallback for glyphs the bundled set lacks.
func prepareFonts(root string) (string, error) {
	entries, err := fs.ReadDir(bundledFonts, "fonts")
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	for _, e := range entries {
		raw, err := bundledFonts.ReadFile("fonts/" + e.Name())
		if err != nil {
			return "", err
		}
		hash.Write([]byte(e.Name()))
		hash.Write(raw)
	}
	parent := filepath.Join(root, "dependencies")
	dir := filepath.Join(parent, "fonts-"+hex.EncodeToString(hash.Sum(nil))[:12])
	conf := filepath.Join(dir, "fonts.conf")
	if info, err := os.Stat(conf); err == nil && info.Mode().IsRegular() {
		return conf, nil
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	for _, e := range entries {
		raw, _ := bundledFonts.ReadFile("fonts/" + e.Name())
		if err := os.WriteFile(filepath.Join(staging, e.Name()), raw, 0600); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(staging, "fonts.conf"), []byte(fontsConfig(dir)), 0600); err != nil {
		return "", err
	}
	// A directory without fonts.conf is an interrupted earlier attempt.
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		return "", err
	}
	return conf, nil
}

func fontsConfig(dir string) string {
	var b strings.Builder
	text := func(s string) string { var out strings.Builder; _ = xml.EscapeText(&out, []byte(s)); return out.String() }
	b.WriteString("<?xml version=\"1.0\"?>\n<!DOCTYPE fontconfig SYSTEM \"urn:fontconfig:fonts.dtd\">\n<fontconfig>\n")
	for _, d := range []string{dir, "/usr/share/fonts", "/usr/local/share/fonts"} {
		b.WriteString("  <dir>" + text(d) + "</dir>\n")
	}
	b.WriteString("  <cachedir>" + text(filepath.Join(dir, "cache")) + "</cachedir>\n")
	alias := func(names []string, family string) {
		for _, name := range names {
			b.WriteString("  <alias binding=\"strong\"><family>" + text(name) + "</family><prefer><family>" + family + "</family></prefer></alias>\n")
		}
	}
	alias(sansAliases, bundledSans)
	alias(monoAliases, bundledMono)
	// Families no font provides resolve to the bundled sans, never to an
	// arbitrary face.
	b.WriteString("  <match target=\"pattern\"><edit name=\"family\" mode=\"append_last\"><string>" + bundledSans + "</string></edit></match>\n")
	b.WriteString("</fontconfig>\n")
	return b.String()
}
