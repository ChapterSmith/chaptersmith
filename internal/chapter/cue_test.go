package chapter

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cueFixture(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.cue")
	if err := os.WriteFile(p, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestCueFileLocalIndices(t *testing.T) {
	p := cueFixture(t, "\ufeffTITLE \"Album\"\r\nFILE \"część one.wav\" WAVE\r\n TRACK 01 AUDIO\r\n INDEX 00 00:00:00\r\n INDEX 01 00:00:00\r\n TRACK 02 AUDIO\r\n TITLE \"Second\"\r\n INDEX 01 00:01:37\r\n file \"two.wav\" WAVE\r\n track 03 audio\r\n index 01 00:00:00\r\n track 04 audio\r\n index 01 00:01:37\r\n")
	v, err := ParseCueTracks(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 4 || v[0].StartUS != 0 || v[1].StartUS != 1493333 || v[2].StartUS != 0 || v[3].StartUS != 1493333 || v[0].Title != "Chapter 1" || v[1].Title != "Second" || v[0].FilePath != filepath.Join(filepath.Dir(p), "część one.wav") {
		t.Fatalf("tracks=%+v", v)
	}
	if _, err := ParseCueFile(p, log.New(io.Discard, "", 0)); err == nil {
		t.Fatal("legacy multi-file CUE accepted")
	}
}
func TestCueStrictErrors(t *testing.T) {
	prefix := "FILE \"a.wav\" WAVE\n TRACK 01 AUDIO\n"
	for _, s := range []string{"INDEX 01 00:00:00\n", "FILE \"a.wav\" WAVE\n INDEX 01 00:00:00\n", prefix + "INDEX 01 00:00:00\n INDEX 01 00:00:01\n", prefix + "INDEX 01 00:01:00\n TRACK 02 AUDIO\n INDEX 01 00:00:00\n", prefix + "INDEX 01 00:60:00\n", prefix + "INDEX 01 00:00:75\n", "FILE \"a.wav\" WAVE\n TRACK 01 MODE1/2352\n INDEX 01 00:00:00\n", prefix, prefix + "PREGAP 00:01:00\n INDEX 01 00:00:00\n", prefix + "POSTGAP 00:01:00\n INDEX 01 00:00:00\n", prefix + "INDEX 01 00:00:00\n FILE \"b.wav\" WAVE\n TRACK 02 AUDIO\n INDEX 01 00:00:00\n FILE \"a.wav\" WAVE\n TRACK 03 AUDIO\n INDEX 01 00:00:00\n", prefix + "INDEX 01 999999999999999999:00:00\n"} {
		t.Run(s, func(t *testing.T) {
			p := cueFixture(t, s)
			_, err := ParseCueTracks(p)
			if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "line") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// Malformed tables must fail rather than omit audio by sorting or truncating markers.
func TestEmbeddedMalformedMarkers(t *testing.T) {
	for _, markers := range [][]Marker{
		{{StartUS: -1}}, {{StartUS: 0}, {StartUS: 0}}, {{StartUS: 1000000}, {StartUS: 0}}, {{StartUS: 3000000}},
	} {
		if _, err := sourceMarkerRanges(0, "part.wav", 3000000, markers); err == nil {
			t.Fatalf("accepted markers=%+v", markers)
		}
	}
}

func TestCueLargeRepresentableTime(t *testing.T) {
	p := cueFixture(t, "FILE \"a.wav\" WAVE\n TRACK 01 AUDIO\n INDEX 01 10000000000:00:37\n")
	tracks, err := ParseCueTracks(p)
	if err != nil || len(tracks) != 1 || tracks[0].StartUS != 600000000000493333 {
		t.Fatalf("tracks=%+v err=%v", tracks, err)
	}
}
