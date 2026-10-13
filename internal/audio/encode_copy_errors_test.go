package audio

import (
	"chaptersmith/internal/audiotest"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A compatibility failure may be recoverable, but a joined close/I/O failure is not.
func TestAACCopyIncompatibilityClassification(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.m4a")
	audiotest.WriteAAC(t, source, 44117, 0)
	opts := EncodeOptions{Format: "m4a", Channels: 1, EndFrame: 1000}
	incompatible := CopyAAC(context.Background(), source, filepath.Join(t.TempDir(), "out.m4a"), opts)
	if incompatible == nil {
		t.Fatal("trim accepted")
	}
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"plain", incompatible, true}, {"wrapped", fmt.Errorf("copy: %w", incompatible), true},
		{"pure join", errors.Join(incompatible, fmt.Errorf("again: %w", incompatible)), true},
		{"joined close", errors.Join(incompatible, &os.PathError{Op: "close", Path: source, Err: syscall.EIO}), false},
		{"nested operational", fmt.Errorf("copy: %w", errors.Join(errors.Join(incompatible), syscall.ENOSPC)), false},
		{"cancel", errors.Join(incompatible, context.Canceled), false}, {"nil", nil, false}, {"operational", syscall.EIO, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAACCopyIncompatible(tc.err); got != tc.want {
				t.Fatalf("classification %v want %v: %v", got, tc.want, tc.err)
			}
		})
	}
	for _, kind := range []string{"missing", "cancel", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := source
			options := EncodeOptions{Format: "m4a", Channels: 1, EndFrame: -1}
			switch kind {
			case "missing":
				path += ".missing"
			case "cancel":
				cancel()
			case "metadata":
				options.Metadata.Title = "bad\x00tag"
			}
			e := CopyAAC(ctx, path, filepath.Join(t.TempDir(), "out.m4a"), options)
			if e == nil || IsAACCopyIncompatible(e) {
				t.Fatal("operational failure classified as recoverable", e)
			}
		})
	}
}
