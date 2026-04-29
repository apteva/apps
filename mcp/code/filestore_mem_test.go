package main

import (
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
)

// memFileStore is an in-memory FileStore used by tests so we don't
// touch /tmp or the real /data/repos. Same semantics as LocalFileStore
// — atomic writes, idempotent deletes, sorted listings.

type memFileStore struct {
	mu    sync.Mutex
	files map[string]map[string][]byte // slug -> path -> content
}

func newMemFileStore() *memFileStore {
	return &memFileStore{files: map[string]map[string][]byte{}}
}

func (m *memFileStore) CreateRepo(slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.files[slug]; !ok {
		m.files[slug] = map[string][]byte{}
	}
	return nil
}

func (m *memFileStore) DropRepo(slug string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, slug)
	return nil
}

func (m *memFileStore) Read(slug, p string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.files[slug]
	if repo == nil {
		return nil, os.ErrNotExist
	}
	b, ok := repo[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

func (m *memFileStore) Write(slug, p string, content []byte) (FileMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.files[slug]
	if repo == nil {
		repo = map[string][]byte{}
		m.files[slug] = repo
	}
	saved := make([]byte, len(content))
	copy(saved, content)
	repo[p] = saved
	return FileMeta{Path: p, Size: int64(len(content)), SHA256: shaOf(content)}, nil
}

func (m *memFileStore) Delete(slug, p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if repo := m.files[slug]; repo != nil {
		delete(repo, p)
	}
	return nil
}

func (m *memFileStore) DeleteTree(slug, prefix string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.files[slug]
	if repo == nil {
		return nil
	}
	if prefix == "" {
		m.files[slug] = map[string][]byte{}
		return nil
	}
	for p := range repo {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			delete(repo, p)
		}
	}
	return nil
}

func (m *memFileStore) Move(slug, src, dst string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.files[slug]
	if repo == nil {
		return nil, os.ErrNotExist
	}
	moved := []string{}
	if body, ok := repo[src]; ok {
		repo[dst] = body
		delete(repo, src)
		moved = append(moved, dst)
		return moved, nil
	}
	// Folder move.
	prefix := src + "/"
	for p, body := range repo {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		newPath := dst + "/" + strings.TrimPrefix(p, prefix)
		repo[newPath] = body
		delete(repo, p)
		moved = append(moved, newPath)
	}
	if len(moved) == 0 {
		return nil, errors.New("source not found")
	}
	return moved, nil
}

func (m *memFileStore) List(slug, prefix string, recursive bool) ([]FileMeta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	repo := m.files[slug]
	if repo == nil {
		return []FileMeta{}, nil
	}
	out := []FileMeta{}
	for p, body := range repo {
		if prefix != "" {
			if !(p == prefix || strings.HasPrefix(p, prefix+"/")) {
				continue
			}
		}
		if !recursive && prefix == "" {
			if strings.Contains(p, "/") {
				continue
			}
		}
		out = append(out, FileMeta{Path: p, Size: int64(len(body))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *memFileStore) Stat(slug, p string) (FileMeta, error) {
	body, err := m.Read(slug, p)
	if err != nil {
		return FileMeta{}, err
	}
	return FileMeta{Path: p, Size: int64(len(body)), SHA256: shaOf(body)}, nil
}

func (m *memFileStore) TotalSize(slug string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var total int64
	for _, body := range m.files[slug] {
		total += int64(len(body))
	}
	return total, nil
}

// ensure interface compliance
var _ FileStore = (*memFileStore)(nil)
