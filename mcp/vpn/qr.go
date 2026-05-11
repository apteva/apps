package main

import (
	"fmt"
	"strings"

	"rsc.io/qr"
)

// renderQRSVG turns the WireGuard config text into a tiny inline SVG
// QR code clients can scan with the WG mobile app (iOS / Android) for
// one-tap import.
//
// rsc.io/qr produces a Code with Size = NxN modules; we paint one <rect>
// per black module against a viewBox sized to that grid, then let the
// browser scale via width="100%". Pure Go, no PNG roundtrip, no
// embedded fonts — copy-paste safe into any HTML response.
//
// On the unhappy path (qr lib refuses, e.g. content too big for level M)
// we return an empty string. Callers should fall back to "QR unavailable
// — paste the config text" rather than blocking.
func renderQRSVG(text string) string {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return ""
	}
	n := code.Size

	var b strings.Builder
	fmt.Fprintf(&b,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="WireGuard QR code">`,
		n, n,
	)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/>`, n, n)
	for y := 0; y < n; y++ {
		// Coalesce horizontal runs of black modules into a single
		// <rect> — same visual, ~6× smaller payload than per-pixel.
		x := 0
		for x < n {
			if !code.Black(x, y) {
				x++
				continue
			}
			start := x
			for x < n && code.Black(x, y) {
				x++
			}
			fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%d" height="1" fill="#000000"/>`,
				start, y, x-start)
		}
	}
	b.WriteString(`</svg>`)
	return b.String()
}
