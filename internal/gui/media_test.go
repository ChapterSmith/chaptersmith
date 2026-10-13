package gui

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func mediaFixture(t *testing.T) (*MediaRegistry, string, string) {
	t.Helper()
	root := t.TempDir()
	preview := filepath.Join(root, "preview")
	cover := filepath.Join(root, "covers")
	for _, p := range []string{preview, cover} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewMediaRegistry(MediaRoots{PreviewDir: preview, CoverDirs: []string{cover}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, preview, cover
}
func mediaFile(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "file.m4a")
	if err := os.WriteFile(p, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func request(h http.Handler, method, url, rng string) *httptest.ResponseRecorder {
	q := httptest.NewRequest(method, url, nil)
	if rng != "" {
		q.Header.Set("Range", rng)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, q)
	return w
}
func TestMediaRanges(t *testing.T) {
	r, p, _ := mediaFixture(t)
	ref, err := r.Register(mediaFile(t, p), "audio/mp4")
	if err != nil {
		t.Fatal(err)
	}
	h := r.Middleware(http.NotFoundHandler())
	w := request(h, "GET", ref.URL, "bytes=2-5")
	if w.Code != 206 || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" {
		t.Fatalf("range: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	w = request(h, "HEAD", ref.URL, "")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD: %+v", w)
	}
	if w = request(h, "GET", ref.URL, "bytes=20-30"); w.Code != 416 {
		t.Fatalf("range status %d", w.Code)
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(h, "GET", ref.URL, "bytes=6-9")
			if w.Code != 206 || w.Body.String() != "6789" {
				t.Errorf("parallel body %q", w.Body.String())
			}
		}()
	}
	wg.Wait()
}
func TestMediaRejectsPaths(t *testing.T) {
	r, p, c := mediaFixture(t)
	path := mediaFile(t, p)
	ref, err := r.Register(path, "audio/mp4")
	if err != nil {
		t.Fatal(err)
	}
	outside := mediaFile(t, t.TempDir())
	link := filepath.Join(c, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	for _, path := range []string{outside, link, c} {
		if _, err := r.Register(path, "image/png"); err == nil {
			t.Fatalf("registered %s", path)
		}
	}
	h := r.Middleware(http.NotFoundHandler())
	for _, url := range []string{"/_media/unknown", "/_media/../file", "/_media/%2e%2e/file", ref.URL + "?path=" + outside} {
		if w := request(h, "GET", url, ""); w.Code != 404 {
			t.Fatalf("%s exposed %d %q", url, w.Code, w.Body.String())
		}
	}
	if w := request(h, "POST", ref.URL, ""); w.Code != 405 {
		t.Fatalf("POST %d", w.Code)
	}
	if err := r.Release(ref.ID); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "GET", ref.URL, ""); w.Code != 404 {
		t.Fatal("released token available")
	}
}

type heldWriter struct {
	*httptest.ResponseRecorder
	entered, resume chan struct{}
}

func (w *heldWriter) Write(p []byte) (int, error) {
	select {
	case <-w.entered:
	default:
		close(w.entered)
	}
	<-w.resume
	return w.ResponseRecorder.Write(p)
}
func TestMediaReleaseDuringRead(t *testing.T) {
	r, p, c := mediaFixture(t)
	path := mediaFile(t, p)
	cover := mediaFile(t, c)
	ref, _ := r.Register(path, "audio/mp4")
	cref, _ := r.Register(cover, "image/png")
	h := r.Middleware(http.NotFoundHandler())
	w := &heldWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, httptest.NewRequest("GET", ref.URL, nil)); close(done) }()
	<-w.entered
	r.Release(ref.ID)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active file removed")
	}
	if request(h, "GET", ref.URL, "").Code != 404 {
		t.Fatal("released ID served")
	}
	close(w.resume)
	<-done
	if !bytes.Equal(w.Body.Bytes(), []byte("0123456789")) {
		t.Fatal("active reader interrupted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview retained")
	}
	r.Release(cref.ID)
	if data, err := os.ReadFile(cover); err != nil || string(data) != "0123456789" {
		t.Fatal("cover removed")
	}
}

var _ io.Writer = (*heldWriter)(nil)

// Close must retain both root and file handles until the active response drains.
func TestMediaCloseWaitsForRead(t *testing.T) {
	r, p, c := mediaFixture(t)
	preview := mediaFile(t, p)
	cover := mediaFile(t, c)
	ref, _ := r.Register(preview, "audio/mp4")
	r.Register(cover, "image/png")
	h := r.Middleware(http.NotFoundHandler())
	w := &heldWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	readDone := make(chan struct{})
	go func() { h.ServeHTTP(w, httptest.NewRequest("GET", ref.URL, nil)); close(readDone) }()
	<-w.entered
	closed := make(chan error)
	go func() { closed <- r.Close() }()
	// Once closed is visible under the registry lock, no new request may acquire it.
	for {
		r.mu.Lock()
		closing := r.closed
		r.mu.Unlock()
		if closing {
			break
		}
		runtime.Gosched()
	}
	if request(h, "GET", ref.URL, "").Code != 404 {
		t.Fatal("closing registry accepted read")
	}
	select {
	case <-closed:
		t.Fatal("close did not join reader")
	default:
	}
	close(w.resume)
	<-readDone
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if w.Body.String() != "0123456789" {
		t.Fatal("read interrupted")
	}
	if _, err := os.Stat(cover); err != nil {
		t.Fatal("cover deleted")
	}
}
func TestMediaRootsMustBeDisjoint(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	os.Mkdir(nested, 0700)
	if r, err := NewMediaRegistry(MediaRoots{PreviewDir: root, CoverDirs: []string{nested}}); err == nil {
		r.Close()
		t.Fatal("overlapping roots accepted")
	}
}

