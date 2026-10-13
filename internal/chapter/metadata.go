package chapter

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// WriteFFmpegChaptersFile writes an FFmpeg metadata chapters file.
// This is used by the muxer when embedding chapters via ffmetadata format.
func WriteFFmpegChaptersFile(chaptersFile string, chapters []Chapter, totalDurationMs float64) error {
	var b strings.Builder
	b.WriteString(";FFMETADATA1\n")

	for i, ch := range chapters {
		startMs := formatChapterTime(ch.StartTime)

		var endMs string
		if i < len(chapters)-1 {
			endMs = formatChapterTime(chapters[i+1].StartTime)
		} else {
			endMs = formatChapterTime(totalDurationMs / 1000)
		}

		b.WriteString("\n[CHAPTER]\n")
		b.WriteString("TIMEBASE=1/1000\n")
		fmt.Fprintf(&b, "START=%s\n", startMs)
		fmt.Fprintf(&b, "END=%s\n", endMs)
		fmt.Fprintf(&b, "title=%s\n", ch.Title)
	}

	if err := os.WriteFile(chaptersFile, []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("writing chapters file: %w", err)
	}
	return nil
}

func formatChapterTime(seconds float64) string {
	return strconv.FormatInt(int64(seconds*1000), 10)
}
