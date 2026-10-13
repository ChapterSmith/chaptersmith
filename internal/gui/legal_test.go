package gui

import (
	"testing"

	"chaptersmith/internal/legal"
)

func TestLegalNoticeMatchesFFmpegNotice(t *testing.T) {
	if got := NewMergerService(nil).LegalNotice(); got != legal.FFmpegNotice() {
		t.Fatalf("LegalNotice() = %q", got)
	}
}
