package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteJSONReader(t *testing.T) {
	for _, tc := range []struct {
		name, input, path, want string
		invalid                 bool
	}{
		{"reordered", `{"other":{"id":999},"file":{"name":"quoted\" id","id":42},"id":123}`, "file.id", "42", false},
		{"escapedURL", `{"upload_url":"https:\/\/host\/path?a=1\u0026b=2"}`, "upload_url", "https://host/path?a=1&b=2", false},
		{"unicode", `{"name":"\u00e9\ud83d\ude00\n\"\\"}`, "name", "é😀\n\"\\", false},
		{"nestedarray", `{"files":[{"id":3},{"id":4}]}`, "files.1.id", "4", false},
		{"malformed", `{"file":{"id":42},broken}`, "file.id", "", true},
		{"truncated", `{"file":{"id":42}`, "file.id", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", remoteJSONReader+"\njson_get "+shellQuote(tc.path))
			cmd.Stdin = strings.NewReader(tc.input)
			out, err := cmd.CombinedOutput()
			if tc.invalid {
				if err == nil {
					t.Fatalf("invalid JSON accepted: %s", out)
				}
				return
			}
			if err != nil || string(out) != tc.want {
				t.Fatalf("got %q err=%v want %q", out, err, tc.want)
			}
		})
	}
}

func TestRemoteChunksBoundConcurrencyAndAbort(t *testing.T) {
	for _, tc := range []struct {
		parallel int
		fail     bool
	}{{1, false}, {2, false}, {2, true}} {
		t.Run(fmt.Sprintf("parallel%d-fail%v", tc.parallel, tc.fail), func(t *testing.T) {
			var active, peak, aborted atomic.Int32
			var mu sync.Mutex
			parts := map[string]string{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test" {
					t.Errorf("missing auth: %s", r.URL.Path)
					w.WriteHeader(401)
					return
				}
				switch {
				case r.URL.Path == "/files/init":
					w.WriteHeader(501)
				case r.URL.Path == "/uploads":
					fmt.Fprintf(w, `{"part_size":3,"upload_id":"u1","max_parallel":%d}`, tc.parallel)
				case strings.Contains(r.URL.Path, "/parts/"):
					n := active.Add(1)
					defer active.Add(-1)
					for {
						p := peak.Load()
						if n <= p || peak.CompareAndSwap(p, n) {
							break
						}
					}
					time.Sleep(20 * time.Millisecond)
					part := filepath.Base(r.URL.Path)
					if tc.fail && part == "2" {
						w.WriteHeader(500)
						return
					}
					data, _ := io.ReadAll(r.Body)
					mu.Lock()
					parts[part] = string(data)
					mu.Unlock()
				case r.Method == "DELETE":
					aborted.Add(1)
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/complete"):
					mu.Lock()
					got := parts["1"] + parts["2"] + parts["3"] + parts["4"]
					mu.Unlock()
					if got != "abcdefghij" {
						t.Errorf("parts corrupted: %q", got)
					}
					fmt.Fprint(w, `{"file":{"name":"out","id":42}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			root := t.TempDir()
			out := filepath.Join(root, "out.mp4")
			if err := os.WriteFile(out, []byte("abcdefghij"), 0600); err != nil {
				t.Fatal(err)
			}
			script := "set -euo pipefail\ncurl_retry() { curl -sS \"$@\"; }\n"
			for k, v := range map[string]string{"OUT": out, "STORAGE_BASE": srv.URL, "STORAGE_TOKEN": "test", "PROJECT_ID": "test", "NAME_JSON": `"out.mp4"`, "FOLDER_JSON": `"/"`, "CT_JSON": `"video/mp4"`, "SIZE": "10", "SHA": strings.Repeat("a", 64)} {
				script += k + "=" + shellQuote(v) + "\n"
			}
			cmd := exec.Command("bash", "-c", script+uploadScriptFragment+"\nprintf '%s' \"$FILE_ID\"")
			cmd.Dir = root
			raw, err := cmd.CombinedOutput()
			if tc.fail {
				if err == nil || aborted.Load() != 1 {
					t.Fatalf("failed upload not aborted: %s err=%v", raw, err)
				}
			} else if err != nil || string(raw) != "42" {
				t.Fatalf("output=%s err=%v", raw, err)
			}
			if peak.Load() > int32(tc.parallel) || (tc.parallel == 2 && peak.Load() != 2) {
				t.Fatalf("peak concurrency=%d", peak.Load())
			}
		})
	}
}

func TestRemoteMultipartPreservesFilenameAndFolder(t *testing.T) {
	name, folder := `-clip" & café.mp4`, "@folder\"\n/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/files" {
			w.WriteHeader(501)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		_, file, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if file.Filename != name || r.FormValue("folder") != folder {
			t.Errorf("filename=%q folder=%q", file.Filename, r.FormValue("folder"))
		}
		fmt.Fprint(w, `{"other":{"id":999},"file":{"name":"ok","id":42}}`)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "out.mp4")
	if err := os.WriteFile(out, []byte("video"), 0600); err != nil {
		t.Fatal(err)
	}
	nameJSON, _ := json.Marshal(name)
	folderJSON, _ := json.Marshal(folder)
	curlName := `"` + strings.ReplaceAll(name, `"`, `\"`) + `"`
	script := "set -euo pipefail\ncurl_retry() { curl -sS \"$@\"; }\n"
	for k, v := range map[string]string{"OUT": out, "STORAGE_BASE": srv.URL, "STORAGE_TOKEN": "test", "PROJECT_ID": "test", "NAME": name, "FOLDER": folder, "CT": "video/mp4", "NAME_JSON": string(nameJSON), "FOLDER_JSON": string(folderJSON), "CT_JSON": `"video/mp4"`, "CURL_NAME": curlName, "SIZE": "5", "SHA": strings.Repeat("a", 64)} {
		script += k + "=" + shellQuote(v) + "\n"
	}
	cmd := exec.Command("bash", "-c", script+uploadScriptFragment+"\nprintf '%s' \"$FILE_ID\"")
	cmd.Dir = t.TempDir()
	raw, err := cmd.CombinedOutput()
	if err != nil || string(raw) != "42" {
		t.Fatalf("output=%s err=%v", raw, err)
	}
}
