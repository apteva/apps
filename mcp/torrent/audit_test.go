package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestIndexerEndpointsAndParsing(t *testing.T) {
	hash := strings.Repeat("a", 40)
	t.Run("apibay full endpoint and numeric counts", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/proxy/q.php" || r.URL.Query().Get("q") != "linux book" || r.URL.Query().Get("token") != "abc" {
				t.Errorf("bad URL %s", r.URL)
			}
			fmt.Fprintf(w, `[{"name":"bad row"},{"name":"Book","info_hash":%q,"seeders":12,"leechers":"3","size":42,"added":1700000000,"category":601}]`, hash)
		}))
		defer srv.Close()
		rs, err := queryApibay(context.Background(), srv.Client(), srv.URL+"/proxy/q.php?token=abc", "linux book", "book", "test")
		if err != nil || len(rs) != 1 || rs[0].Seeders != 12 || rs[0].SizeBytes != 42 {
			t.Fatalf("%+v %v", rs, err)
		}
	})
	t.Run("apibay HTML challenge", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<!DOCTYPE html><html>Challenge</html>") }))
		defer srv.Close()
		_, err := queryApibay(context.Background(), srv.Client(), srv.URL, "linux", "", "test")
		if err == nil || !strings.Contains(err.Error(), "HTML") {
			t.Fatal(err)
		}
	})
	for _, kind := range []string{"jackett", "prowlarr"} {
		t.Run(kind, func(t *testing.T) {
			expected := "/proxy/api/v2.0/indexers/all/results"
			base := "/proxy/api/v2.0/indexers/all"
			if kind == "prowlarr" {
				expected = "/proxy/api/v1/search"
				base = "/proxy/api/v1"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != expected {
					t.Errorf("path %q", r.URL.Path)
				}
				if kind == "jackett" {
					fmt.Fprint(w, `{"Results":[]}`)
				} else {
					fmt.Fprint(w, `[]`)
				}
			}))
			defer srv.Close()
			var err error
			if kind == "jackett" {
				_, err = queryJackett(context.Background(), srv.Client(), srv.URL+base, "key", "linux", "", "test")
			} else {
				_, err = queryProwlarr(context.Background(), srv.Client(), srv.URL+base, "key", "linux", "", "test")
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("torznab link and attributes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel><item><title>Linux</title><link>magnet:?xt=urn:btih:%s</link><pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate><torznab:attr name="size" value="123"/><torznab:attr name="seeders" value="4"/><torznab:attr name="peers" value="6"/></item></channel></rss>`, hash)
		}))
		defer srv.Close()
		rs, err := queryTorznabRSS(context.Background(), srv.Client(), srv.URL, "", "linux", "", "test")
		if err != nil || len(rs) != 1 || rs[0].Infohash != hash || rs[0].SizeBytes != 123 || rs[0].Leechers != 2 || rs[0].PublishedAt != "2006-01-02T22:04:05Z" {
			t.Fatalf("%+v %v", rs, err)
		}
	})
	t.Run("invalid URL does not panic", func(t *testing.T) {
		if _, err := queryApibay(context.Background(), http.DefaultClient, "://invalid", "x", "", "test"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func newAuditEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine(EngineConfig{WorkingDir: t.TempDir(), MaxConcurrent: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

// Two independent pieces make skipping the second file unambiguous.
func auditTorrent(t *testing.T, e *Engine, name string, firstComplete bool) *managedTorrent {
	t.Helper()
	data := bytes.Repeat([]byte{42}, 16<<10)
	h := sha1.Sum(data)
	info := metainfo.Info{Name: name, PieceLength: int64(len(data)), Pieces: append(append([]byte{}, h[:]...), h[:]...), Files: []metainfo.FileInfo{{Length: int64(len(data)), Path: []string{"first"}}, {Length: int64(len(data)), Path: []string{"second"}}}}
	if firstComplete {
		if err := os.MkdirAll(filepath.Join(e.cfg.WorkingDir, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.cfg.WorkingDir, name, "first"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	tor, err := e.cli.AddTorrent(&metainfo.MetaInfo{InfoBytes: b})
	if err != nil {
		t.Fatal(err)
	}
	mt := &managedTorrent{t: tor, infohash: tor.InfoHash().HexString(), queued: true, addedAt: time.Now(), priorityHint: map[int]types.PiecePriority{}}
	e.torrents[mt.infohash] = mt
	if firstComplete {
		deadline := time.Now().Add(3 * time.Second)
		for tor.Files()[0].BytesCompleted() != int64(len(data)) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if tor.Files()[0].BytesCompleted() != int64(len(data)) {
			t.Fatal("piece verification did not finish")
		}
	}
	return mt
}

func TestQueueAndSelectiveDownload(t *testing.T) {
	e := newAuditEngine(t)
	a := auditTorrent(t, e, "first-torrent", true)
	b := auditTorrent(t, e, "second-torrent", false)
	a.priorityHint[1] = types.PiecePriorityNone
	e.onMetadataReadyTorrent(a.infohash, a.t)
	e.onMetadataReadyTorrent(b.infohash, b.t)
	snap := e.Snapshot(a.infohash)
	if snap.BytesMissing != 0 || snap.Progress != 1 {
		t.Fatalf("skipped bytes block completion: %+v", snap)
	}
	if a.t.Files()[1].Priority() != types.PiecePriorityNone {
		t.Fatal("skipped file enabled")
	}
	if b.queued {
		t.Fatal("completed selection consumed a download slot")
	}
	if err := e.Pause(b.infohash); err != nil {
		t.Fatal(err)
	}
	if err := e.SetFilePriority(b.infohash, 0, "high"); err != nil {
		t.Fatal(err)
	}
	if b.t.Files()[0].Priority() != types.PiecePriorityNone {
		t.Fatal("priority edit resumed paused download")
	}
	if err := e.Resume(b.infohash); err != nil {
		t.Fatal(err)
	}
	if b.t.Files()[0].Priority() != types.PiecePriorityHigh {
		t.Fatal("priority not restored")
	}
}

func TestMetadataQueueAdmitsOneAndPreservesFIFO(t *testing.T) {
	e := newAuditEngine(t)
	a := auditTorrent(t, e, "a", false)
	b := auditTorrent(t, e, "b", false)
	c := auditTorrent(t, e, "c", false)
	for _, mt := range []*managedTorrent{a, b, c} {
		e.onMetadataReadyTorrent(mt.infohash, mt.t)
	}
	if a.queued || !b.queued || !c.queued {
		t.Fatal("wrong admission")
	}
	if err := e.SetFilePriority(b.infohash, 0, "high"); err != nil {
		t.Fatal(err)
	}
	if b.t.Files()[0].Priority() != types.PiecePriorityNone {
		t.Fatal("queued priority started download")
	}
	if err := e.Pause(a.infohash); err != nil {
		t.Fatal(err)
	}
	if b.queued || !c.queued {
		t.Fatal("queue did not admit oldest waiting torrent")
	}
}

func TestNativeMetadataFromLocalPeer(t *testing.T) {
	e := newAuditEngine(t)
	mt := auditTorrent(t, e, "native-metadata", true)
	addr := e.cli.ListenAddrs()[0].String()
	// Listener is wildcard; dial the corresponding loopback address.
	if strings.HasPrefix(addr, "[::]") {
		addr = strings.Replace(addr, "[::]", "[::1]", 1)
	} else if strings.HasPrefix(addr, "0.0.0.0") {
		addr = strings.Replace(addr, "0.0.0.0", "127.0.0.1", 1)
	}
	magnet := "magnet:?xt=urn:btih:" + mt.infohash + "&x.pe=" + url.QueryEscape(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rs, err := e.resolveMetadata(ctx, magnet)
	if err != nil || len(rs) != 1 || rs[0].Name != "native-metadata" || !rs[0].AvailabilityUnknown {
		t.Fatalf("%+v %v", rs, err)
	}
	stats := mt.t.Stats()
	if len(e.torrents) != 1 || !mt.queued || stats.BytesReadData.Int64() != 0 {
		t.Fatal("lookup changed active engine or downloaded payload")
	}
}

func TestSavedSearchAutoAddUsesOneSource(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("audit"))
	e := newAuditEngine(t)
	app := &App{ctx: ctx, engine: e}
	hash := strings.Repeat("b", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"name":"Linux","info_hash":%q,"seeders":"1","size":"123"}]`, hash)
	}))
	defer srv.Close()
	if _, err := addIndexer(ctx.AppDB(), "audit", "test", "apibay", srv.URL, "", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := addSavedSearch(ctx.AppDB(), "audit", SavedSearch{Query: "linux", AutoAddTopN: 1, RunIntervalMinutes: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE saved_searches SET next_run_at = NULL`); err != nil {
		t.Fatal(err)
	}
	app.runDueSearches(context.Background())
	rows, err := listTorrentRows(ctx.AppDB(), "audit", "all")
	if err != nil || len(rows) != 1 || rows[0].Infohash != hash {
		t.Fatalf("auto-add failed: %+v %v", rows, err)
	}
}

func TestRecoveredEngineErrorClearsButUploadErrorPersists(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("audit"))
	app := &App{ctx: ctx}
	hash := strings.Repeat("c", 40)
	row, err := upsertTorrentRow(ctx.AppDB(), "audit", TorrentRow{Infohash: hash, State: "error", AddedAt: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"info not received yet (peers / DHT may be cold)", "upload to storage failed: unavailable"} {
		if _, err := ctx.AppDB().Exec(`UPDATE torrents SET state='error', last_error=? WHERE id=?`, message, row.ID); err != nil {
			t.Fatal(err)
		}
		app.persistSnapshot("audit", hash, TorrentSnapshot{State: "downloading"})
		got, err := getTorrentRow(ctx.AppDB(), "audit", hash)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(message, "info") {
			if got.State != "downloading" || got.LastError != "" {
				t.Fatalf("recovery hidden: %+v", got)
			}
		} else if got.State != "error" || got.LastError != message {
			t.Fatal("upload failure lost")
		}
	}
}

func TestApiBayLiveOptIn(t *testing.T) {
	if os.Getenv("APTEVA_TORRENT_LIVE_SEARCH") != "1" {
		t.Skip("set APTEVA_TORRENT_LIVE_SEARCH=1 for read-only upstream check")
	}
	rs, err := queryApibay(context.Background(), &http.Client{Timeout: 15 * time.Second}, "https://apibay.org/q.php", "ubuntu", "", "apibay")
	if err != nil || len(rs) == 0 {
		t.Fatalf("ApiBay live query: %d results, %v", len(rs), err)
	}
	t.Logf("ApiBay adapter returned %d results", len(rs))
}

func TestSharedIntentReplacesRemovedProjectPriorities(t *testing.T) {
	e := newAuditEngine(t)
	mt := auditTorrent(t, e, "shared", false)
	e.RestoreState(mt.infohash, false, map[int]string{1: "skip"})
	if mt.t.Files()[1].Priority() != types.PiecePriorityNone {
		t.Fatal("skip not applied")
	}
	e.RestoreState(mt.infohash, false, nil)
	if mt.t.Files()[1].Priority() != types.PiecePriorityNormal {
		t.Fatal("removed project left stale skip hint")
	}
}
