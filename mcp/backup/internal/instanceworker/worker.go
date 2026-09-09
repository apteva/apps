// Package instanceworker implements Backup's standalone remote folder worker.
package instanceworker

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const Lifetime = 2 * time.Hour
const Format = "apteva-instance-folders-v1"

type Config struct {
	OperationID  string   `json:"operation_id"`
	Token        string   `json:"token"`
	Kind         string   `json:"kind"`
	Identity     string   `json:"instance_identity"`
	Paths        []string `json:"paths"`
	TargetPath   string   `json:"target_path"`
	SHA256       string   `json:"sha256"`
	ArchiveBytes int64    `json:"archive_bytes"`
}
type State struct {
	Stage          string   `json:"stage"`
	Error          string   `json:"error,omitempty"`
	Files          int64    `json:"files"`
	Bytes          int64    `json:"bytes"`
	ArchiveBytes   int64    `json:"archive_bytes,omitempty"`
	SHA256         string   `json:"sha256,omitempty"`
	TargetPath     string   `json:"target_path,omitempty"`
	SourcePaths    []string `json:"source_paths,omitempty"`
	PartialFailure bool     `json:"partial_failure"`
}
type Manifest struct {
	Format    string   `json:"format"`
	Identity  string   `json:"instance_identity"`
	Paths     []string `json:"paths"`
	OS        string   `json:"os"`
	Method    string   `json:"method"`
	Metadata  []string `json:"metadata"`
	Ownership string   `json:"ownership"`
}
type Worker struct {
	Config    Config
	Dir       string
	mu        sync.Mutex
	operation sync.Mutex
	state     State
}

func ValidPath(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" || len(p) > 4096 {
		return errors.New("choose a clean absolute non-root folder path")
	}
	for _, r := range p {
		if r < 32 {
			return errors.New("folder paths must not contain control characters")
		}
	}
	for _, v := range []string{"/dev", "/proc", "/sys"} {
		if p == v || strings.HasPrefix(p, v+"/") {
			return errors.New("virtual filesystem folders are unsupported")
		}
	}
	return nil
}

