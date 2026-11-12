package browserbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/apteva/apps/mcp/computer/internal/browser/api"
	"github.com/apteva/apps/mcp/computer/internal/browser/navigation"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// This TLS/HTTP2 peer sends real REFUSED_STREAM frames. Chrome itself exhausts
// its internal retries before Computer sees ERR_HTTP2_SERVER_REFUSED_STREAM.
// Computer's two attempts are counted separately from Chromium's wire retries.
func refusedStreamPeer(t *testing.T) (string, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	cert := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	config := cert.TLS.Clone()
	cert.Close()
	config.NextProtos = []string{"h2"}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", config)
	if err != nil {
		t.Fatal(err)
	}
	var refuse atomic.Bool
	var resets atomic.Int32
	var wg sync.WaitGroup
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for c := range connections {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				defer func() { mu.Lock(); delete(connections, conn); mu.Unlock() }()
				preface := make([]byte, len(http2.ClientPreface))
				if _, err := io.ReadFull(conn, preface); err != nil {
					return
				}
				if string(preface) != http2.ClientPreface {
					return
				}
				framer := http2.NewFramer(conn, conn)
				if framer.WriteSettings() != nil {
					return
				}
				for {
					frame, err := framer.ReadFrame()
					if err != nil {
						return
					}
					switch f := frame.(type) {
					case *http2.SettingsFrame:
						if !f.IsAck() {
							if framer.WriteSettingsAck() != nil {
								return
							}
						}
					case *http2.PingFrame:
						if !f.Flags.Has(http2.FlagPingAck) {
							if framer.WritePing(true, f.Data) != nil {
								return
							}
						}
					case *http2.HeadersFrame:
						if refuse.Load() {
							resets.Add(1)
							if framer.WriteRSTStream(f.StreamID, http2.ErrCodeRefusedStream) != nil {
								return
							}
							continue
						}
						var block bytes.Buffer
						encoder := hpack.NewEncoder(&block)
						for _, h := range []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "content-type", Value: "text/html"}} {
							if encoder.WriteField(h) != nil {
								return
							}
						}
						if framer.WriteHeaders(http2.HeadersFrameParam{StreamID: f.StreamID, BlockFragment: block.Bytes(), EndHeaders: true}) != nil {
							return
						}
						if framer.WriteData(f.StreamID, true, []byte(`<html><title>ready</title><body>ready<button onclick="document.body.textContent='saved'">Save</button></body></html>`)) != nil {
							return
						}
					}
				}
			}()
		}
	}()
	return "https://" + listener.Addr().String(), &refuse, &resets
}

func TestBrowserbaseHTTP2ReadOnlyRecovery(t *testing.T) {
	if os.Getenv("RUN_NAVIGATION_BROWSER_TESTS") != "1" {
		t.Skip("set RUN_NAVIGATION_BROWSER_TESTS=1 for real local Chromium HTTP/2 tests")
	}
	for _, mode := range []string{"recover", "exhausted", "not_declared_read_only"} {
		t.Run(mode, func(t *testing.T) {
			endpoint, refuse, resets := refusedStreamPeer(t)
			options := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.Flag("headless", true), chromedp.Flag("ignore-certificate-errors", true), chromedp.Flag("no-sandbox", true), chromedp.UserDataDir(t.TempDir()))
			if executable := os.Getenv("CHROME_BIN"); executable != "" {
				options = append(options, chromedp.ExecPath(executable))
			}
			alloc, stop := chromedp.NewExecAllocator(context.Background(), options...)
			defer stop()
			ctx, cancel := chromedp.NewContext(alloc)
			defer func() {
				// Let Chromium finish profile writes before TempDir cleanup.
				closing, stopClosing := context.WithTimeout(ctx, 5*time.Second)
				defer stopClosing()
				if err := chromedp.Cancel(closing); err != nil {
					t.Errorf("close Chromium: %v", err)
				}
				cancel()
			}()
			if err := chromedp.Run(ctx, chromedp.Navigate(endpoint+"/seed"), chromedp.Evaluate(`localStorage.setItem('session-proof','preserved');document.cookie='context=preserved;path=/'`, nil)); err != nil {
				t.Fatal(err)
			}
			c := &Computer{ctx: ctx, cancel: cancel, allocCtx: ctx, display: api.DisplaySize{Width: 1200, Height: 800}, sessionID: "same-provider", contextID: "same-context"}
			previous := c.ActiveTabID()
			var pages atomic.Int32
			chromedp.ListenBrowser(ctx, func(event any) {
				if e, ok := event.(*target.EventTargetCreated); ok && e.TargetInfo.Type == "page" {
					pages.Add(1)
					if mode == "recover" {
						refuse.Store(false)
					}
				}
			})
			refuse.Store(true)
			started := time.Now()
			err := c.ExecuteAction(api.Action{Type: "navigate", URL: endpoint + "/performers", ReadOnly: mode != "not_declared_read_only"})
			d := c.LastNavigation()
			if time.Since(started) > 70*time.Second {
				t.Fatal("unbounded recovery")
			}
			if d != nil && d.Attempts[0].FailedURL != endpoint+"/performers" {
				t.Fatalf("missing failed request URL: %+v", d.Attempts[0])
			}
			if resets.Load() == 0 {
				t.Fatal("HTTP/2 failure was not injected")
			}
			if d == nil || !strings.Contains(d.Attempts[0].Error, "ERR_HTTP2_SERVER_REFUSED_STREAM") {
				t.Fatalf("missing Chrome HTTP/2 error: %+v err=%v", d, err)
			}
			if mode == "recover" {
				if err != nil || !d.Recovered || len(d.Attempts) != 2 || pages.Load() != 1 || c.ActiveTabID() == previous {
					t.Fatalf("not recovered once: %+v err=%v pages=%d", d, err, pages.Load())
				}
				var stored, cookie string
				if err := chromedp.Run(c.ctx, chromedp.Evaluate(`localStorage.getItem('session-proof')`, &stored), chromedp.Evaluate(`document.cookie`, &cookie)); err != nil {
					t.Fatal(err)
				}
				if stored != "preserved" || !strings.Contains(cookie, "context=preserved") || c.sessionID != "same-provider" || c.contextID != "same-context" {
					t.Fatal("saved context was lost")
				}
				if d.Attempts[1].Requests[0].Protocol != "h2" {
					t.Fatal("missing HTTP/2 response diagnostics")
				}
			} else {
				var failed *navigation.Failure
				if !errors.As(err, &failed) {
					t.Fatalf("no structured failure: %v", err)
				}
				want := 2
				if mode == "not_declared_read_only" {
					want = 1
				}
				if len(d.Attempts) != want || int(pages.Load()) != want-1 {
					t.Fatalf("unexpected replay attempts=%d new pages=%d", len(d.Attempts), pages.Load())
				}
				if c.ActiveTabID() != previous {
					t.Fatal("failed retry discarded original inspection page")
				}
			}
		})
	}
}
