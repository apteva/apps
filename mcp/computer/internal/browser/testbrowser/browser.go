package testbrowser

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/apteva/apps/mcp/computer/internal/browser/checkedinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/fileupload"
	"github.com/apteva/apps/mcp/computer/internal/browser/selectinput"
	"github.com/apteva/apps/mcp/computer/internal/browser/textinput"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/network"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

type Browser struct {
	ctx             context.Context
	cancel          context.CancelFunc
	allocatorCancel context.CancelFunc
	boundary        *boundary
	options         Options
	mu              sync.Mutex
	stateMu         sync.Mutex
	errors          []string
	secrets         []string
	engine          string
	deniedPages     int
}

func New(request context.Context, opts Options) (*Browser, error) {
	if err := opts.Policy.Validate(); err != nil {
		return nil, err
	}
	if opts.Width < 320 || opts.Width > 1920 || opts.Height < 240 || opts.Height > 1200 || opts.Profile == "" {
		return nil, errors.New("invalid viewport or ephemeral profile")
	}
	edge, err := newBoundary(opts.Policy)
	if err != nil {
		return nil, err
	}
	flags := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	flags = append(flags,
		chromedp.Headless, chromedp.DisableGPU,
		chromedp.UserDataDir(opts.Profile),
		chromedp.ProxyServer(edge.URL()),
		chromedp.Flag("proxy-bypass-list", "<-loopback>"),
		chromedp.Flag("disable-quic", true),
		chromedp.Flag("dns-prefetch-disable", true),
		chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.*, EXCLUDE [::1]"),
		chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-domain-reliability", true),
		chromedp.Flag("disable-features", "site-per-process,Translate,BlinkGenPropertyTrees,AutofillServerCommunication,OptimizationHints,MediaRouter,PasswordLeakDetection"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("lang", opts.Locale),
	)
	if path := os.Getenv("CHROME_BIN"); path != "" {
		flags = append(flags, chromedp.ExecPath(path))
	}
	if os.Getenv("APTEVA_CHROME_NO_SANDBOX") == "1" {
		flags = append(flags, chromedp.NoSandbox)
	}
	alloc, allocCancel := chromedp.NewExecAllocator(context.Background(), flags...)
	ctx, cancel := chromedp.NewContext(alloc)
	b := &Browser{ctx: ctx, cancel: cancel, allocatorCancel: allocCancel, boundary: edge, options: opts}
	stop := context.AfterFunc(request, cancel)
	err = chromedp.Run(ctx,
		emulation.SetDeviceMetricsOverride(int64(opts.Width), int64(opts.Height), 1, false),
		emulation.SetTimezoneOverride(opts.Timezone),
		emulation.SetLocaleOverride().WithLocale(opts.Locale),
		network.Enable(),
		cdpruntime.Enable(),
		chromedp.Evaluate("navigator.userAgent", &b.engine),
	)
	stop()
	if err != nil || request.Err() != nil {
		b.Close()
		return nil, errors.New("local Chromium startup failed")
	}
	chromedp.ListenTarget(ctx, func(event any) {
		switch e := event.(type) {
		case *network.EventRequestWillBeSent:
			u, err := url.Parse(e.Request.URL)
			if err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "ws" || u.Scheme == "wss") {
				if u.Scheme == "ws" {
					u.Scheme = "http"
				}
				if opts.Policy.Match(u) == "" {
					b.stateMu.Lock()
					b.deniedPages++
					b.stateMu.Unlock()
					b.boundary.record(Request{Method: e.Request.Method, Origin: safeOrigin(u), Status: 403})
				}
			}

		case *cdpruntime.EventExceptionThrown:
			b.addError(e.ExceptionDetails.Text)
		case *cdpruntime.EventConsoleAPICalled:
			if e.Type == cdpruntime.APITypeError {
				b.addError("browser console error")
			}
		}
	})
	return b, nil
}
func (b *Browser) PID() int {
	c := chromedp.FromContext(b.ctx)
	if c == nil || c.Browser == nil || c.Browser.Process() == nil {
		return 0
	}
	return c.Browser.Process().Pid
}
func (b *Browser) Engine() string { return b.engine }
func (b *Browser) Close() {
	b.cancel()
	b.allocatorCancel()
	b.boundary.close()
}
func (b *Browser) addError(text string) {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if len(b.errors) < 40 {
		b.errors = append(b.errors, text)
	}
}
func (b *Browser) RegisterSecret(value string) {
	if value == "" {
		return
	}
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	if len(b.secrets) < 128 {
		b.secrets = append(b.secrets, value)
	}
}
func (b *Browser) clean(value string) string {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	for _, secret := range b.secrets {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	if len(value) > 4096 {
		return value[:4096]
	}
	return value
}
func (b *Browser) Diagnostics() Diagnostics {
	d := b.boundary.diagnostics()
	b.stateMu.Lock()
	raw := append([]string{}, b.errors...)
	d.Blocked += b.deniedPages
	b.stateMu.Unlock()
	d.Errors = make([]string, len(raw))
	for i, text := range raw {
		d.Errors[i] = b.clean(text)
	}
	for i := range d.Requests {
		d.Requests[i].Origin = b.clean(d.Requests[i].Origin)
		d.Requests[i].Method = b.clean(d.Requests[i].Method)
	}
	return d
}
func (b *Browser) Do(ctx context.Context, command Command) (Result, error) {
	if err := command.Validate(); err != nil {
		return Result{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	op, cancel := context.WithCancel(b.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	result := Result{}
	if command.Sensitive {
		b.RegisterSecret(command.Value)
	}
	const selector = `[data-apteva-test-target="active"]`
	var observation struct {
		Observation
		Sensitive bool   `json:"sensitive"`
		Error     string `json:"error"`
	}
	locate := func() error {
		if command.Locator == (Locator{}) {
			return nil
		}
		if err := chromedp.Run(op, chromedp.Evaluate(locatorScript+"("+jsonText(command.Locator)+")", &observation)); err != nil {
			return err
		}
		if observation.Error != "" {
			return errors.New(observation.Error)
		}
		return nil
	}
	if err := locate(); err != nil {
		return result, errors.New("browser target observation failed")
	}
	if command.Action != "observe" && command.Action != "navigate" && command.Action != "reload" {
		if observation.Count != 1 {
			return result, errors.New("browser locator must match exactly one element")
		}
		if !observation.Visible || !observation.Enabled {
			return result, errors.New("browser target is hidden or disabled")
		}
	}
	var err error
	switch command.Action {
	case "navigate":
		target, e := b.options.Policy.URL(command.Target, command.Path)
		if e != nil {
			return result, e
		}
		result.Dispatched = true
		err = chromedp.Run(op, chromedp.Navigate(target))
	case "reload":
		result.Dispatched = true
		err = chromedp.Run(op, chromedp.Reload())
	case "observe":
	case "click":
		result.Dispatched = true
		err = chromedp.Run(op, chromedp.Click(selector, chromedp.ByQuery))
	case "fill":
		if observation.Sensitive {
			b.RegisterSecret(command.Value)
		}
		result.Dispatched = true
		_, err = textinput.Set(op, textinput.Target{Selector: selector}, textinput.SetRequest{Text: command.Value, Mode: "replace"})
	case "select":
		result.Dispatched = true
		_, err = selectinput.Select(op, selectinput.Target{Selector: selector}, selectinput.Request{Value: command.Value})
	case "check":
		result.Dispatched = true
		_, err = checkedinput.Set(op, checkedinput.Target{Selector: selector}, checkedinput.Request{Checked: command.Checked})
	case "upload":
		data, e := base64.StdEncoding.DecodeString(command.File.Base64)
		if e != nil || len(data) > 96<<10 {
			return result, errors.New("invalid upload fixture")
		}
		result.Dispatched = true
		_, err = fileupload.SetPayloads(op, fileupload.Target{Selector: selector}, []fileupload.Payload{{Name: command.File.Name, MIME: command.File.MIME, Data: data}})
	}
	if err != nil {
		return result, errors.New("browser command failed or timed out")
	}
	if command.Action == "observe" {
		result.Observation = observation.Observation
	}
	var location string
	if err = chromedp.Run(op, chromedp.Location(&location)); err != nil {
		return result, errors.New("browser location unavailable")
	}
	for name, raw := range b.options.Policy.Targets {
		if strings.HasPrefix(location, strings.TrimSuffix(raw, "/")+"/") || location == raw {
			result.Observation.Target = name
			path := strings.TrimPrefix(location, strings.TrimSuffix(raw, "/"))
			path = strings.SplitN(strings.SplitN(path, "?", 2)[0], "#", 2)[0]
			if path == "" {
				path = "/"
			}
			result.Observation.Path = b.clean(path)
			break
		}
	}
	if result.Observation.Target == "" && location != "about:blank" {
		return result, errors.New("browser left the permitted runtime")
	}
	result.Observation.Text = b.clean(result.Observation.Text)
	if v := result.Observation.Value; v != nil {
		clean := b.clean(*v)
		if command.Sensitive || observation.Sensitive || clean != *v {
			result.Observation.Value = nil
		} else {
			result.Observation.Value = &clean
		}
	}
	return result, nil
}
func (b *Browser) Screenshot(ctx context.Context) (*Artifact, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	op, cancel := context.WithCancel(b.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer func() { stop(); cancel() }()
	b.stateMu.Lock()
	secrets := append([]string{}, b.secrets...)
	b.stateMu.Unlock()
	script := `(function(secrets){
 const style=document.createElement('style');style.id='apteva-test-mask';
 style.textContent='input,textarea,[contenteditable],[data-test-sensitive],[data-apteva-sensitive-mask]{visibility:hidden!important}';
 document.documentElement.appendChild(style);
 const mask=()=>{const walk=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);let n;while(n=walk.nextNode()){if(secrets.some(s=>s&&n.textContent.includes(s)))n.parentElement?.setAttribute('data-apteva-sensitive-mask','')}};
 mask();window.__aptevaMaskObserver=new MutationObserver(mask);window.__aptevaMaskObserver.observe(document.body,{subtree:true,childList:true,characterData:true});
 })(` + jsonText(secrets) + ")"
	if err := chromedp.Run(op, chromedp.Evaluate(script, nil)); err != nil {
		return nil, errors.New("screenshot masking failed")
	}
	defer func() {
		_ = chromedp.Run(op, chromedp.Evaluate(`window.__aptevaMaskObserver?.disconnect();document.getElementById('apteva-test-mask')?.remove();document.querySelectorAll('[data-apteva-sensitive-mask]').forEach(e=>e.removeAttribute('data-apteva-sensitive-mask'))`, nil))
	}()
	var data []byte
	if err := chromedp.Run(op, chromedp.CaptureScreenshot(&data)); err != nil || len(data) > MaxArtifact {
		return nil, errors.New("screenshot exceeds the capture limit or failed")
	}
	sum := sha256.Sum256(data)
	return &Artifact{MIME: "image/png", SHA256: hex.EncodeToString(sum[:]), Base64: base64.StdEncoding.EncodeToString(data)}, nil
}

func Available() bool {
	if path := os.Getenv("CHROME_BIN"); path != "" {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular()
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}
