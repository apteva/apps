package main

import (
	"embed"
	"net/http"
	"strings"
)

// Static assets for the anonymous signing page, embedded so they ship
// inside the sidecar binary and serve under the no_auth /sign/ prefix
// (/sign/assets/<name>). The same vendor files double as the dashboard
// panel's pdf.js, served from disk by the platform under /ui/vendor/.
//
//go:embed ui/vendor/pdf.min.mjs ui/vendor/pdf.worker.min.mjs ui/sign.js
var signingAssets embed.FS

var signingAssetPaths = map[string]string{
	"pdf.min.mjs":        "ui/vendor/pdf.min.mjs",
	"pdf.worker.min.mjs": "ui/vendor/pdf.worker.min.mjs",
	"sign.js":            "ui/sign.js",
}

func serveSigningAsset(w http.ResponseWriter, r *http.Request, name string) {
	path, ok := signingAssetPaths[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	body, err := signingAssets.ReadFile(path)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	contentType := "application/octet-stream"
	if strings.HasSuffix(name, ".mjs") || strings.HasSuffix(name, ".js") {
		contentType = "text/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
