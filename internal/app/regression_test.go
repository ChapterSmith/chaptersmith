package app

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"chaptersmith/internal/chapter"
)

func TestRunPreservesSourceCover(t *testing.T) {
	for _, fix := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "fix"}[fix], func(t *testing.T) {
			files := []string{"m4a/01.m4a", "cover.jpg"}
			if !fix {
				files = append(files, "m4a/02.m4a")
			}
			dir := copyFixtures(t, files...)
			cover := filepath.Join(dir, "cover.jpg")
			before, err := os.ReadFile(cover)
			if err != nil {
				t.Fatal(err)
			}
			runApp(t, dir, fix)
			after, err := os.ReadFile(cover)
			if err != nil {
				t.Fatalf("source cover lost: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("source cover changed")
			}
		})
	}
}

func TestRunPreservesPlaylistOrder(t *testing.T) {
	dir := copyFixtures(t, "m4a/01.m4a", "m4a/02.m4a", "cover.png")
	playlist := "#EXTM3U\n#EXTINF:3,Second first\n02.m4a\n#EXTINF:3,First second\n01.m4a\n"
	if err := os.WriteFile(filepath.Join(dir, "book.m3u"), []byte(playlist), 0600); err != nil {
		t.Fatal(err)
	}
	chapters, err := chapter.ReadEmbeddedChapters(runApp(t, dir, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 2 || chapters[0].Title != "Second first" || chapters[1].Title != "First second" {
		t.Fatalf("playlist order changed: %+v", chapters)
	}
}

func TestRunReadsCueFromInputDirectory(t *testing.T) {
	dir := copyFixtures(t, "m4a/01.m4a", "m4a/02.m4a", "cover.png")
	cue := "FILE \"01.m4a\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"Opening\"\n    INDEX 01 00:00:00\nFILE \"02.m4a\" WAVE\n  TRACK 02 AUDIO\n    TITLE \"Ending\"\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, "book.cue"), []byte(cue), 0600); err != nil {
		t.Fatal(err)
	}
	chapters, err := chapter.ReadEmbeddedChapters(runApp(t, dir, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 2 || chapters[0].Title != "Opening" {
		t.Fatalf("input directory CUE ignored: %+v", chapters)
	}
}

func TestRunKeepsRequestedOutputPath(t *testing.T) {
	dir := copyFixtures(t, "m4a/01.m4a", "m4a/02.m4a")
	out := filepath.Join(dir, "custom.audio")
	cfg := Config{InputDir: dir, OutputPath: out, Parallelism: 2, Progress: &ProgressReporter{OnLog: func(string) {}}}
	if err := New(cfg, log.New(io.Discard, "", 0)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("requested output missing: %v", err)
	}
}

func TestLogWriterPreservesPartialLines(t *testing.T) {
	var lines []string
	w := &logWriter{onLog: func(s string) { lines = append(lines, s) }}
	for _, part := range []string{"first ", "line\nsecond", " line\n"} {
		if _, err := w.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(lines, []string{"first line", "second line"}) {
		t.Fatalf("fragmented log lines lost: %q", lines)
	}
}
