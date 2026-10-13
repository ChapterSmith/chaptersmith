package gui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type MediaRef struct {
	ID  string
	URL string
}
type MediaRoots struct {
	PreviewDir string
	CoverDirs  []string
}
type mediaRoot struct {
	path       string
	handle     *os.Root
	disposable bool
}

// mediaStream is content produced while it is read, such as a chapter decoded
// on demand. Interrupt makes reads in progress return promptly.
type mediaStream interface {
	io.ReaderAt
	io.Closer
	Size() int64
	Interrupt()
}

type mediaEntry struct {
	stream            mediaStream
	file              *os.File
	stat              os.FileInfo
	root              *mediaRoot
	name, contentType string
	readers           int
	released          bool
	ownership         *mediaOwnership
}

// mediaOwnership records filesystem identities, never permission to remove a
// path merely because its basename resembles a generated preview. Directory
// removal is non-recursive and restricted to the renderer's direct child stage.
type mediaOwnership struct {
	root          *os.Root
	name          string
	file          os.FileInfo
	directory     string
	directoryInfo os.FileInfo
}

func captureMediaOwnership(root *os.Root, name string, file os.FileInfo) (*mediaOwnership, error) {
	owned := &mediaOwnership{root: root, name: name, file: file}
	directory := filepath.Dir(name)
	if filepath.Base(name) == "preview.m4a" && filepath.Dir(directory) == "." &&
		strings.HasPrefix(directory, ".book-preview-") && directory != ".book-preview-" {
		info, err := root.Lstat(directory)
		if err != nil {
			return nil, fmt.Errorf("inspecting preview directory: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("preview directory is not an ordinary directory")
		}
		owned.directory, owned.directoryInfo = directory, info
	}
	return owned, nil
}

// ownRenderedPreview gives the service an independent cleanup handle until
// Register succeeds, including when the registry itself rejects registration.
func ownRenderedPreview(cacheDir, path string) (*mediaOwnership, error) {
	cache, err := filepath.EvalSymlinks(cacheDir)
	if err != nil {
		return nil, err
	}
	cache, err = filepath.Abs(cache)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return nil, err
	}
	name, inside := contained(cache, canonical)
	if !inside {
		return nil, fmt.Errorf("renderer output is outside preview cache")
	}
	root, err := os.OpenRoot(cache)
	if err != nil {
		return nil, err
	}
	info, err := root.Lstat(name)
	if err != nil {
		root.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		root.Close()
		return nil, fmt.Errorf("renderer output is not an ordinary file")
	}
	owned, err := captureMediaOwnership(root, name, info)
	if err != nil {
		root.Close()
		return nil, err
	}
	if owned.directory == "" {
		root.Close()
		return nil, fmt.Errorf("renderer output is not in a private preview stage")
	}
	return owned, nil
}

func (owned *mediaOwnership) remove() error {
	// A replaced parent must not let cleanup touch the replacement's files or
	// remove even an empty foreign directory. A missing stage is already clean.
	if owned.directory != "" {
		current, err := owned.root.Lstat(owned.directory)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspecting owned preview directory: %w", err)
		}
		if !os.SameFile(current, owned.directoryInfo) {
			return fmt.Errorf("owned preview directory was replaced")
		}
	}
	if err := removeOwnedMediaPath(owned.root, owned.name, owned.file); err != nil {
		return err
	}
	if owned.directory != "" {
		return removeOwnedMediaPath(owned.root, owned.directory, owned.directoryInfo)
	}
	return nil
}

func removeOwnedMediaPath(root *os.Root, name string, expected os.FileInfo) error {
	current, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting owned media %q: %w", name, err)
	}
	if !os.SameFile(current, expected) {
		return fmt.Errorf("owned media %q was replaced", name)
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing owned media %q: %w", name, err)
	}
	return nil
}

