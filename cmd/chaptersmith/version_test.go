package main

import (
	"bytes"
	"strings"
	"testing"

	"chaptersmith/internal/legal"
)

func TestPrintVersionIncludesFFmpegNotice(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)

	out := buf.String()
	if !strings.Contains(out, "chaptersmith "+version) {
		t.Errorf("missing version line: %q", out)
	}
	if !strings.Contains(out, legal.FFmpegNotice()) {
		t.Errorf("missing FFmpeg notice: %q", out)
	}
}
