package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/book"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// previewDeps supplies I/O boundaries per request without shared mutable hooks.
type previewDeps struct {
	render func(context.Context, book.BookProject, []string, string, string) error
	encode func(context.Context, string, string, audio.EncodeOptions) error
}

func defaultPreviewDeps() previewDeps {
	return previewDeps{render: RenderPCM, encode: audio.EncodePCM}
}

// Preview renders only the selected chapter to a unique private cached M4A.
// It always uses standard AAC, auto channels and no normalization. The caller
// owns the returned file and its private parent directory after success.
func Preview(ctx context.Context, project book.BookProject, chapterID, cacheDir string) (string, error) {
	return previewWith(ctx, project, chapterID, cacheDir, defaultPreviewDeps())
}

func previewWith(ctx context.Context, project book.BookProject, chapterID, cacheDir string, deps previewDeps) (path string, err error) {
	project = book.Clone(project)
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = book.Validate(project); err != nil {
		return "", err
	}
	found := false
	for _, chapter := range project.Chapters {
		if chapter.ID == chapterID {
			found = true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("unknown chapter ID %q", chapterID)
	}
	// Even sources outside the selected chapter must still match the snapshot.
	if err = book.CheckSources(project); err != nil {
		return "", err
	}
	if strings.TrimSpace(cacheDir) == "" {
		return "", errors.New("preview cache directory is empty")
	}
	cache, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", err
	}
	project.Export = book.ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4a"}
	plan, err := resolveEncodingWith(ctx, project, nil)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(cache, ".book-preview-")
	if err != nil {
		return "", err
	}
	owned, err := os.Lstat(stage)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeOwnedStage(stage, owned))
		}
	}()
	pcm := filepath.Join(stage, "chapter.wav")
	if err = deps.render(ctx, project, []string{chapterID}, pcm, "auto"); err != nil {
		return "", fmt.Errorf("rendering preview PCM: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	output := filepath.Join(stage, "preview.m4a")
	opts := audio.EncodeOptions{Format: "m4a", Channels: plan.Channels, BitRate: plan.BitRate, EndFrame: -1}
	if err = deps.encode(ctx, pcm, output, opts); err != nil {
		return "", fmt.Errorf("encoding preview: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = os.Remove(pcm); err != nil {
		return "", err
	}
	return output, nil
}