// OpenDirectory anchors every component with O_NOFOLLOW, avoiding symlink races.
func OpenDirectory(p string) (*os.File, error) {
	if !filepath.IsAbs(p) {
		return nil, errors.New("absolute folder required")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(p), "/"), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), p), nil
}
func Probe(paths []string) (map[string]any, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, errors.New("only macOS and Linux are supported")
	}
	for _, p := range paths {
		if e := ValidPath(p); e != nil {
			return nil, e
		}
		f, e := OpenDirectory(p)
		if e != nil {
			return nil, fmt.Errorf("source folder %q: %w", p, e)
		}
		f.Close()
	}
	return map[string]any{"supported": true, "os": runtime.GOOS, "arch": runtime.GOARCH, "method": "folders", "metadata": []string{"file contents", "directories", "POSIX permissions (no special bits)", "modification times"}, "limitations": []string{"no symlinks or special files", "no ACLs or extended attributes", "ownership becomes the SSH user", "files must remain unchanged during capture", "not full-machine recovery"}}, nil
}
func AtomicJSON(file string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(file+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	if closeErr := f.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	return os.Rename(file+".tmp", file)
}
func HashFile(file string) (string, error) {
	f, e := os.Open(file)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func New(config Config, dir string) (*Worker, error) {
	if len(config.OperationID) != 48 || len(config.Token) != 48 {
		return nil, errors.New("invalid operation credentials")
	}
	if _, e := hex.DecodeString(config.OperationID); e != nil {
		return nil, e
	}
	if config.Kind != "backup" && config.Kind != "restore" {
		return nil, errors.New("invalid operation kind")
	}
	w := &Worker{Config: config, Dir: dir, state: State{Stage: "pending"}}
	if b, e := os.ReadFile(filepath.Join(dir, "state.json")); e == nil {
		if e = json.Unmarshal(b, &w.state); e != nil {
			return nil, e
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	return w, nil
}
func (w *Worker) Snapshot() State { w.mu.Lock(); defer w.mu.Unlock(); return w.state }
func (w *Worker) update(f func(*State)) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	next := w.state
	f(&next)
	if e := AtomicJSON(filepath.Join(w.Dir, "state.json"), next); e != nil {
		return e
	}
	w.state = next
	return nil
}
func (w *Worker) fail(e error) {
	_ = w.update(func(s *State) { s.Stage = "failed"; s.Error = e.Error() })
}
func (w *Worker) archive() string { return filepath.Join(w.Dir, "archive.tar.gz") }
func (w *Worker) Capture(ctx context.Context) error {
	w.operation.Lock()
	defer w.operation.Unlock()
	if w.Snapshot().Stage == "ready" {
		if _, e := os.Stat(w.archive()); e == nil {
			return nil
		}
	}
	if _, e := Probe(w.Config.Paths); e != nil {
		return e
	}
	if len(w.Config.Paths) == 0 || len(w.Config.Paths) > 32 {
		return errors.New("choose 1–32 source folders")
	}
	if e := w.update(func(s *State) { *s = State{Stage: "capturing"} }); e != nil {
		return e
	}
	f, e := os.OpenFile(w.archive()+".partial", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	manifest := Manifest{Format: Format, Identity: w.Config.Identity, Paths: w.Config.Paths, OS: runtime.GOOS, Method: "folders", Metadata: []string{"mode", "mtime"}, Ownership: "restoring SSH user"}
	b, _ := json.Marshal(manifest)
	if e = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); e != nil {
		return e
	}
	if _, e = tw.Write(b); e != nil {
		return e
	}
	var count, bytes int64
	own, e := os.Stat(w.Dir)
	if e != nil {
		return e
	}
	var add func(*os.File, string) error
	add = func(dir *os.File, name string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		before, e := dir.Stat()
		if e != nil {
			return e
		}
		if os.SameFile(before, own) {
			return errors.New("source contains Backup working directory")
		}
		header, e := tar.FileInfoHeader(before, "")
		if e != nil {
			return e
		}
		header.Name = name
		header.Mode = int64(before.Mode().Perm())
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		if e = tw.WriteHeader(header); e != nil {
			return e
		}
		entries, e := dir.ReadDir(-1)
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if e = ctx.Err(); e != nil {
				return e
			}
			info, e := entry.Info()
			if e != nil {
				return e
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("symlinks and special files are unsupported: %s", entry.Name())
			}
			flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
			if info.IsDir() {
				flags |= unix.O_DIRECTORY
			}
			fd, e := unix.Openat(int(dir.Fd()), entry.Name(), flags, 0)
			if e != nil {
				return e
			}
			file := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), entry.Name()))
			opened, e := file.Stat()
			if e != nil {
				file.Close()
				return e
			}
			if !os.SameFile(info, opened) {
				file.Close()
				return errors.New("source changed during capture")
			}
			if info.IsDir() {
				e = add(file, name+"/"+entry.Name())
				file.Close()
				if e != nil {
					return e
				}
				continue
			}
			h, e := tar.FileInfoHeader(info, "")
			if e != nil {
				file.Close()
				return e
			}
			h.Name = name + "/" + entry.Name()
			h.Mode = int64(info.Mode().Perm())
			h.Uid = 0
			h.Gid = 0
			h.Uname = ""
			h.Gname = ""
			if e = tw.WriteHeader(h); e == nil {
				_, e = io.CopyN(tw, contextReader{ctx, file}, info.Size())
			}
			after, statErr := file.Stat()
			file.Close()
			if e != nil {
				return e
			}
			if statErr != nil {
				return statErr
			}
			if after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
				return fmt.Errorf("source changed during capture: %s", h.Name)
			}
			count++
			bytes += info.Size()
			if count%100 == 0 {
				if e = w.update(func(s *State) { s.Files = count; s.Bytes = bytes }); e != nil {
					return e
				}
			}
		}
		after, e := dir.Stat()
		if e != nil {
			return e
		}
		if !after.ModTime().Equal(before.ModTime()) {
			return errors.New("directory changed during capture")
		}
		return nil
	}
	for i, p := range w.Config.Paths {
		dir, e := OpenDirectory(p)
		if e != nil {
			return e
		}
		e = add(dir, "data/"+strconv.Itoa(i))
		dir.Close()
		if e != nil {
			return e
		}
	}
	if e = tw.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(w.archive()+".partial", w.archive()); e != nil {
		return e
	}
	digest, e := HashFile(w.archive())
	if e != nil {
		return e
	}
	info, e := os.Stat(w.archive())
	if e != nil {
		return e
	}
	return w.update(func(s *State) {
		s.Stage = "ready"
		s.SHA256 = digest
		s.ArchiveBytes = info.Size()
		s.Files = count
		s.Bytes = bytes
	})
}
func (w *Worker) Restore(ctx context.Context) error {
	w.operation.Lock()
	defer w.operation.Unlock()
	if w.Snapshot().Stage == "restored" {
		return nil
	}
	if e := ValidPath(w.Config.TargetPath); e != nil {
		return e
	}
	parent, e := OpenDirectory(filepath.Dir(w.Config.TargetPath))
	if e != nil {
		return e
	}
	defer parent.Close()
	// Root stays anchored even if the selected parent is renamed during extraction.
	root, e := os.OpenRoot(filepath.Dir(w.Config.TargetPath))
	if e != nil {
		return e
	}
	defer root.Close()
	check, e := root.Stat(".")
	if e != nil {
		return e
	}
	parentInfo, e := parent.Stat()
	if e != nil {
		return e
	}
	if !os.SameFile(check, parentInfo) {
		return errors.New("restore parent changed")
	}
	target := filepath.Base(w.Config.TargetPath)
	stage := ".apteva-restore-" + w.Config.OperationID
	if targetInfo, statErr := root.Lstat(target); statErr == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("restore target already exists as a symlink")
		}
		// Receipt reconciles a completed rename after interruption.
		f, e := root.Open(target + "/.apteva-recovery.json")
		if e == nil {
			var receipt struct {
				ID string `json:"operation_id"`
			}
			e = json.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&receipt)
			f.Close()
			if e == nil && receipt.ID == w.Config.OperationID {
				return w.update(func(s *State) { s.Stage = "restored"; s.TargetPath = w.Config.TargetPath })
			}
		}
		return errors.New("restore target already exists; choose a new directory")
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	digest, e := HashFile(w.archive())
	if e != nil {
		return e
	}
	if digest != w.Config.SHA256 {
		return errors.New("archive integrity mismatch")
	}
	if e = w.update(func(s *State) { s.Stage = "restoring"; s.Error = "" }); e != nil {
		return e
	}
	if info, e := root.Lstat(stage); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe staging directory")
	}
	if e = root.RemoveAll(stage); e != nil {
		return e
	}
	if e = root.Mkdir(stage, 0700); e != nil {
		return e
	}
	defer root.RemoveAll(stage)
	staging, e := root.OpenRoot(stage)
	if e != nil {
		return e
	}
	defer staging.Close()
	file, e := os.Open(w.archive())
	if e != nil {
		return e
	}
	defer file.Close()
	gz, e := gzip.NewReader(file)
	if e != nil {
		return e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	first, e := tr.Next()
	if e != nil || first.Name != "manifest.json" || first.Typeflag != tar.TypeReg || first.Size > 4<<20 {
		return errors.New("invalid instance manifest")
	}
	var manifest Manifest
	if e = json.NewDecoder(io.LimitReader(tr, 4<<20)).Decode(&manifest); e != nil {
		return e
	}
	if manifest.Format != Format || manifest.Identity != w.Config.Identity || len(manifest.Paths) == 0 || len(manifest.Paths) > 32 {
		return errors.New("archive source identity does not match recovery point")
	}
	seen := map[string]bool{}
	var directories []*tar.Header
	var count int64
	for {
		if e = ctx.Err(); e != nil {
			return e
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := strings.TrimSuffix(h.Name, "/")
		parts := strings.Split(name, "/")
		if len(parts) < 2 || parts[0] != "data" || path.Clean(name) != name || seen[name] {
			return errors.New("unsafe or duplicate archive path")
		}
		index, e := strconv.Atoi(parts[1])
		if e != nil || index < 0 || index >= len(manifest.Paths) || strconv.Itoa(index) != parts[1] {
			return errors.New("unsafe archive source index")
		}
		if h.Typeflag != tar.TypeDir && h.Typeflag != tar.TypeReg {
			return errors.New("unsupported archive member")
		}
		seen[name] = true
		relative := strings.Join(parts[1:], "/")
		if e = staging.MkdirAll(path.Dir(relative), 0700); e != nil {
			return e
		}
		if h.Typeflag == tar.TypeDir {
			if e = staging.MkdirAll(relative, 0700); e != nil {
				return e
			}
			copy := *h
			copy.Name = relative
			directories = append(directories, &copy)
		} else {
			dst, e := staging.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			_, e = io.Copy(dst, contextReader{ctx, tr})
			if e == nil {
				e = dst.Sync()
			}
			if e == nil {
				e = dst.Chmod(os.FileMode(h.Mode) & 0777)
			}
			dst.Close()
			if e != nil {
				return e
			}
			if e = staging.Chtimes(relative, h.ModTime, h.ModTime); e != nil {
				return e
			}
			count++
		}
		if count%100 == 0 {
			if e = w.update(func(s *State) { s.Files = count }); e != nil {
				return e
			}
		}
	}
	if _, e = io.Copy(io.Discard, gz); e != nil {
		return e
	}
	receipt, _ := json.Marshal(map[string]any{"operation_id": w.Config.OperationID, "sources": manifest.Paths})
	rf, e := staging.OpenFile(".apteva-recovery.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = rf.Write(receipt)
	if e == nil {
		e = rf.Sync()
	}
	rf.Close()
	if e != nil {
		return e
	}
	for i := len(directories) - 1; i >= 0; i-- {
		h := directories[i]
		if e = staging.Chmod(h.Name, os.FileMode(h.Mode)&0777); e != nil {
			return e
		}
		if e = staging.Chtimes(h.Name, h.ModTime, h.ModTime); e != nil {
			return e
		}
	}
	if e = renameExclusive(int(parent.Fd()), stage, target); e != nil {
		return fmt.Errorf("restore target activation: %w", e)
	}
	if e = parent.Sync(); e != nil {
		return e
	}
	return w.update(func(s *State) {
		s.Stage = "restored"
		s.Files = count
		s.TargetPath = w.Config.TargetPath
		s.SourcePaths = manifest.Paths
	})
}
func (w *Worker) Serve(ctx context.Context) error {
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return e
	}
	runCtx, cancel := context.WithTimeout(ctx, Lifetime)
	defer cancel()
	var jobs sync.WaitGroup
	var jobsMu sync.Mutex
	stopped := false
	start := func(fn func(context.Context) error) {
		jobsMu.Lock()
		if stopped {
			jobsMu.Unlock()
			return
		}
		jobs.Add(1)
		jobsMu.Unlock()
		go func() {
			defer jobs.Done()
			if e := fn(runCtx); e != nil {
				w.fail(e)
			}
		}()
	}
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second, ReadTimeout: Lifetime, WriteTimeout: Lifetime, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return runCtx }}
	server.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+w.Config.Token)) != 1 {
			http.Error(rw, "forbidden", 403)
			return
		}
		jsonReply := func(v any) { rw.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(rw).Encode(v) }
		switch {
		case r.Method == "GET" && r.URL.Path == "/status":
			jsonReply(w.Snapshot())
		case r.Method == "GET" && r.URL.Path == "/archive" && w.Snapshot().Stage == "ready":
			http.ServeFile(rw, r, w.archive())
		case r.Method == "POST" && r.URL.Path == "/cleanup":
			jsonReply(map[string]bool{"closed": true})
			cancel()
		case r.Method == "POST" && r.URL.Path == "/restore" && w.Config.Kind == "restore":
			stage := w.Snapshot().Stage
			if stage == "restored" || stage == "restoring" {
				jsonReply(w.Snapshot())
				return
			}
			if !w.operation.TryLock() {
				http.Error(rw, "operation busy", 409)
				return
			}
			e := func() error {
				defer w.operation.Unlock()
				if r.ContentLength < 0 || r.ContentLength != w.Config.ArchiveBytes {
					return errors.New("invalid archive size")
				}
				if e := w.update(func(s *State) { s.Stage = "receiving" }); e != nil {
					return e
				}
				f, e := os.OpenFile(w.archive()+".partial", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
				if e != nil {
					return e
				}
				h := sha256.New()
				n, e := io.Copy(io.MultiWriter(f, h), r.Body)
				if e == nil {
					e = f.Sync()
				}
				f.Close()
				if e != nil {
					return e
				}
				if n != w.Config.ArchiveBytes || hex.EncodeToString(h.Sum(nil)) != w.Config.SHA256 {
					return errors.New("archive integrity mismatch")
				}
				if e = os.Rename(w.archive()+".partial", w.archive()); e != nil {
					return e
				}
				return w.update(func(s *State) { s.Stage = "uploaded" })
			}()
			if e != nil {
				w.fail(e)
				http.Error(rw, "restore upload failed", 400)
				return
			}
			start(w.Restore)
			jsonReply(map[string]string{"stage": "restoring"})
		default:
			http.NotFound(rw, r)
		}
	})
	if e = AtomicJSON(filepath.Join(w.Dir, "endpoint.json"), map[string]int{"port": ln.Addr().(*net.TCPAddr).Port}); e != nil {
		ln.Close()
		return e
	}
	if w.Config.Kind == "backup" {
		start(w.Capture)
	} else if s := w.Snapshot().Stage; s == "uploaded" || s == "restoring" {
		start(w.Restore)
	}
	go func() { <-runCtx.Done(); _ = server.Close() }()
	e = server.Serve(ln)
	cancel()
	jobsMu.Lock()
	stopped = true
	jobsMu.Unlock()
	jobs.Wait()
	_ = os.RemoveAll(w.Dir)
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
