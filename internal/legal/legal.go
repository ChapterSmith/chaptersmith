// Package legal holds third-party notices shown to users.
package legal

// SourceURL locates the release archives containing corresponding FFmpeg source. Override at build time with
// -ldflags "-X chaptersmith/internal/legal.SourceURL=...".
var SourceURL = "https://github.com/ChapterSmith/chaptersmith/releases"

// FFmpegNotice is the attribution required by FFmpeg's LGPL checklist.
func FFmpegNotice() string {
	return "This software uses libraries from the FFmpeg project (https://ffmpeg.org) " +
		"under the LGPLv2.1. FFmpeg source code: " + SourceURL +
		". Build instructions: bundled README.md and BUILDING-FFMPEG.txt."
}
