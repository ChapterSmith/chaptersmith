package render

import (
	"chaptersmith/internal/audio"
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestFrameGrid(t *testing.T) {
	for _, c := range []struct{ us, want int64 }{{1000000, 44100}, {500000, 22050}, {23, 1}, {math.MaxInt64, 406750706825295613}} {
		n, e := frameAtUS(c.us)
		if e != nil || n != c.want {
			t.Errorf("Q(%d)=%d,%v want %d", c.us, n, e, c.want)
		}
	}
	if _, e := frameAtUS(-1); e == nil {
		t.Fatal("negative accepted")
	}
}
func TestRenderSelection(t *testing.T) {
	p, s := fixtureProject(t, 44100)
	p.Chapters[0].Segments = []book.Segment{{SourceID: "s", StartUS: 100000, EndUS: 300000}, {SilenceUS: 100000}, {SourceID: "s", StartUS: 500000, EndUS: 700000}}
	got := renderSamples(t, p, nil)
	want := append(pcmFloats(s[4410:13230]), make([]float32, 4410)...)
	want = append(want, pcmFloats(s[22050:30870])...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection differs: got %d want %d samples", len(got), len(want))
	}
}
func TestAdjacentSegmentsEqualUnsplit(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	want := renderSamples(t, p, nil)
	p.Chapters[0].Segments = []book.Segment{{SourceID: "s", EndUS: 123457}, {SourceID: "s", StartUS: 123457, EndUS: 1000000}}
	if !reflect.DeepEqual(renderSamples(t, p, nil), want) {
		t.Fatal("adjacent cuts differ")
	}
}
func TestChapterSelection(t *testing.T) {
	p, s := fixtureProject(t, 44100)
	p.Chapters = []book.Chapter{{ID: "a", Title: "A", Segments: []book.Segment{{SourceID: "s", EndUS: 500000}}}, {ID: "b", Title: "B", Segments: []book.Segment{{SourceID: "s", StartUS: 500000, EndUS: 1000000}}}}
	if !reflect.DeepEqual(renderSamples(t, p, []string{"b"}), pcmFloats(s[22050:])) {
		t.Fatal("wrong selection")
	}
	for _, ids := range [][]string{{"missing"}, {"b", "b"}} {
		if e := RenderPCM(context.Background(), p, ids, filepath.Join(t.TempDir(), "bad.wav"), "auto"); e == nil {
			t.Fatal("bad IDs accepted")
		}
	}
	sp, e := chapterSpans(p)
	if e != nil || len(sp) != 2 || sp[1].StartFrame != 22050 || sp[1].EndFrame != 44100 {
		t.Fatalf("spans %v %v", sp, e)
	}
}
func TestChannels(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	for _, c := range []struct {
		choice string
		want   int
	}{{"auto", 1}, {"mono", 1}, {"stereo", 2}} {
		n, e := resolveChannels(p, c.choice)
		if e != nil || n != c.want {
			t.Fatalf("%s=%d %v", c.choice, n, e)
		}
	}
	p.Sources = append(p.Sources, book.Source{Channels: 2})
	n, e := resolveChannels(p, "auto")
	if e != nil || n != 2 {
		t.Fatal(n, e)
	}
	if _, e = resolveChannels(p, "invalid"); e == nil {
		t.Fatal("invalid accepted")
	}
}
func TestResampling(t *testing.T) {
	p, _ := fixtureProject(t, 48000)
	full := renderSamples(t, p, nil)
	p.Chapters[0].Segments = []book.Segment{{SourceID: "s", StartUS: 123457, EndUS: 999000}}
	a, _ := frameAtUS(123457)
	b, _ := frameAtUS(999000)
	if !reflect.DeepEqual(renderSamples(t, p, nil), full[a:b]) {
		t.Fatal("resample restarted at cut")
	}
}
func renderSamples(t *testing.T, p book.BookProject, ids []string) []float32 {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out.wav")
	if e := RenderPCM(context.Background(), p, ids, out, "auto"); e != nil {
		t.Fatal(e)
	}
	return audiotest.ReadPCM(t, out, 1)
}

func TestRenderReorderedAndRepeatedSources(t *testing.T) {
	p, s := fixtureProject(t, 44100)
	p.Chapters[0].Segments = []book.Segment{{SourceID: "s", StartUS: 700000, EndUS: 900000}, {SourceID: "s", StartUS: 100000, EndUS: 200000}, {SourceID: "s", StartUS: 100000, EndUS: 200000}}
	want := pcmFloats(s[30870:39690])
	want = append(want, pcmFloats(s[4410:8820])...)
	want = append(want, pcmFloats(s[4410:8820])...)
	if !reflect.DeepEqual(renderSamples(t, p, nil), want) {
		t.Fatal("backward/repeated ranges differ")
	}
}
func TestRenderRejectsChangedSourceAndEmptyGrid(t *testing.T) {
	p, _ := fixtureProject(t, 44100)
	p.Sources[0].SizeBytes++
	out := filepath.Join(t.TempDir(), "out.wav")
	if e := RenderPCM(context.Background(), p, nil, out, "auto"); e == nil {
		t.Fatal("changed source accepted")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("output created on validation failure")
	}
	p.Sources[0].SizeBytes--
	p.Chapters[0].Segments = []book.Segment{{SourceID: "s", EndUS: 1}}
	if e := RenderPCM(context.Background(), p, nil, out, "auto"); e == nil {
		t.Fatal("empty sample range accepted")
	}
}
func TestPCMStreamingStress(t *testing.T) {
	if os.Getenv("BOOK_AUDIO_STRESS") != "1" {
		t.Skip("opt-in: BOOK_AUDIO_STRESS=1")
	}
	p, _ := fixtureProject(t, 44100)
	segments := make([]book.Segment, 1200)
	for i := range segments {
		segments[i] = book.Segment{SourceID: "s", EndUS: 1000000}
	}
	p.Chapters[0].Segments = segments
	runtime.GC()
	before := audiotest.ResidentBytes()
	path := filepath.Join(t.TempDir(), "twenty-minutes.wav")
	if e := RenderPCM(context.Background(), p, nil, path, "mono"); e != nil {
		t.Fatal(e)
	}
	var frames int64
	peak := audiotest.ResidentBytes()
	calls := 0
	e := audio.DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error {
		if len(b) > 16384 {
			t.Fatal("unbounded chunk")
		}
		frames += int64(len(b))
		calls++
		if calls%1024 == 0 {
			peak = max(peak, audiotest.ResidentBytes())
		}
		return nil
	})
	if e != nil || frames != 1200*44100 {
		t.Fatal("stress decode", frames, e)
	}
	if peak > before+64*1024*1024 {
		t.Fatalf("stream retained audio: RSS %d -> %d", before, peak)
	}
	t.Logf("20 minutes, %d frames; native-inclusive RSS %d -> %d", frames, before, peak)
}
