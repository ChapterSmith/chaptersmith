package audio

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeSource(t *testing.T) {
	for _, tc := range []struct {
		ext  string
		rate int
	}{{"wav", 44100}, {"flac", 44100}, {"opus", 48000}} {
		t.Run(tc.ext, func(t *testing.T) {
			p := filepath.Join("../../testdata/formats", tc.ext, "01."+tc.ext)
			s, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			v, err := ProbeSource(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			if v.SampleRate != tc.rate || v.Channels != 1 || v.SizeBytes != s.Size() || v.MTimeNS != s.ModTime().UnixNano() || v.DurationUS < 2970000 || v.DurationUS > 3030000 {
				t.Fatalf("info=%+v stat=%v", v, s)
			}
			if tc.ext == "wav" && v.DurationUS != 3000000 {
				t.Fatalf("duration=%d", v.DurationUS)
			}
		})
	}
}
func TestProbeFailure(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"missing.wav", "truncated.wav", "notes.txt"} {
		p := filepath.Join(d, n)
		if n != "missing.wav" {
			os.WriteFile(p, []byte("RIFF"), 0600)
		}
		_, err := ProbeSource(context.Background(), p)
		if err == nil || !strings.Contains(err.Error(), p) {
			t.Fatalf("err=%v", err)
		}
	}
	image := "../../testdata/formats/cover.jpg"
	if _, err := ProbeSource(context.Background(), image); err == nil || !strings.Contains(err.Error(), image) {
		t.Fatalf("non-audio err=%v", err)
	}
	p := "../../testdata/formats/wav/01.wav"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ProbeSource(ctx, p)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), p) {
		t.Fatalf("err=%v", err)
	}
}
