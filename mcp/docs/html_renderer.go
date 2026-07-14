package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	htemplate "html/template"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const htmlRendererVersion = "chromium-v1"

type DocumentSettings struct {
	PageSize        string   `json:"page_size,omitempty"`
	Landscape       bool     `json:"landscape,omitempty"`
	MarginTopMM     *float64 `json:"margin_top_mm,omitempty"`
	MarginRightMM   *float64 `json:"margin_right_mm,omitempty"`
	MarginBottomMM  *float64 `json:"margin_bottom_mm,omitempty"`
	MarginLeftMM    *float64 `json:"margin_left_mm,omitempty"`
	PrintBackground *bool    `json:"print_background,omitempty"`
}

type htmlRenderOptions struct {
	PageSize      string
	ImageResolver imageResolver
	Timeout       time.Duration
}

var cssURLPattern = regexp.MustCompile(`(?is)url\((.*?)\)`)

func renderHTMLToPDF(ctx context.Context, body, stylesheet string, data map[string]any, settings DocumentSettings, opts htmlRenderOptions) ([]byte, error) {
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("HTML template body empty")
	}
	if data == nil {
		data = map[string]any{}
	}
	merged, err := mergeHTMLTemplate(body, data)
	if err != nil {
		return nil, fmt.Errorf("HTML template substitution: %w", err)
	}
	cleanBody, err := sanitizeHTMLFragment(merged, opts.ImageResolver)
	if err != nil {
		return nil, err
	}
	cleanCSS, err := sanitizeCSS(stylesheet, opts.ImageResolver)
	if err != nil {
		return nil, err
	}
	pageHTML := buildPrintableHTML(cleanBody, cleanCSS, documentTitle(data))
	chromePath, err := findChromeExecutable()
	if err != nil {
		return nil, err
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 25 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	profileDir, err := os.MkdirTemp("", "apteva-docs-chrome-*")
	if err != nil {
		return nil, fmt.Errorf("create Chrome profile: %w", err)
	}
	defer os.RemoveAll(profileDir)
	execPath, launchEnv, err := prepareChromeLaunch(chromePath, profileDir)
	if err != nil {
		return nil, err
	}
	allocatorOptions := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	allocatorOptions = append(allocatorOptions,
		chromedp.ExecPath(execPath),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("no-proxy-server", true),
	)
	if len(launchEnv) > 0 {
		allocatorOptions = append(allocatorOptions, chromedp.Env(launchEnv...))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer allocCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	pageSize := opts.PageSize
	if pageSize == "" {
		pageSize = settings.PageSize
	}
	width, height := paperDimensions(pageSize)
	if settings.Landscape {
		width, height = height, width
	}
	printBackground := true
	if settings.PrintBackground != nil {
		printBackground = *settings.PrintBackground
	}
	marginTop := marginInches(settings.MarginTopMM, 12)
	marginRight := marginInches(settings.MarginRightMM, 12)
	marginBottom := marginInches(settings.MarginBottomMM, 12)
	marginLeft := marginInches(settings.MarginLeftMM, 12)

	dataURL := "data:text/html;base64," + base64.StdEncoding.EncodeToString([]byte(pageHTML))
	var pdf []byte
	err = chromedp.Run(browserCtx,
		network.Enable(),
		network.SetBlockedURLs().WithURLPatterns([]*network.BlockPattern{
			{URLPattern: "http://*/*", Block: true},
			{URLPattern: "https://*/*", Block: true},
			{URLPattern: "file://*/*", Block: true},
			{URLPattern: "ftp://*/*", Block: true},
			{URLPattern: "ws://*/*", Block: true},
			{URLPattern: "wss://*/*", Block: true},
		}),
		emulation.SetScriptExecutionDisabled(true),
		chromedp.Navigate(dataURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(75*time.Millisecond),
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			var printErr error
			pdf, _, printErr = cdppage.PrintToPDF().
				WithPrintBackground(printBackground).
				WithPaperWidth(width).
				WithPaperHeight(height).
				WithMarginTop(marginTop).
				WithMarginRight(marginRight).
				WithMarginBottom(marginBottom).
				WithMarginLeft(marginLeft).
				WithDisplayHeaderFooter(false).
				WithPreferCSSPageSize(false).
				Do(actionCtx)
			return printErr
		}),
	)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("HTML render exceeded %s", opts.Timeout)
		}
		return nil, fmt.Errorf("Chromium PDF render: %w", err)
	}
	if len(pdf) < 5 || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, errors.New("Chromium returned an invalid PDF")
	}
	return pdf, nil
}

