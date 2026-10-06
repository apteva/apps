package main

import (
	"bytes"
	_ "embed"
	"net/http"
	"time"
)

// Keep the installed app icon available even when the runtime's UI directory
// is missing. The same canonical SVG is published for marketplace previews.
//
//go:embed ui/icon.svg
var financeIcon []byte

func serveFinanceIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "icon.svg", time.Time{}, bytes.NewReader(financeIcon))
}
