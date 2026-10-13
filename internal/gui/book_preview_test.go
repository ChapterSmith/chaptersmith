package gui

import (
	"chaptersmith/internal/book"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBookPreviewOwnership(t *testing.T) {
	s, _, p := newBookFixture(t)
	v := loadedBook(t, s)
	calls := 0
	s.deps.Preview = func(ctx context.Context, got book.BookProject, id, dir string) (string, error) {
		calls++
		if id != "c2" || got.ID != p.ID || dir != filepath.Join(s.cacheDir, "preview") {
			t.Fatal("wrong snapshot/chapter/cache")
		}
		got.Metadata.Extra["language"] = "mutation"
		return renderedPreview(t, dir), nil
	}
	result, err := s.PreviewChapter("preview", v.Revision, "c2")
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != v.Revision || result.ProjectID != p.ID || result.OriginUS != 10000000 || result.DurationUS != 10000000 || !strings.HasPrefix(result.URL, "/_media/") || len(result.MediaID) < 32 {
		t.Fatalf("bad result %+v", result)
	}
	current, _ := s.GetProject()
	if current.Project.Metadata.Extra["language"] != "pl" {
		t.Fatal("snapshot alias")
	}
	s.ReleaseMedia(result.MediaID)
	os.WriteFile(p.Sources[0].Path, []byte("changed"), 0600)
	if _, err = s.PreviewChapter("changed", v.Revision, "c2"); err == nil || calls != 1 {
		t.Fatal("changed source rendered")
	}
}
func TestBookPreviewCancellation(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	started := make(chan struct{})
	var path string
	s.deps.Preview = func(ctx context.Context, p book.BookProject, id, dir string) (string, error) {
		path = renderedPreview(t, dir)
		close(started)
		<-ctx.Done()
		return path, nil
	}
	done := make(chan error)
	go func() { _, err := s.PreviewChapter("cancel", v.Revision, "c1"); done <- err }()
	<-started
	s.CancelOperation("cancel")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("cancel leaked preview")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("cancel leaked preview directory")
	}
}

func TestBookCoverMediaUndo(t *testing.T) {
	s, _, p := newBookFixture(t)
	v := loadedBook(t, s)
	s.deps.Pick = func(string) (string, error) { return p.CoverPath, nil }
	v, err := s.SelectCover(v.Revision)
	if err != nil {
		t.Fatal(err)
	}
	originalURL := v.CoverURL
	cached := v.Project.CoverPath
	if originalURL == "" {
		t.Fatal("no registered cover")
	}
	h := s.Media.Middleware(http.NotFoundHandler())
	if request(h, "GET", originalURL, "").Code != 200 {
		t.Fatal("cover not served")
	}
	v, err = s.ApplyEdit(v.Revision, EditRequest{Kind: "cover-remove"})
	if err != nil {
		t.Fatal(err)
	}
	if request(h, "GET", originalURL, "").Code != 404 {
		t.Fatal("old cover handle retained")
	}
	v, err = s.Undo(v.Revision)
	if err != nil || v.CoverURL == "" || request(h, "GET", v.CoverURL, "").Code != 200 {
		t.Fatal("undo cover unavailable")
	}
	if _, err := os.Stat(cached); err != nil {
		t.Fatal("undo bytes deleted")
	}
	saved, err := s.SaveProject(v.Revision)
	if err != nil || saved.CoverURL == "" {
		t.Fatalf("durable cover unavailable %v", err)
	}
	if _, err := os.Stat(p.CoverPath); err != nil {
		t.Fatal("source cover removed")
	}
}

func TestBookPreviewRegistrationFailureCleansStage(t *testing.T) {
	s, _, source := newBookFixture(t)
	view := loadedBook(t, s)
	var path string
	s.deps.Preview = func(_ context.Context, _ book.BookProject, _, cache string) (string, error) {
		path = renderedPreview(t, cache)
		// Closing the registry is a deterministic registration failure after a
		// successful renderer transfer, without modifying the rendered artifacts.
		if err := s.Media.Close(); err != nil {
			t.Fatal(err)
		}
		return path, nil
	}
	if _, err := s.PreviewChapter("register-failure", view.Revision, "c1"); err == nil || !strings.Contains(err.Error(), "registering preview") {
		t.Fatalf("expected registration failure, got %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("service leaked owned preview stage: %v", err)
	}
	if _, err := os.Stat(source.Sources[0].Path); err != nil {
		t.Fatal("source removed", err)
	}
}

func TestBookPreviewFailureNeverCleansForeignOutput(t *testing.T) {
	s, _, source := newBookFixture(t)
	view := loadedBook(t, s)
	s.deps.Preview = func(context.Context, book.BookProject, string, string) (string, error) {
		return source.Sources[0].Path, nil
	}
	if _, err := s.PreviewChapter("foreign-output", view.Revision, "c1"); err == nil {
		t.Fatal("foreign renderer output accepted")
	}
	if data, err := os.ReadFile(source.Sources[0].Path); err != nil || string(data) != "untouched audio source" {
		t.Fatal("source output removed")
	}
}