func mergeHTMLTemplate(body string, data map[string]any) (string, error) {
	funcs := htemplate.FuncMap{
		"asset": func(value any) (htemplate.URL, error) {
			id := strings.TrimSpace(fmt.Sprint(value))
			if _, err := strconv.ParseInt(id, 10, 64); err != nil {
				return "", errors.New("asset id must be numeric")
			}
			return htemplate.URL("storage:" + id), nil
		},
	}
	t, err := htemplate.New("document").Funcs(funcs).Option("missingkey=zero").Parse(body)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		return "", err
	}
	return out.String(), nil
}

func sanitizeHTMLFragment(source string, resolve imageResolver) (string, error) {
	contextNode := &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := xhtml.ParseFragment(strings.NewReader(source), contextNode)
	if err != nil {
		return "", fmt.Errorf("parse HTML: %w", err)
	}
	for _, node := range nodes {
		if err := sanitizeHTMLNode(node, resolve); err != nil {
			return "", err
		}
	}
	var out bytes.Buffer
	for _, node := range nodes {
		if err := xhtml.Render(&out, node); err != nil {
			return "", fmt.Errorf("serialize HTML: %w", err)
		}
	}
	return out.String(), nil
}

func sanitizeHTMLNode(node *xhtml.Node, resolve imageResolver) error {
	if node.Type == xhtml.ElementNode {
		tag := strings.ToLower(node.Data)
		if !allowedHTMLTag(tag) {
			return fmt.Errorf("HTML element <%s> is not allowed in document templates", tag)
		}
		attrs := make([]xhtml.Attribute, 0, len(node.Attr))
		for _, attr := range node.Attr {
			name := strings.ToLower(attr.Key)
			if strings.HasPrefix(name, "on") || name == "srcdoc" {
				return fmt.Errorf("HTML attribute %q is not allowed", name)
			}
			if !allowedHTMLAttribute(name) {
				return fmt.Errorf("HTML attribute %q is not allowed", name)
			}
			switch name {
			case "src":
				if tag != "img" {
					return fmt.Errorf("src is only supported on images")
				}
				resolved, err := resolveHTMLImage(attr.Val, resolve)
				if err != nil {
					return err
				}
				attr.Val = resolved
			case "href":
				if err := validateDocumentLink(attr.Val); err != nil {
					return err
				}
			case "style":
				clean, err := sanitizeCSS(attr.Val, resolve)
				if err != nil {
					return fmt.Errorf("inline style: %w", err)
				}
				attr.Val = clean
			}
			attrs = append(attrs, attr)
		}
		node.Attr = attrs
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if err := sanitizeHTMLNode(child, resolve); err != nil {
			return err
		}
	}
	return nil
}

func allowedHTMLTag(tag string) bool {
	allowed := map[string]bool{
		"a": true, "abbr": true, "address": true, "article": true, "aside": true,
		"b": true, "blockquote": true, "br": true, "caption": true, "code": true,
		"col": true, "colgroup": true, "dd": true, "div": true, "dl": true, "dt": true,
		"em": true, "figcaption": true, "figure": true, "footer": true, "h1": true,
		"h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "header": true,
		"hr": true, "i": true, "img": true, "li": true, "main": true, "mark": true,
		"nav": true, "ol": true, "p": true, "pre": true, "section": true, "small": true,
		"span": true, "strong": true, "sub": true, "sup": true, "table": true,
		"tbody": true, "td": true, "tfoot": true, "th": true, "thead": true, "tr": true,
		"u": true, "ul": true,
	}
	return allowed[tag]
}

func allowedHTMLAttribute(name string) bool {
	if strings.HasPrefix(name, "aria-") || strings.HasPrefix(name, "data-") {
		return true
	}
	allowed := map[string]bool{
		"alt": true, "class": true, "colspan": true, "height": true, "href": true,
		"id": true, "rel": true, "role": true, "rowspan": true, "src": true,
		"style": true, "target": true, "title": true, "width": true,
	}
	return allowed[name]
}

