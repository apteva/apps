package main

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
)

// Bounded source cache; ctime prevents a same-size rewrite with restored mtime
// from reusing a stale read receipt. Both Linux and Darwin stat layouts work.
type cachedSource struct {
	revision string
	body     []byte
	sha      string
	lines    []string
}

var sourcePageCache = struct {
	sync.Mutex
	entries map[string]cachedSource
	bytes   int
}{entries: map[string]cachedSource{}}

func fileRevision(info os.FileInfo) string {
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	extra := ""
	if value.IsValid() && value.Kind() == reflect.Struct {
		for _, field := range []string{"Dev", "Ino", "Ctim", "Ctimespec"} {
			f := value.FieldByName(field)
			if f.IsValid() {
				extra += fmt.Sprint(f.Interface())
			}
		}
	}
	return fmt.Sprintf("%d:%d:%s", info.Size(), info.ModTime().UnixNano(), extra)
}
func cachedReadPage(f *os.File, full, rel string, info os.FileInfo, offset, limit int) (*ReadResult, error) {
	revision := fileRevision(info)
	sourcePageCache.Lock()
	cached, ok := sourcePageCache.entries[full]
	sourcePageCache.Unlock()
	if !ok || cached.revision != revision {
		body, err := io.ReadAll(io.LimitReader(f, maxFileBytes()+1))
		if err != nil {
			return nil, err
		}
		after, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > maxFileBytes() || fileRevision(after) != revision {
			return nil, errRevisionConflict
		}
		lines := strings.Split(string(body), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		cached = cachedSource{revision: revision, body: body, sha: shaOf(body), lines: lines}
		cost := len(body)*2 + len(lines)*16
		if cost <= 4<<20 {
			sourcePageCache.Lock()
			if old, ok := sourcePageCache.entries[full]; ok {
				sourcePageCache.bytes -= len(old.body)*2 + len(old.lines)*16
				delete(sourcePageCache.entries, full)
			}
			for key, old := range sourcePageCache.entries {
				if sourcePageCache.bytes+cost <= 16<<20 && len(sourcePageCache.entries) < 128 {
					break
				}
				sourcePageCache.bytes -= len(old.body)*2 + len(old.lines)*16
				delete(sourcePageCache.entries, key)
			}
			sourcePageCache.entries[full] = cached
			sourcePageCache.bytes += cost
			sourcePageCache.Unlock()
		}
	}
	return renderReadLines(rel, cached.lines, int64(len(cached.body)), cached.sha, offset, limit), nil
}
