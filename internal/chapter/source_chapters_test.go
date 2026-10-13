package chapter_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/chapter"
	"chaptersmith/internal/muxer"
)

func embeddedFixture(t *testing.T, starts ...float64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "part.m4b")
	var markers []chapter.Chapter
	for i, s := range starts {
		title := "First"
		if i > 0 {
			title = "Second"
		}
		markers = append(markers, chapter.Chapter{Title: title, StartTime: s})
	}
	if err := muxer.Finalize(context.Background(), muxer.Options{InputFile: "../../testdata/formats/m4a/01.m4a", OutputFile: p, Chapters: markers, HasChapters: len(markers) > 0}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatal(err)
	}
	// Finalize writes both QT chapter tracks and Nero chpl. QT edit lists make
	// a lone nonzero marker read back at zero. Disable only that track reference
	// in this temporary fixture so libav reads the genuine Nero 1-second marker.
	if len(starts) > 0 && starts[0] > 0 {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		offset := bytes.Index(data, []byte{0, 0, 0, 12, 'c', 'h', 'a', 'p'})
		if offset < 0 {
			t.Fatal("fixture missing QT chapter reference")
		}
		copy(data[offset+4:offset+8], "free")
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func sourceProbes(t *testing.T, files []string) map[string]audio.SourceInfo {
	t.Helper()
	m := map[string]audio.SourceInfo{}
	for _, f := range files {
		v, err := audio.ProbeSource(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		m[f] = v
	}
	return m
}
func assertTwoEach(t *testing.T, files []string) {
	t.Helper()
	probes := sourceProbes(t, files)
	entries := make([]audio.M3UEntry, len(files))
	for i, f := range files {
		entries[i] = audio.M3UEntry{Filename: f, Title: "Whole file"}
	}
	v, err := chapter.ResolveSourceChapters(context.Background(), files, entries, probes, "", "embedded")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != len(files)*2 {
		t.Fatalf("chapters=%+v", v)
	}
	for i, f := range files {
		a, b := v[2*i], v[2*i+1]
		if a.SourceIndex != i || b.SourceIndex != i || a.StartUS != 0 || a.EndUS != 1000000 || b.StartUS != 1000000 || b.EndUS != probes[f].DurationUS || a.Title != "First" || b.Title != "Second" {
			t.Fatalf("chapters=%+v", v)
		}
	}
}
func TestAllEmbeddedSources(t *testing.T) {
	assertTwoEach(t, []string{embeddedFixture(t, 0, 1), embeddedFixture(t, 0, 1)})
}
func TestEmbeddedRepeatedOccurrence(t *testing.T) {
	p := embeddedFixture(t, 0, 1)
	assertTwoEach(t, []string{p, p})
}
func TestEmbeddedOpening(t *testing.T) {
	p := embeddedFixture(t, 1)
	probes := sourceProbes(t, []string{p})
	v, err := chapter.ResolveSourceChapters(context.Background(), []string{p}, nil, probes, "", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || v[0].Title != "part — Opening" || v[0].StartUS != 0 || v[0].EndUS != 1000000 || v[1].StartUS != 1000000 || v[1].EndUS != probes[p].DurationUS {
		t.Fatalf("chapters=%+v", v)
	}
	m, err := chapter.ReadEmbeddedMarkers(p)
	if err != nil || len(m) != 1 || m[0].StartUS != 1000000 {
		t.Fatalf("markers=%+v err=%v", m, err)
	}
	legacy, err := chapter.ReadEmbeddedChapters(p)
	if err != nil || len(legacy) != 1 || legacy[0].StartTime != 1 {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
}
func TestSourceFallbackTitles(t *testing.T) {
	a := embeddedFixture(t)
	b := embeddedFixture(t)
	files := []string{a, b, a}
	probes := sourceProbes(t, files)
	entries := []audio.M3UEntry{{Filename: a, Title: "First occurrence", Duration: 999}, {Filename: b}, {Filename: a, Title: "Encore", Duration: 1}}
	for _, mode := range []string{"auto", "embedded", "files"} {
		v, err := chapter.ResolveSourceChapters(context.Background(), files, entries, probes, "", mode)
		if err != nil || len(v) != 3 {
			t.Fatalf("chapters=%+v err=%v", v, err)
		}
		for i, title := range []string{"First occurrence", "part", "Encore"} {
			if v[i].Title != title || v[i].SourceIndex != i || v[i].StartUS != 0 || v[i].EndUS != probes[files[i]].DurationUS {
				t.Fatalf("chapters=%+v", v)
			}
		}
	}
}
func resolutionCue(t *testing.T, files []string, starts string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.cue")
	var s string
	for _, f := range files {
		s += "FILE \"" + f + "\" WAVE\n TRACK 01 AUDIO\n INDEX 01 " + starts + "\n TRACK 02 AUDIO\n INDEX 01 00:01:37\n"
	}
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestCueResolution(t *testing.T) {
	a := embeddedFixture(t)
	b := embeddedFixture(t)
	files := []string{a, b}
	probes := sourceProbes(t, files)
	cue := resolutionCue(t, files, "00:00:00")
	v, err := chapter.ResolveSourceChapters(context.Background(), files, nil, probes, cue, "cue")
	if err != nil || len(v) != 4 {
		t.Fatalf("chapters=%+v err=%v", v, err)
	}
	for i, f := range files {
		a, b := v[2*i], v[2*i+1]
		if a.SourceIndex != i || b.SourceIndex != i || a.StartUS != 0 || a.EndUS != 1493333 || b.StartUS != 1493333 || b.EndUS != probes[f].DurationUS {
			t.Fatalf("chapters=%+v", v)
		}
	}
	auto, err := chapter.ResolveSourceChapters(context.Background(), files, nil, probes, cue, "auto")
	if err != nil || len(auto) != 4 {
		t.Fatalf("auto chapters=%+v err=%v", auto, err)
	}
	for _, bad := range [][]string{{b, a}, {a}, {a, a}} {
		_, err := chapter.ResolveSourceChapters(context.Background(), bad, nil, probes, cue, "cue")
		if err == nil {
			t.Fatalf("accepted files=%v", bad)
		}
	}
	small := map[string]audio.SourceInfo{a: {DurationUS: 1000000}, b: probes[b]}
	if _, err := chapter.ResolveSourceChapters(context.Background(), files, nil, small, cue, "cue"); err == nil {
		t.Fatal("out of bounds accepted")
	}
	opening := resolutionCue(t, files, "00:00:37")
	v, err = chapter.ResolveSourceChapters(context.Background(), files, nil, probes, opening, "cue")
	if err != nil || len(v) != 6 || v[0].Title != "part — Opening" || v[0].StartUS != 0 || v[0].EndUS != 493333 || v[1].StartUS != 493333 {
		t.Fatalf("chapters=%+v err=%v", v, err)
	}
}
func TestChapterSourceConflict(t *testing.T) {
	p := embeddedFixture(t, 0, 1)
	files := []string{p}
	probes := sourceProbes(t, files)
	cue := resolutionCue(t, files, "00:00:00")
	for _, mode := range []string{"auto", "", "embedded", "files"} {
		_, err := chapter.ResolveSourceChapters(context.Background(), files, nil, probes, cue, mode)
		if err == nil || !strings.Contains(err.Error(), cue) {
			t.Fatalf("mode=%s err=%v", mode, err)
		}
	}
	if _, err := chapter.ResolveSourceChapters(context.Background(), files, nil, probes, cue, "cue"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"embedded", "files"} {
		if _, err := chapter.ResolveSourceChapters(context.Background(), files, nil, probes, "", mode); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := chapter.ResolveSourceChapters(context.Background(), files, nil, probes, "", "cue"); err == nil {
		t.Fatal("cue without path accepted")
	}
}
func TestEmbeddedInvalidInputs(t *testing.T) {
	p := embeddedFixture(t, 0, 1)
	files := []string{p}
	probes := sourceProbes(t, files)
	for _, tc := range []struct {
		entries []audio.M3UEntry
		probes  map[string]audio.SourceInfo
		mode    string
	}{{[]audio.M3UEntry{{}, {}}, probes, "files"}, {nil, nil, "files"}, {nil, map[string]audio.SourceInfo{p: {}}, "files"}, {nil, probes, "invalid"}} {
		if _, err := chapter.ResolveSourceChapters(context.Background(), files, tc.entries, tc.probes, "", tc.mode); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := chapter.ResolveSourceChapters(ctx, files, nil, probes, "", "embedded")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
