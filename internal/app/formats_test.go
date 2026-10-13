package app

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/asticode/go-astiav"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/chapter"
)

const formatsDir = "../../testdata/formats"

var formatFixtures = []struct {
	name  string
	files []string
}{
	{"mp3", []string{"mp3/01.mp3", "mp3/02.mp3"}},
	{"ogg", []string{"ogg/01.ogg", "ogg/02.ogg"}},
	{"m4a", []string{"m4a/01.m4a", "m4a/02.m4a"}},
	{"aac", []string{"aac/01.aac", "aac/02.aac"}},
}

// copyFixtures copies testdata/formats files into a fresh directory, flattened.
func copyFixtures(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(formatsDir, n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(n)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runApp(t *testing.T, dir string, fix bool) string {
	t.Helper()
	out := filepath.Join(dir, "out.m4b")
	cfg := Config{
		InputDir:    dir,
		OutputPath:  out,
		CPUCores:    2,
		Parallelism: 2,
		FixMode:     fix,
		// OnLog marks the run as non-interactive (no stdin metadata prompt).
		Progress: &ProgressReporter{OnLog: func(string) {}},
	}
	var phases []string
	cfg.Progress.OnPhase = func(phase string) { phases = append(phases, phase) }
	if err := New(cfg, log.New(io.Discard, "", 0)).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fix && !slices.Contains(phases, "CONVERTING TO AAC") {
		t.Fatal("fix mode did not force AAC conversion")
	}
	return out
}

func hasCoverArt(t *testing.T, path string) bool {
	t.Helper()
	fc := astiav.AllocFormatContext()
	defer fc.Free()
	if err := fc.OpenInput(path, nil, nil); err != nil {
		t.Fatal(err)
	}
	defer fc.CloseInput()
	for _, s := range fc.Streams() {
		if s.DispositionFlags().Has(astiav.DispositionFlagAttachedPic) {
			return true
		}
	}
	return false
}

// assertAudiobook checks duration (±0.5 s), cover art and, when wantChapters >= 0, the chapter count.
func assertAudiobook(t *testing.T, path string, wantSec float64, wantChapters int) {
	t.Helper()
	d, err := audio.GetDuration(path)
	if err != nil {
		t.Fatal(err)
	}
	if d < wantSec-0.5 || d > wantSec+0.5 {
		t.Fatalf("duration %.2fs, want ~%.0fs", d, wantSec)
	}
	if wantChapters >= 0 {
		chs, err := chapter.ReadEmbeddedChapters(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(chs) != wantChapters {
			t.Fatalf("chapters: got %d, want %d", len(chs), wantChapters)
		}
	}
	if !hasCoverArt(t, path) {
		t.Fatal("cover art missing")
	}
}

func TestMergeEachFormat(t *testing.T) {
	for _, f := range formatFixtures {
		t.Run(f.name, func(t *testing.T) {
			dir := copyFixtures(t, append(f.files, "cover.jpg")...)
			assertAudiobook(t, runApp(t, dir, false), 6, 2)
		})
	}
}

func TestMergeWithPNGCover(t *testing.T) {
	dir := copyFixtures(t, "mp3/01.mp3", "mp3/02.mp3", "cover.png")
	assertAudiobook(t, runApp(t, dir, false), 6, 2)
}

func TestFixEachFormat(t *testing.T) {
	for _, f := range formatFixtures {
		t.Run(f.name, func(t *testing.T) {
			dir := copyFixtures(t, f.files[0], "cover.jpg")
			assertAudiobook(t, runApp(t, dir, true), 3, -1)
		})
	}
}