func TestMediaPreviewCannotBeRegisteredWhileRetiring(t *testing.T) {
	r, p, _ := mediaFixture(t)
	path := mediaFile(t, p)
	ref, _ := r.Register(path, "audio/mp4")
	w := &heldWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() {
		r.Middleware(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", ref.URL, nil))
		close(done)
	}()
	<-w.entered
	r.Release(ref.ID)
	_, err := r.Register(path, "audio/mp4")
	close(w.resume)
	<-done
	if err == nil {
		t.Fatal("registered a preview whose retiring reader can delete it")
	}
}

// Mirrors render.Preview's ownership contract, including its private parent.
func renderedPreview(t *testing.T, cache string) string {
	t.Helper()
	dir, err := os.MkdirTemp(cache, ".book-preview-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "preview.m4a")
	if err := os.WriteFile(path, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMediaOwnedPreviewDirectoryLifecycle(t *testing.T) {
	for _, mode := range []string{"release", "close", "active-read"} {
		t.Run(mode, func(t *testing.T) {
			r, cache, _ := mediaFixture(t)
			path := renderedPreview(t, cache)
			ref, err := r.Register(path, "audio/mp4")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "active-read" {
				w := &heldWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
				done := make(chan struct{})
				go func() {
					r.Middleware(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", ref.URL, nil))
					close(done)
				}()
				<-w.entered
				if err := r.Release(ref.ID); err != nil {
					t.Error(err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Error("removed active preview", err)
				}
				close(w.resume)
				<-done
			} else if mode == "close" {
				err = r.Close()
			} else {
				err = r.Release(ref.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("owned stage remains: %v", err)
			}
			if _, err := os.Stat(cache); err != nil {
				t.Fatal("removed preview root", err)
			}
		})
	}
}

func TestMediaDeferredCleanupErrorSurvivesUntilClose(t *testing.T) {
	r, cache, _ := mediaFixture(t)
	ref, err := r.Register(renderedPreview(t, cache), "audio/mp4")
	if err != nil {
		t.Fatal(err)
	}
	w := &heldWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	done := make(chan struct{})
	go func() {
		r.Middleware(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", ref.URL, nil))
		close(done)
	}()
	<-w.entered
	// The request has copied all ten bytes into the blocked writer. Force a real
	// descriptor-close failure when disposal runs after the response drains.
	r.mu.Lock()
	entry := r.entries[ref.ID]
	r.mu.Unlock()
	if err := entry.file.Close(); err != nil {
		t.Error(err)
	}
	if err := r.Release(ref.ID); err != nil {
		t.Error(err)
	}
	close(w.resume)
	<-done
	if err := r.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("deferred cleanup error lost: %v", err)
	}
	if err := r.Close(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("repeat Close lost error: %v", err)
	}
}

func TestMediaCleanupStatErrorAndMissingFile(t *testing.T) {
	t.Run("genuine-stat-error", func(t *testing.T) {
		r, cache, _ := mediaFixture(t)
		ref, err := r.Register(renderedPreview(t, cache), "audio/mp4")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.roots[0].handle.Close(); err != nil {
			t.Fatal(err)
		}
		if err := r.Release(ref.ID); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("unexpected stat error ignored: %v", err)
		}
	})
	t.Run("already-absent", func(t *testing.T) {
		r, cache, _ := mediaFixture(t)
		path := renderedPreview(t, cache)
		ref, err := r.Register(path, "audio/mp4")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := r.Release(ref.ID); err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestMediaCleanupPreservesForeignDirectories(t *testing.T) {
	t.Run("ordinary-parent", func(t *testing.T) {
		r, cache, _ := mediaFixture(t)
		dir := filepath.Join(cache, "foreign")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		ref, err := r.Register(mediaFile(t, dir), "audio/mp4")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Release(ref.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatal("removed foreign directory", err)
		}
	})
	t.Run("foreign-child", func(t *testing.T) {
		r, cache, _ := mediaFixture(t)
		path := renderedPreview(t, cache)
		ref, err := r.Register(path, "audio/mp4")
		if err != nil {
			t.Fatal(err)
		}
		foreign := filepath.Join(filepath.Dir(path), "foreign.txt")
		if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := r.Release(ref.ID); err == nil {
			t.Fatal("failed empty-directory cleanup was hidden")
		}
		if err := r.Close(); err == nil {
			t.Fatal("cleanup error was forgotten")
		}
		if data, err := os.ReadFile(foreign); err != nil || string(data) != "keep" {
			t.Fatal("foreign bytes removed")
		}
	})
	t.Run("replaced-parent", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows refuses to rename a directory that holds open files")
		}
		r, cache, _ := mediaFixture(t)
		path := renderedPreview(t, cache)
		ref, err := r.Register(path, "audio/mp4")
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(path)
		if err := os.Rename(dir, dir+"-moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := r.Release(ref.ID); err == nil {
			t.Fatal("directory identity change was hidden")
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatal("replacement directory removed")
		}
		if data, err := os.ReadFile(filepath.Join(dir+"-moved", "preview.m4a")); err != nil || string(data) != "0123456789" {
			t.Fatal("moved file touched")
		}
	})
}
