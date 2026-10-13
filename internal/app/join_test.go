package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/asticode/go-astiav"
	"log"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"chaptersmith/internal/audio"
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/book"
	"chaptersmith/internal/chapter"
	"io"
)

// The oracle directly uses libavcodec, independently of the PCM/encoder core.
func TestCLIContinuousJoinOnsets(t *testing.T) {
	dir := t.TempDir()
	const frames = 22073 // Deliberately not an AAC frame multiple.
	paths := []string{filepath.Join(dir, "a.m4a"), filepath.Join(dir, "b.m4a")}
	originals := make([][32]byte, 2)
	sources := make([][]float32, 2)
	for j, p := range paths {
		samples := make([]float32, frames)
		for i := range samples {
			samples[i] = float32(.3 * math.Cos(2*math.Pi*float64(370+j*210)*float64(i)/44100))
		}
		audiotest.WriteAACSamples(t, p, samples, 128000)
		sources[j] = audiotest.ReferenceAAC(t, p)[:frames]
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		originals[j] = sha256.Sum256(b)
	}
	playlist := filepath.Join(dir, "order.m3u")
	writeCLIFile(t, playlist, "b.m4a\na.m4a\nb.m4a\n")
	cfg := cliConfig(dir)
	cfg.PlaylistPath = playlist
	if e := runCLI(cfg); e != nil {
		t.Fatal(e)
	}
	reference := append(append(append([]float32{}, sources[1]...), sources[0]...), sources[1]...)
	refPath := filepath.Join(t.TempDir(), "reference.m4a")
	audiotest.WriteAACSamples(t, refPath, reference, 128000)
	want := audiotest.ReferenceAAC(t, refPath)
	got := audiotest.ReferenceAAC(t, cfg.OutputPath)
	for _, start := range []int{frames, 2 * frames} {
		var power float64
		for i := start; i < start+512; i++ {
			d := float64(got[i] - want[i])
			power += d * d
		}
		if rmse := math.Sqrt(power / 512); rmse > .005 {
			t.Errorf("boundary %d onset RMSE %.8f > .005", start, rmse)
		}
	}
	chapters, e := chapter.ReadEmbeddedChapters(cfg.OutputPath)
	if e != nil {
		t.Fatal(e)
	}
	if len(chapters) != 3 {
		t.Fatal("chapter count", len(chapters))
	}
	for i, c := range chapters {
		if math.Abs(c.StartTime-float64(i*frames)/44100) > .0011 {
			t.Fatalf("final chapter %d: %g", i, c.StartTime)
		}
	}
	var audible int
	if e := audio.DecodePCM(context.Background(), cfg.OutputPath, 1, 0, -1, func(b []float32) error { audible += len(b); return nil }); e != nil {
		t.Fatal(e)
	}
	if audible != 3*frames {
		t.Errorf("audible frames %d want %d", audible, 3*frames)
	}
	for j, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil || sha256.Sum256(b) != originals[j] {
			t.Fatal("source changed", p, e)
		}
	}
}

func cliAACPayloads(t *testing.T, path string) [][32]byte {
	t.Helper()
	fc := astiav.AllocFormatContext()
	defer fc.Free()
	if e := fc.OpenInput(path, nil, nil); e != nil {
		t.Fatal(e)
	}
	defer fc.CloseInput()
	if e := fc.FindStreamInfo(nil); e != nil {
		t.Fatal(e)
	}
	index := -1
	for _, s := range fc.Streams() {
		if s.CodecParameters().MediaType() == astiav.MediaTypeAudio {
			index = s.Index()
			break
		}
	}
	var hashes [][32]byte
	p := astiav.AllocPacket()
	defer p.Free()
	for {
		e := fc.ReadFrame(p)
		if errors.Is(e, astiav.ErrEof) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if p.StreamIndex() == index {
			hashes = append(hashes, sha256.Sum256(p.Data()))
		}
		p.Unref()
	}
	return hashes
}

