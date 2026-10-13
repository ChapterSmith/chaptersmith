package gui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"chaptersmith/internal/book"
	"chaptersmith/internal/render"
)

type PreviewResult struct {
	RequestID   string `json:"requestId"`
	ProjectID   string `json:"projectId"`
	Revision    int64  `json:"revision"`
	MediaID     string `json:"mediaId"`
	URL         string `json:"url"`
	DurationUS  int64  `json:"durationUs"`
	OriginUS    int64  `json:"originUs"`
	OldMarkerUS int64  `json:"oldMarkerUs"`
	NewMarkerUS int64  `json:"newMarkerUs"`
}

func (s *BookService) PreviewChapter(requestID string, expectedRevision int64, chapterID string) (result PreviewResult, err error) {
	ctx, snapshot, err := s.beginOperation(requestID, "preview", expectedRevision)
	if err != nil {
		return result, err
	}
	defer func() {
		status, message := "succeeded", "Preview ready — press Play"
		if err != nil {
			status, message = "failed", err.Error()
			if errors.Is(err, context.Canceled) {
				status = "cancelled"
			}
		}
		s.finishOperation(requestID, status, message)
	}()
	origin, err := book.ChapterStartUS(snapshot, chapterID)
	if err != nil {
		return result, err
	}
	var duration int64
	for _, chapter := range snapshot.Chapters {
		if chapter.ID == chapterID {
			duration = book.DurationUS(book.BookProject{Chapters: []book.Chapter{chapter}})
			break
		}
	}
	return s.renderPreview(ctx, snapshot, chapterID, PreviewResult{RequestID: requestID, ProjectID: snapshot.ID, Revision: expectedRevision, DurationUS: duration, OriginUS: origin})
}

// openChapterStream plays a chapter straight from the edit, decoded while it
// is read. Nil selects the rendered-file preview for every chapter.
var openChapterStream = func(p book.BookProject, chapterID string) (mediaStream, error) {
	return render.OpenChapterStream(p, chapterID)
}

// streamPreview registers an on-demand stream of the chapter. ok is false when
// the chapter has to be rendered to a file instead.
func streamPreview(media *MediaRegistry, p book.BookProject, chapterID string) (ref MediaRef, ok bool, err error) {
	if openChapterStream == nil {
		return ref, false, nil
	}
	stream, err := openChapterStream(p, chapterID)
	if errors.Is(err, render.ErrChapterTooLong) {
		return ref, false, nil
	}
	if err != nil {
		return ref, false, err
	}
	if ref, err = media.RegisterStream(stream, "audio/wav"); err != nil {
		return ref, false, errors.Join(fmt.Errorf("registering preview: %w", err), stream.Close())
	}
	return ref, true, nil
}

// renderPreview retains cleanup ownership until the media registry adopts the stage.
func (s *BookService) renderPreview(ctx context.Context, snapshot book.BookProject, chapterID string, result PreviewResult) (PreviewResult, error) {
	if err := book.CheckSources(snapshot); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if ref, ok, err := streamPreview(s.Media, snapshot, chapterID); err != nil {
		return result, err
	} else if ok {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return result, errors.Join(err, s.Media.Release(ref.ID))
		}
		s.operation.committed = true
		result.MediaID, result.URL = ref.ID, ref.URL
		return result, nil
	}
	path, err := s.deps.Preview(ctx, snapshot, chapterID, filepath.Join(s.cacheDir, "preview"))
	if err != nil {
		return result, err
	}
	// A successful renderer transfers both the file and its private stage.
	// Keep independent cleanup ownership until the registry adopts the file.
	owned, err := ownRenderedPreview(filepath.Join(s.cacheDir, "preview"), path)
	if err != nil {
		return result, fmt.Errorf("claiming rendered preview: %w", err)
	}
	ref, err := s.Media.Register(path, "audio/mp4")
	if err != nil {
		return result, errors.Join(fmt.Errorf("registering preview: %w", err), owned.remove(), owned.root.Close())
	}
	if err := owned.root.Close(); err != nil {
		return result, errors.Join(err, s.Media.Release(ref.ID))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, errors.Join(err, s.Media.Release(ref.ID))
	}
	s.operation.committed = true
	result.MediaID, result.URL = ref.ID, ref.URL
	return result, nil
}
func (s *BookService) ReleaseMedia(mediaID string) error { return s.Media.Release(mediaID) }
