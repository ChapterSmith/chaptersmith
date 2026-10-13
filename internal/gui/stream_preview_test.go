package gui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"chaptersmith/internal/book"
	"chaptersmith/internal/render"
)

// fakeStream is generated content with a recognisable byte at every offset.
type fakeStream struct {
	size                int64
	closed, interrupted atomic.Int32
}

func (f *fakeStream) ReadAt(p []byte, off int64) (int, error) {
	for i := range p {
		if off+int64(i) >= f.size {
			return i, io.EOF
		}
		p[i] = byte((off + int64(i)) % 251)
	}
	return len(p), nil
}
func (f *fakeStream) Size() int64  { return f.size }
func (f *fakeStream) Interrupt()   { f.interrupted.Add(1) }
func (f *fakeStream) Close() error { f.closed.Add(1); return nil }

func withChapterStream(t *testing.T, open func(book.BookProject, string) (mediaStream, error)) {
	t.Helper()
	previous := openChapterStream
	openChapterStream = open
	t.Cleanup(func() { openChapterStream = previous })
}

func getMedia(t *testing.T, s *BookService, url, byteRange string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, url, nil)
	if byteRange != "" {
		request.Header.Set("Range", byteRange)
	}
	response := httptest.NewRecorder()
	s.Media.Middleware(http.NotFoundHandler()).ServeHTTP(response, request)
	return response
}

func TestChapterPreviewStreamsWithoutRenderingAndServesAnyRange(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	stream := &fakeStream{size: 100000}
	withChapterStream(t, func(p book.BookProject, id string) (mediaStream, error) {
		if id != "c2" {
			t.Errorf("stream opened for chapter %q", id)
		}
		return stream, nil
	})
	s.deps.Preview = func(context.Context, book.BookProject, string, string) (string, error) {
		t.Error("a streamed preview must not render a file")
		return "", errors.New("unexpected render")
	}

	result, err := s.PreviewChapter("preview", v.Revision, "c2")
	if err != nil {
		t.Fatal(err)
	}
	response := getMedia(t, s, result.URL, "bytes=70000-70009")
	want := make([]byte, 10)
	stream.ReadAt(want, 70000)
	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), want) {
		t.Fatalf("range request returned %d %v, want 206 %v", response.Code, response.Body.Bytes(), want)
	}
	if got := response.Header().Get("Content-Type"); got != "audio/wav" {
		t.Fatalf("content type %q", got)
	}
	if got := response.Header().Get("Content-Range"); got != "bytes 70000-70009/100000" {
		t.Fatalf("content range %q", got)
	}

	if err = s.ReleaseMedia(result.MediaID); err != nil {
		t.Fatal(err)
	}
	if stream.closed.Load() != 1 {
		t.Fatalf("stream closed %d times after release, want 1", stream.closed.Load())
	}
	if code := getMedia(t, s, result.URL, "").Code; code != http.StatusNotFound {
		t.Fatalf("released media answered %d", code)
	}
}

func TestChapterPreviewRendersAFileWhenTheChapterCannotStream(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	withChapterStream(t, func(book.BookProject, string) (mediaStream, error) { return nil, render.ErrChapterTooLong })
	rendered := 0
	s.deps.Preview = func(_ context.Context, _ book.BookProject, _, dir string) (string, error) {
		rendered++
		return renderedPreview(t, dir), nil
	}
	if _, err := s.PreviewChapter("preview", v.Revision, "c2"); err != nil || rendered != 1 {
		t.Fatalf("err=%v rendered=%d, want the file renderer to be used once", err, rendered)
	}
}

func TestChapterPreviewReportsAStreamThatCannotOpen(t *testing.T) {
	s, _, _ := newBookFixture(t)
	v := loadedBook(t, s)
	withChapterStream(t, func(book.BookProject, string) (mediaStream, error) { return nil, errors.New("source changed") })
	if _, err := s.PreviewChapter("preview", v.Revision, "c2"); err == nil {
		t.Fatal("a failing stream was reported as a ready preview")
	}
}

func TestShutdownClosesRegisteredStreams(t *testing.T) {
	s, _, _ := newBookFixture(t)
	stream := &fakeStream{size: 10}
	if _, err := s.Media.RegisterStream(stream, "audio/wav"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Media.RegisterStream(&fakeStream{size: 1}, "text/html"); err == nil {
		t.Fatal("an unsupported content type was registered")
	}
	if err := s.Media.Close(); err != nil {
		t.Fatal(err)
	}
	if stream.closed.Load() != 1 {
		t.Fatalf("stream closed %d times at shutdown, want 1", stream.closed.Load())
	}
}