func TestCLIContinuousFixLongAndSingleCopy(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.m4a")
	const frames = 21*44100 + 37 // Old parallelism=2 split route activates above 20 s.
	samples := make([]float32, frames)
	for i := range samples {
		samples[i] = float32(.25 * math.Cos(2*math.Pi*540*float64(i)/44100))
	}
	audiotest.WriteAACSamples(t, source, samples, 128000)
	before, _ := os.ReadFile(source)
	hash := sha256.Sum256(before)
	payloads := cliAACPayloads(t, source)
	original := audiotest.ReferenceAAC(t, source, frames+2048)[:frames]
	referencePath := filepath.Join(t.TempDir(), "reference.m4a")
	audiotest.WriteAACSamples(t, referencePath, original, 128000)
	reference := audiotest.ReferenceAAC(t, referencePath, frames+2048)
	adapterOut := filepath.Join(t.TempDir(), "adapter.m4a")
	if e := audio.TranscodeFileParallel(context.Background(), source, adapterOut, 2, log.New(io.Discard, "", 0)); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(cliAACPayloads(t, adapterOut), cliAACPayloads(t, referencePath)) {
		t.Fatal("legacy segmented adapter reintroduced multiple encoders")
	}
	playlist := filepath.Join(dir, "book.m3u")
	writeCLIFile(t, playlist, "source.m4a\n")
	for _, parallel := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(parallel), func(t *testing.T) {
			cfg := cliConfig(dir)
			cfg.PlaylistPath = playlist
			cfg.Parallelism = parallel
			cfg.FixMode = parallel != 0
			cfg.OutputPath = filepath.Join(dir, fmt.Sprintf("out-%d.m4b", parallel))
			if e := runCLI(cfg); e != nil {
				t.Fatal(e)
			}
			gotPackets := cliAACPayloads(t, cfg.OutputPath)
			if parallel == 0 {
				if !reflect.DeepEqual(gotPackets, payloads) {
					t.Fatal("ordinary single AAC packet payloads changed")
				}
			} else {
				if reflect.DeepEqual(gotPackets, payloads) {
					t.Fatal("forced repair did not re-encode")
				}
				// Direct-frame reference uses exactly one fresh AAC encoder at 128000.
				// Matching its whole packet sequence proves repair did not split encoders.
				if !reflect.DeepEqual(gotPackets, cliAACPayloads(t, referencePath)) {
					t.Fatal("repair differs from one fresh 128000-bit/s encoder")
				}
				got := audiotest.ReferenceAAC(t, cfg.OutputPath, frames+2048)
				for _, start := range []int{0, frames / 2, frames - 1024} {
					var power float64
					for i := start; i < start+512; i++ {
						d := float64(got[i] - reference[i])
						power += d * d
					}
					if math.Sqrt(power/512) > .000001 {
						t.Fatalf("repair onset/tail at %d", start)
					}
				}
			}
			count := 0
			if e := audio.DecodePCM(context.Background(), cfg.OutputPath, 1, 0, -1, func(b []float32) error { count += len(b); return nil }); e != nil {
				t.Fatal(e)
			}
			if count != frames {
				t.Fatal("audible length", count)
			}
			after, _ := os.ReadFile(source)
			if sha256.Sum256(after) != hash {
				t.Fatal("source changed")
			}
		})
	}
}

func TestCLIJoinFailureNoSuccess(t *testing.T) {
	for _, kind := range []string{"cancel", "finalize"} {
		t.Run(kind, func(t *testing.T) {
			dir := copyFixtures(t, "m4a/01.m4a", "m4a/02.m4a")
			cfg := cliConfig(dir)
			var messages []string
			cfg.Progress.OnLog = func(s string) { messages = append(messages, s) }
			completed := false
			cfg.Progress.OnProgress = func(current, total int, message string) {
				if message == "Complete" {
					completed = true
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "cancel" {
				cfg.Progress.OnPhase = func(s string) {
					if s == "AUDIO CONVERSION" {
						cancel()
					}
				}
			} else {
				if e := os.Mkdir(cfg.OutputPath, 0700); e != nil {
					t.Fatal(e)
				}
			}
			var logs bytes.Buffer
			e := New(cfg, log.New(&logs, "", 0)).Run(ctx)
			if e == nil || completed || strings.Contains(logs.String()+strings.Join(messages, "\n"), "SUCCESS!") {
				t.Fatal("false success", e, logs.String())
			}
		})
	}
}

func TestJoinedChapterFrames(t *testing.T) {
	dir := copyFixtures(t, "m4a/01.m4a", "m4a/02.m4a")
	dir, _ = filepath.EvalSymlinks(dir)
	inputs := book.ImportInputs{Files: []string{filepath.Join(dir, "02.m4a"), filepath.Join(dir, "01.m4a"), filepath.Join(dir, "02.m4a")}, WorkDir: dir}
	got, e := loadJoinedChapterMarkers(context.Background(), inputs, "files", audio.JoinResult{SampleRate: 48000, Frames: []int64{48013, 48037, 48013}})
	if e != nil {
		t.Fatal(e)
	}
	for i, want := range []float64{0, 48013.0 / 48000, 96050.0 / 48000} {
		if math.Abs(got[i].StartTime-want) > .00000051 {
			t.Fatal("chapter uses probed duration", i, got[i], want)
		}
	}
	if _, e := scaleTimeline(math.MaxInt64, 1000000, 1); e == nil {
		t.Fatal("overflow accepted")
	}
}