// MediaRegistry is Go-only. Tokens refer to opened files, never frontend paths.
type MediaRegistry struct {
	mu        sync.Mutex
	roots     []*mediaRoot
	entries   map[string]*mediaEntry
	live      map[*mediaEntry]struct{}
	closed    bool
	reads     sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func contained(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	return rel, err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}
func NewMediaRegistry(roots MediaRoots) (*MediaRegistry, error) {
	r := &MediaRegistry{entries: make(map[string]*mediaEntry), live: make(map[*mediaEntry]struct{})}
	paths := append([]string{roots.PreviewDir}, roots.CoverDirs...)
	for i, path := range paths {
		if path == "" {
			r.Close()
			return nil, fmt.Errorf("media root is required")
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			r.Close()
			return nil, err
		}
		canonical, err := filepath.EvalSymlinks(abs)
		if err != nil {
			r.Close()
			return nil, err
		}
		for _, existing := range r.roots {
			_, inside := contained(existing.path, canonical)
			_, outside := contained(canonical, existing.path)
			if existing.path == canonical || inside || outside {
				r.Close()
				return nil, fmt.Errorf("media roots must be disjoint")
			}
		}
		handle, err := os.OpenRoot(canonical)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.roots = append(r.roots, &mediaRoot{canonical, handle, i == 0})
	}
	return r, nil
}
func (r *MediaRegistry) Register(path, contentType string) (MediaRef, error) {
	if contentType != "audio/mp4" && contentType != "image/png" && contentType != "image/jpeg" {
		return MediaRef{}, fmt.Errorf("unsupported media type")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return MediaRef{}, err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return MediaRef{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return MediaRef{}, fmt.Errorf("media registry is closed")
	}
	for _, root := range r.roots {
		name, ok := contained(root.path, canonical)
		if !ok {
			continue
		}
		// Root.Open also prevents a symlink swap from escaping between validation and open.
		before, err := root.handle.Stat(name)
		if err != nil {
			return MediaRef{}, err
		}
		if !before.Mode().IsRegular() {
			return MediaRef{}, fmt.Errorf("media must be an ordinary file")
		}
		var ownership *mediaOwnership
		if root.disposable {
			ownership, err = captureMediaOwnership(root.handle, name, before)
			if err != nil {
				return MediaRef{}, err
			}
		}
		file, err := root.handle.Open(name)
		if err != nil {
			return MediaRef{}, err
		}
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() || !os.SameFile(before, stat) {
			file.Close()
			return MediaRef{}, fmt.Errorf("media changed while opening")
		}
		for entry := range r.live {
			if root.disposable && os.SameFile(entry.stat, stat) {
				file.Close()
				return MediaRef{}, fmt.Errorf("media already registered")
			}
		}
		token := make([]byte, 24)
		if _, err = rand.Read(token); err != nil {
			file.Close()
			return MediaRef{}, err
		}
		id := hex.EncodeToString(token)
		entry := &mediaEntry{file: file, stat: stat, root: root, name: name, contentType: contentType, ownership: ownership}
		r.entries[id] = entry
		r.live[entry] = struct{}{}
		return MediaRef{ID: id, URL: "/_media/" + id}, nil
	}
	return MediaRef{}, fmt.Errorf("media is outside managed roots")
}

// RegisterStream serves generated audio under a token. The registry owns the
// stream from a successful call until its token is released.
func (r *MediaRegistry) RegisterStream(stream mediaStream, contentType string) (MediaRef, error) {
	if contentType != "audio/wav" {
		return MediaRef{}, fmt.Errorf("unsupported media type")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return MediaRef{}, fmt.Errorf("media registry is closed")
	}
	token := make([]byte, 24)
	if _, err := rand.Read(token); err != nil {
		return MediaRef{}, err
	}
	id := hex.EncodeToString(token)
	entry := &mediaEntry{stream: stream, contentType: contentType}
	r.entries[id] = entry
	r.live[entry] = struct{}{}
	return MediaRef{ID: id, URL: "/_media/" + id}, nil
}
func (r *MediaRegistry) disposeLocked(e *mediaEntry) error {
	delete(r.live, e)
	if e.stream != nil {
		err := e.stream.Close()
		r.closeErr = errors.Join(r.closeErr, err)
		return err
	}
	err := e.file.Close()
	if e.ownership != nil {
		err = errors.Join(err, e.ownership.remove())
	}
	// Release may finish before an active reader. Retain disposal errors from
	// either path so the later shutdown cannot falsely report clean completion.
	r.closeErr = errors.Join(r.closeErr, err)
	return err
}
func (r *MediaRegistry) Release(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[id]
	if !ok {
		return nil
	}
	delete(r.entries, id)
	e.released = true
	if e.readers == 0 {
		return r.disposeLocked(e)
	}
	if e.stream != nil {
		// A slow decode must not outlive the player that asked for it.
		e.stream.Interrupt()
	}
	return nil
}
func (r *MediaRegistry) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		for id, e := range r.entries {
			delete(r.entries, id)
			e.released = true
			if e.readers == 0 {
				r.disposeLocked(e)
			} else if e.stream != nil {
				e.stream.Interrupt()
			}
		}
		r.mu.Unlock()
		r.reads.Wait()
		for _, root := range r.roots {
			r.closeErr = errors.Join(r.closeErr, root.handle.Close())
		}
	})
	return r.closeErr
}
func (r *MediaRegistry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if q.URL.Path != "/_media" && !strings.HasPrefix(q.URL.Path, "/_media/") {
			next.ServeHTTP(w, q)
			return
		}
		if q.Method != "GET" && q.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(q.URL.Path, "/_media/")
		decoded, err := hex.DecodeString(id)
		if err != nil || len(decoded) != 24 || q.URL.RawQuery != "" || q.URL.RawPath != "" {
			http.NotFound(w, q)
			return
		}
		r.mu.Lock()
		e, ok := r.entries[id]
		if !ok || r.closed {
			r.mu.Unlock()
			http.NotFound(w, q)
			return
		}
		e.readers++
		r.reads.Add(1)
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			e.readers--
			if e.released && e.readers == 0 {
				r.disposeLocked(e)
			}
			r.mu.Unlock()
			r.reads.Done()
		}()
		w.Header().Set("Content-Type", e.contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if e.stream != nil {
			http.ServeContent(w, q, "", time.Time{}, io.NewSectionReader(e.stream, 0, e.stream.Size()))
			return
		}
		http.ServeContent(w, q, "", e.stat.ModTime(), io.NewSectionReader(e.file, 0, e.stat.Size()))
	})
}
