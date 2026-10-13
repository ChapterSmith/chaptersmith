package legal

import (
	"strings"
	"testing"
)

func TestFFmpegNoticeNamesLicenseProjectAndSource(t *testing.T) {
	n := FFmpegNotice()
	for _, want := range []string{"FFmpeg", "https://ffmpeg.org", "LGPLv2.1", SourceURL} {
		if !strings.Contains(n, want) {
			t.Errorf("notice %q does not contain %q", n, want)
		}
	}
}

func TestFFmpegNoticeUsesOverriddenSourceURL(t *testing.T) {
	old := SourceURL
	t.Cleanup(func() { SourceURL = old })
	SourceURL = "https://example.com/src"

	if !strings.Contains(FFmpegNotice(), "https://example.com/src") {
		t.Fatal("notice ignores SourceURL override")
	}
}
