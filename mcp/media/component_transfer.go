package main

// Job-scoped transfer capabilities adapt both S3 direct uploads and disk-backed
// resumable uploads. The parent's normal Storage token never leaves this app.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

var transferID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type transferBudget struct {
	reader io.Reader
	hash   string
}

func (b *transferBudget) Read(p []byte) (int, error) {
	n, e := b.reader.Read(p)
	if n > 0 {
		res, err := globalCtx.AppDB().Exec(`UPDATE media_executor_grants SET used_bytes=used_bytes+? WHERE token_hash=? AND expires_at>? AND used_bytes+?<=max_bytes`, n, b.hash, time.Now().Unix(), n)
		if err != nil {
			return 0, err
		}
		changed, _ := res.RowsAffected()
		if changed != 1 {
			return 0, errors.New("executor transfer limit exceeded")
		}
	}
	return n, e
}
func (*App) handleExecutorTransfer(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	var project, foldersRaw string
	var max int64
	if token == "" || globalCtx.AppDB().QueryRow(`SELECT project_id,folders,max_bytes FROM media_executor_grants WHERE token_hash=? AND expires_at>?`, hash, time.Now().Unix()).Scan(&project, &foldersRaw, &max) != nil {
		http.Error(w, "invalid transfer capability", 403)
		return
	}
	var folders []string
	if json.Unmarshal([]byte(foldersRaw), &folders) != nil {
		http.Error(w, "invalid transfer scope", 403)
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/executor-io")
	if !strings.HasPrefix(tail, boundStorageProxyPath+"/") {
		http.Error(w, "invalid transfer path", 403)
		return
	}
	tail = strings.TrimPrefix(tail, boundStorageProxyPath)
	if path.Clean(tail) != tail || strings.ContainsAny(tail, "%\\") {
		http.Error(w, "invalid transfer path", 403)
		return
	}
	pieces := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	init := r.Method == "POST" && (tail == "/files/init" || tail == "/uploads")
	legacy := r.Method == "POST" && tail == "/files"
	allowed := init || legacy
	kind := ""
	id := ""
	if len(pieces) >= 2 && transferID.MatchString(pieces[1]) {
		kind = pieces[0]
		id = pieces[1]
		if kind == "files" && len(pieces) == 3 && pieces[2] == "finalize" && r.Method == "POST" {
			allowed = true
		}
		if kind == "uploads" && ((len(pieces) == 4 && pieces[2] == "parts" && r.Method == "PUT" && positivePart(pieces[3])) || (len(pieces) == 3 && pieces[2] == "complete" && r.Method == "POST") || (len(pieces) == 2 && r.Method == "DELETE")) {
			allowed = true
		}
		if allowed && !init && !legacy {
			var found int
			if globalCtx.AppDB().QueryRow(`SELECT 1 FROM media_executor_uploads WHERE token_hash=? AND kind=? AND upload_id=?`, hash, kind, id).Scan(&found) != nil {
				allowed = false
			}
		}
	}
	if !allowed {
		http.Error(w, "transfer operation outside job scope", 403)
		return
	}
	if init || legacy {
		res, err := globalCtx.AppDB().Exec(`UPDATE media_executor_grants SET requests=requests+1 WHERE token_hash=? AND requests<128`, hash)
		if err != nil {
			http.Error(w, "transfer unavailable", 503)
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			http.Error(w, "transfer count exceeded", 429)
			return
		}
	}
	var body io.Reader = &transferBudget{reader: r.Body, hash: hash}
	ct := r.Header.Get("Content-Type")
	var reserved int64
	if init {
		raw, err := io.ReadAll(io.LimitReader(body, 64<<10))
		if err != nil {
			http.Error(w, "transfer limit exceeded", 413)
			return
		}
		var fields map[string]any
		if json.Unmarshal(raw, &fields) != nil || !allowedTransferFolder(fields["folder"], folders) {
			http.Error(w, "invalid output destination", 403)
			return
		}
		size, _ := fields["size_bytes"].(float64)
		if size == 0 {
			size, _ = fields["size"].(float64)
		}
		if size <= 0 || size > float64(max/2) {
			http.Error(w, "output size exceeds grant", 413)
			return
		}
		reserved = int64(size)
		res, err := globalCtx.AppDB().Exec(`UPDATE media_executor_grants SET reserved_bytes=reserved_bytes+? WHERE token_hash=? AND reserved_bytes+?<=max_bytes/2`, reserved, hash, reserved)
		if err != nil {
			http.Error(w, "transfer unavailable", 503)
			return
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			http.Error(w, "declared output budget exceeded", 413)
			return
		}
		fields["project_id"] = project
		fields["visibility"] = "private"
		raw, _ = json.Marshal(fields)
		body = bytes.NewReader(raw)
	} else if legacy {
		mediaType, params, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			http.Error(w, "expected multipart upload", 400)
			return
		}
		reader := multipart.NewReader(body, params["boundary"])
		pipeR, pipeW := io.Pipe()
		defer pipeR.Close()
		writer := multipart.NewWriter(pipeW)
		ct = writer.FormDataContentType()
		body = pipeR
		go func() {
			failed := func(err error) { _ = pipeW.CloseWithError(err) }
			fileCount := 0
			folderSeen := false
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					failed(err)
					return
				}
				name := part.FormName()
				if part.FileName() != "" {
					fileCount++
					if fileCount != 1 || name != "file" || !folderSeen {
						failed(errors.New("invalid output file"))
						return
					}
					dest, err := writer.CreatePart(part.Header)
					if err != nil {
						failed(err)
						return
					}
					copied, copyErr := io.Copy(dest, io.LimitReader(part, max/2+1))
					if copyErr != nil || copied > max/2 {
						err = errors.New("output exceeds grant")
						failed(err)
						return
					}
				} else {
					raw, err := io.ReadAll(io.LimitReader(part, 8193))
					if err != nil || len(raw) > 8192 {
						failed(errors.New("invalid output field"))
						return
					}
					value := string(raw)
					switch name {
					case "folder":
						if !allowedTransferFolder(value, folders) {
							failed(errors.New("invalid output folder"))
							return
						}
						folderSeen = true
					case "visibility":
						value = "private"
					case "project_id":
						value = project
					case "source", "tags", "name", "filename", "content_type":
					default:
						failed(errors.New("unsupported output field"))
						return
					}
					if err = writer.WriteField(name, value); err != nil {
						failed(err)
						return
					}
				}
				part.Close()
			}
			if fileCount != 1 {
				failed(errors.New("missing output file"))
				return
			}
			if err := writer.Close(); err != nil {
				failed(err)
				return
			}
			pipeW.Close()
		}()
	}
	sc := newStorageClient()
	query := url.Values{"project_id": []string{project}}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, sc.base+tail+"?"+query.Encode(), body)
	if err != nil {
		http.Error(w, "transfer unavailable", 503)
		return
	}
	req.Header.Set("Authorization", "Bearer "+sc.token)
	req.Header.Set("Content-Type", ct)
	client := *sc.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "transfer failed", 502)
		return
	}
	defer resp.Body.Close()
	if reserved > 0 && resp.StatusCode >= 400 {
		_, _ = globalCtx.AppDB().Exec(`UPDATE media_executor_grants SET reserved_bytes=MAX(0,reserved_bytes-?) WHERE token_hash=?`, reserved, hash)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		http.Error(w, "transfer response unreadable", 502)
		return
	}
	if init && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var out struct {
			UploadID string `json:"upload_id"`
		}
		if json.Unmarshal(raw, &out) != nil {
			http.Error(w, "invalid upload response", 502)
			return
		}
		if out.UploadID != "" {
			newKind := "uploads"
			if tail == "/files/init" {
				newKind = "files"
			}
			if !transferID.MatchString(out.UploadID) {
				http.Error(w, "invalid upload identity", 502)
				return
			}
			if _, err := globalCtx.AppDB().Exec(`INSERT OR IGNORE INTO media_executor_uploads(token_hash,kind,upload_id) VALUES(?,?,?)`, hash, newKind, out.UploadID); err != nil {
				http.Error(w, "upload receipt unavailable", 503)
				return
			}
		}
	}
	for _, key := range []string{"Content-Type", "Retry-After"} {
		if v := resp.Header.Get(key); v != "" {
			w.Header().Set(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}
func positivePart(s string) bool {
	if len(s) > 6 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != "" && s != "0"
}
func allowedTransferFolder(value any, allowed []string) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}
	for _, v := range allowed {
		if "/"+strings.Trim(s, "/")+"/" == "/"+strings.Trim(v, "/")+"/" {
			return true
		}
	}
	return false
}
