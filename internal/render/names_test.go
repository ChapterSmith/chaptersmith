package render

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Catches unsafe paths, normalization of spelling, byte-sliced runes and ambiguous numbering.
func TestExportNames(t *testing.T) {
	for _, tc := range []struct {
		title, format, want string
		index, total        int
	}{
		{"Title", "m4b", "001 — Title.m4a", 1, 2},
		{"Title", "flac", "1000 — Title.flac", 1000, 1000},
		{"Title", "wav", "0001 — Title.wav", 1, 1000},
		{"é", "m4a", "001 — é.m4a", 1, 2},
		{"e\u0301", "m4a", "002 — e\u0301.m4a", 2, 2},
		{"CON", "wav", "001 — _CON.wav", 1, 1},
		{"aux.txt", "wav", "001 — _aux.txt.wav", 1, 1},
		{"a/b\\c:*?\"<>|\x00\n. ", "wav", "001 — a_b_c_________.wav", 1, 1},
		{"... ", "wav", "001 — Chapter.wav", 1, 1},
	} {
		got, e := chapterFilename(tc.index, tc.total, tc.title, tc.format)
		if e != nil || got != tc.want {
			t.Errorf("%q: %q %v want %q", tc.title, got, e, tc.want)
		}
	}
	a, _ := chapterFilename(1, 2, "Same", "wav")
	b, _ := chapterFilename(2, 2, "Same", "wav")
	if strings.EqualFold(a, b) {
		t.Fatal("duplicate names")
	}
	got, e := chapterFilename(1, 1, strings.Repeat("界", 50), "wav")
	if e != nil || !utf8.ValidString(got) || got != "001 — "+strings.Repeat("界", 40)+".wav" {
		t.Fatal(got, e)
	}
	for _, x := range []struct {
		i, n int
		f    string
	}{{0, 1, "wav"}, {2, 1, "wav"}, {1, 0, "wav"}, {1, 1, "mp3"}} {
		if _, e := chapterFilename(x.i, x.n, "A", x.f); e == nil {
			t.Fatal("invalid name accepted", x)
		}
	}
}

func TestExportNamesTruncatedBlank(t *testing.T) {
	got, e := chapterFilename(1, 1, strings.Repeat(" ", 121)+"Title", "wav")
	if e != nil || got != "001 — Chapter.wav" {
		t.Fatal(got, e)
	}
}