func resolveHTMLImage(src string, resolve imageResolver) (string, error) {
	src = strings.TrimSpace(src)
	if resolve == nil {
		return "", errors.New("HTML image cannot be resolved without Storage")
	}
	data, ext, err := resolve(src)
	if err != nil {
		return "", fmt.Errorf("resolve HTML image %q: %w", src, err)
	}
	mime := "image/png"
	if ext == "jpg" || ext == "jpeg" {
		mime = "image/jpeg"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func validateDocumentLink(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid link %q", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "mailto", "tel":
		return nil
	default:
		return fmt.Errorf("link scheme %q is not allowed", u.Scheme)
	}
}

func sanitizeCSS(source string, resolve imageResolver) (string, error) {
	lower := strings.ToLower(source)
	for _, blocked := range []string{"</style", "@import", "expression(", "javascript:", "-moz-binding", "behavior:"} {
		if strings.Contains(lower, blocked) {
			return "", fmt.Errorf("CSS construct %q is not allowed", blocked)
		}
	}
	var replaceErr error
	clean := cssURLPattern.ReplaceAllStringFunc(source, func(match string) string {
		if replaceErr != nil {
			return match
		}
		parts := cssURLPattern.FindStringSubmatch(match)
		if len(parts) != 2 {
			replaceErr = errors.New("invalid CSS url()")
			return match
		}
		raw := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if strings.HasPrefix(raw, "data:image/png;base64,") || strings.HasPrefix(raw, "data:image/jpeg;base64,") ||
			strings.HasPrefix(raw, "data:font/woff;base64,") || strings.HasPrefix(raw, "data:font/woff2;base64,") {
			return `url("` + raw + `")`
		}
		if strings.HasPrefix(raw, "storage:") || isNumericString(raw) {
			resolved, err := resolveHTMLImage(raw, resolve)
			if err != nil {
				replaceErr = err
				return match
			}
			return `url("` + resolved + `")`
		}
		replaceErr = fmt.Errorf("external CSS asset %q is not allowed", raw)
		return match
	})
	if replaceErr != nil {
		return "", replaceErr
	}
	return clean, nil
}

func isNumericString(value string) bool {
	_, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return err == nil
}

func documentTitle(data map[string]any) string {
	title := strings.TrimSpace(fmt.Sprint(data["title"]))
	if title == "" || title == "<nil>" {
		return "Document"
	}
	if runes := []rune(title); len(runes) > 200 {
		title = string(runes[:200])
	}
	return title
}

func buildPrintableHTML(body, stylesheet, title string) string {
	const csp = "default-src 'none'; img-src data:; font-src data:; style-src 'unsafe-inline'; script-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"
	return "<!doctype html><html><head><meta charset=\"utf-8\"><meta http-equiv=\"Content-Security-Policy\" content=\"" + csp + "\">" +
		"<title>" + html.EscapeString(title) + "</title>" +
		"<style>html{-webkit-print-color-adjust:exact;print-color-adjust:exact}body{margin:0}" + stylesheet + "</style>" +
		"</head><body>" + body + "</body></html>"
}

func findChromeExecutable() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("APTEVA_CHROME_PATH")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("APTEVA_CHROME_PATH does not point to an executable file: %s", configured)
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	candidates := []string{}
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if root != "" {
				candidates = append(candidates,
					filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
					filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"))
			}
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("HTML rendering requires Chromium or Chrome; set APTEVA_CHROME_PATH when it is installed outside the standard locations")
}

func paperDimensions(pageSize string) (float64, float64) {
	switch strings.ToLower(strings.TrimSpace(pageSize)) {
	case "letter":
		return 8.5, 11
	case "legal":
		return 8.5, 14
	default:
		return 8.2677165, 11.692913
	}
}

func marginInches(value *float64, fallbackMM float64) float64 {
	mm := fallbackMM
	if value != nil {
		mm = *value
	}
	if mm < 0 {
		mm = 0
	}
	if mm > 50 {
		mm = 50
	}
	return mm / 25.4
}
