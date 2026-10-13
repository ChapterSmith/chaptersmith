package render

import (
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
	"os"
	"path/filepath"
	"testing"
)

func fixtureProject(t testing.TB, rate int) (book.BookProject, []int16) {
	t.Helper()
	s := make([]int16, rate)
	for i := range s {
		s[i] = int16(i%30000 - 15000)
	}
	path := filepath.Join(t.TempDir(), "in.wav")
	audiotest.WriteWAV(t, path, rate, 1, s)
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	return book.BookProject{ID: "p", SchemaVersion: 1, Sources: []book.Source{{ID: "s", Path: path, DurationUS: 1000000, SizeBytes: st.Size(), MTimeNS: st.ModTime().UnixNano(), SampleRate: rate, Channels: 1}}, Chapters: []book.Chapter{{ID: "a", Title: "A", Segments: []book.Segment{{SourceID: "s", EndUS: 1000000}}}}, Export: book.ExportSettings{Profile: "standard", Channels: "auto", OutputMode: "book", Format: "m4b"}}, s
}
func pcmFloats(s []int16) []float32 {
	out := make([]float32, len(s))
	for i, v := range s {
		out[i] = float32(v) / 32768
	}
	return out
}
