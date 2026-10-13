package audio

import (
	"chaptersmith/internal/audiotest"
	"chaptersmith/internal/meta"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func encodingFixture(t *testing.T, channels, frames int) (string, []float32) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.wav")
	s := make([]int16, frames*channels)
	want := make([]float32, len(s))
	for i := range s {
		s[i] = int16(12000 * math.Sin(2*math.Pi*440*float64(i/channels)/44100))
		want[i] = float32(s[i]) / 32768
	}
	audiotest.WriteWAV(t, path, 44100, channels, s)
	return path, want
}
func encodedPCM(t *testing.T, path string, ch int) []float32 {
	t.Helper()
	var out []float32
	if e := DecodePCM(context.Background(), path, ch, 0, -1, func(b []float32) error { out = append(out, b...); return nil }); e != nil {
		t.Fatal(e)
	}
	return out
}

// Catches padded/truncated final AAC frames, wrong sample rate and lossy lossless output.
func TestEncodeFormats(t *testing.T) {
	for _, format := range []string{"m4b", "m4a", "flac", "wav"} {
		for _, ch := range []int{1, 2} {
			t.Run(format+string(rune('0'+ch)), func(t *testing.T) {
				src, want := encodingFixture(t, ch, 44117)
				out := filepath.Join(t.TempDir(), "out."+format)
				e := EncodePCM(context.Background(), src, out, EncodeOptions{Format: format, Channels: ch, BitRate: 96000, EndFrame: -1})
				if e != nil {
					t.Fatal(e)
				}
				got := encodedPCM(t, out, ch)
				if len(got) != len(want) {
					t.Fatalf("audible length %d want %d", len(got), len(want))
				}
				info, e := ProbeSource(context.Background(), out)
				if e != nil || info.SampleRate != 44100 || info.Channels != ch {
					t.Fatal(info, e)
				}
				if format == "wav" || format == "flac" {
					if !reflect.DeepEqual(got, want) {
						t.Fatal("lossless samples changed")
					}
				} else {
					signalError(t, got, want, 100*ch, len(want)-100*ch)
				}
			})
		}
	}
}
func signalError(t *testing.T, got, want []float32, start, end int) {
	t.Helper()
	var delta, power float64
	for i := start; i < end; i++ {
		d := float64(got[i] - want[i])
		delta += d * d
		power += float64(want[i]) * float64(want[i])
	}
	if delta/power > .025 {
		t.Fatalf("acoustic error ratio %g", delta/power)
	}
}
func TestEncoderPartialFinalFrame(t *testing.T) {
	src, want := encodingFixture(t, 1, 44117)
	out := filepath.Join(t.TempDir(), "out.m4a")
	if e := EncodePCM(context.Background(), src, out, EncodeOptions{Format: "m4a", Channels: 1, BitRate: 96000, EndFrame: -1}); e != nil {
		t.Fatal(e)
	}
	got := encodedPCM(t, out, 1)
	if len(got) != 44117 {
		t.Fatal("audible length", len(got))
	}
	oraclePath := filepath.Join(t.TempDir(), "oracle.m4a")
	audiotest.WriteAACSamples(t, oraclePath, want, 96000)
	oracle := audiotest.ReferenceAAC(t, oraclePath)
	ref := audiotest.ReferenceAAC(t, out)
	if len(ref) < 44117 {
		t.Fatal("reference short", len(ref))
	}
	for _, start := range []int{0, 21000, 43093} {
		if !reflect.DeepEqual(got[start:start+1024], ref[start:start+1024]) {
			t.Fatal("independent decoder differs", start)
		}
		t.Logf("acoustic window start=%d", start)
		signalError(t, got, oracle, start, start+1024)
	}
	inspect := audiotest.Inspect(t, out)
	if inspect.DurationFrames != 44117 {
		t.Fatal("independent container duration", inspect.DurationFrames)
	}
}
func TestProjectTags(t *testing.T) {
	src, _ := encodingFixture(t, 1, 44117)
	tagged := filepath.Join(t.TempDir(), "tagged.m4a")
	options := EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1, Metadata: meta.AudiobookMetadata{Title: "Source title", Artist: "Source author", Composer: "Source narrator", Extra: map[string]string{"keywords": "source"}}}
	if e := EncodePCM(context.Background(), src, tagged, options); e != nil {
		t.Fatal(e)
	}
	for _, format := range []string{"m4b", "flac", "wav"} {
		for _, whole := range []bool{true, false} {
			out := filepath.Join(t.TempDir(), "out."+format)
			opts := EncodeOptions{Format: format, Channels: 1, BitRate: 64000, EndFrame: -1, Track: "2/3", Metadata: meta.AudiobookMetadata{Album: "Book", Extra: map[string]string{"keywords": "project", "title": "must not win", "artist": "must not win", "composer": "must not win", "track": "7/8"}}}
			if whole {
				opts.Chapters = []ChapterMarker{{"Opening", 0, 12345}, {"Ending", 12345, 44117}}
			}
			if e := EncodePCM(context.Background(), tagged, out, opts); e != nil {
				t.Fatal(e)
			}
			got := audiotest.Inspect(t, out)
			for _, key := range []string{"title", "artist", "composer"} {
				if got.Tags[key] != "" {
					t.Fatal("cleared tag resurrected", key, got.Tags)
				}
			}
			if got.Tags["album"] != "Book" || got.Tags["track"] != "2/3" {
				t.Fatal(got.Tags)
			}
			if format != "wav" && got.Tags["keywords"] != "project" {
				t.Fatal("Extra lost", got.Tags)
			}
			if format == "m4b" {
				if whole {
					if len(got.Chapters) != 2 || got.Chapters[1].StartFrame != 12345 || got.Chapters[1].EndFrame != 44117 {
						t.Fatal(got.Chapters)
					}
				} else if len(got.Chapters) != 0 {
					t.Fatal("individual inherited chapters")
				}
			}
		}
	}
}
func TestCoverSupport(t *testing.T) {
	src, _ := encodingFixture(t, 1, 2000)
	for _, kind := range []string{"jpeg", "png", "invalid"} {
		cover := filepath.Join(t.TempDir(), "cover")
		f, e := os.Create(cover)
		if e != nil {
			t.Fatal(e)
		}
		im := image.NewRGBA(image.Rect(0, 0, 16, 16))
		im.Set(0, 0, color.RGBA{R: 255, A: 255})
		switch kind {
		case "jpeg":
			e = jpeg.Encode(f, im, nil)
		case "png":
			e = png.Encode(f, im)
		default:
			_, e = f.WriteString("bad image")
		}
		if e != nil {
			t.Fatal(e)
		}
		f.Close()
		for _, format := range []string{"m4a", "m4b", "flac", "wav"} {
			out := filepath.Join(t.TempDir(), "out."+format)
			e := EncodePCM(context.Background(), src, out, EncodeOptions{Format: format, Channels: 1, BitRate: 64000, EndFrame: -1, CoverPath: cover, Metadata: meta.AudiobookMetadata{Title: "Artwork", Extra: map[string]string{"keywords": "with cover"}}})
			if kind == "invalid" {
				if e == nil {
					t.Fatal("invalid cover accepted")
				}
				continue
			}
			if e != nil {
				t.Fatal(e)
			}
			got := audiotest.Inspect(t, out)
			want := 1
			if format == "wav" {
				want = 0
			}
			if format != "wav" && got.Tags["keywords"] != "with cover" {
				t.Fatal("cover/Extra coexistence", got.Tags)
			}
			if got.Covers != want {
				t.Fatalf("%s/%s covers %d", kind, format, got.Covers)
			}
		}
	}
}
func TestEncodeFailureCleanup(t *testing.T) {
	src, _ := encodingFixture(t, 1, 44117)
	before, _ := os.ReadFile(src)
	opts := EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1}
	if e := EncodePCM(context.Background(), src, src, opts); e == nil {
		t.Fatal("source overwritten")
	}
	for _, kind := range []string{"write", "short", "trailer", "sync", "close"} {
		t.Run(kind, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out.m4a")
			fault := errors.New("injected " + kind)
			var f *faultOutput
			e := withEncodedOutput(out, func(file *os.File) error {
				f = &faultOutput{File: file, kind: kind, fault: fault}
				format, _ := bookPCMFormat(1)
				return encodeStream(context.Background(), f, format, opts, func(emit func([]float32) error) error { return emit(make([]float32, 44117)) })
			})
			if kind == "short" {
				fault = io.ErrShortWrite
			}
			if !errors.Is(e, fault) || !f.closed {
				t.Fatal(e, f.closed)
			}
			if _, e = os.Lstat(out); !os.IsNotExist(e) {
				t.Fatal("partial survives")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := filepath.Join(t.TempDir(), "cancel.m4a")
	e := withEncodedOutput(out, func(f *os.File) error {
		format, _ := bookPCMFormat(1)
		return encodeStream(ctx, f, format, opts, func(emit func([]float32) error) error { cancel(); return emit([]float32{0}) })
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = os.Lstat(out); !os.IsNotExist(e) {
		t.Fatal("cancel partial survives")
	}
	after, _ := os.ReadFile(src)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("source modified")
	}
}

func TestPreserveAACCopy(t *testing.T) {
	for _, origin := range []int{0, 88200} {
		t.Run(fmt.Sprint(origin), func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "source.bin")
			audiotest.WriteAAC(t, src, 44117, origin)
			before, _ := os.ReadFile(src)
			t.Logf("source inspect: %+v", audiotest.Inspect(t, src))
			out := filepath.Join(t.TempDir(), "copy.m4b")
			opts := EncodeOptions{Format: "m4b", Channels: 1, EndFrame: -1, Metadata: meta.AudiobookMetadata{Title: "Copied", Extra: map[string]string{"keywords": "explicit"}}, Chapters: []ChapterMarker{{"One", 0, 12345}, {"Two", 12345, 44117}}}
			if e := CopyAAC(context.Background(), src, out, opts); e != nil {
				t.Fatal(e)
			}
			want := encodedPCM(t, src, 1)
			got := encodedPCM(t, out, 1)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("packet copy changed audio %d/%d", len(want), len(got))
			}
			inspect := audiotest.Inspect(t, out)
			if inspect.Tags["title"] != "Copied" || inspect.DurationFrames != 44117 || len(inspect.Chapters) != 2 || inspect.Chapters[1].StartFrame != 12345 {
				t.Fatal(inspect)
			}
			after, _ := os.ReadFile(src)
			if sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("source changed")
			}
		})
	}
}
func TestPreserveAACRejects(t *testing.T) {
	src, _ := encodingFixture(t, 1, 44117)
	for _, kind := range []string{"notAAC", "trim", "gain", "format", "channels", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			in := src
			if kind != "notAAC" {
				in = filepath.Join(t.TempDir(), "in.m4a")
				audiotest.WriteAAC(t, in, 44117, 0)
			}
			opts := EncodeOptions{Format: "m4a", Channels: 1, EndFrame: -1}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "trim":
				opts.EndFrame = 1000
			case "gain":
				opts.GainDB = 1
			case "format":
				opts.Format = "wav"
			case "channels":
				opts.Channels = 2
			case "cancel":
				cancel()
			}
			out := filepath.Join(t.TempDir(), "out.m4a")
			if e := CopyAAC(ctx, in, out, opts); e == nil {
				t.Fatal("incompatible copy accepted")
			}
			if _, e := os.Lstat(out); !os.IsNotExist(e) {
				t.Fatal("copy partial exists")
			}
		})
	}
}
func TestEncodeRangeGainQuantization(t *testing.T) {
	src, want := encodingFixture(t, 2, 44117)
	for _, format := range []string{"wav", "flac"} {
		out := filepath.Join(t.TempDir(), "out."+format)
		if e := EncodePCM(context.Background(), src, out, EncodeOptions{Format: format, Channels: 2, StartFrame: 123, EndFrame: 44000, GainDB: -6}); e != nil {
			t.Fatal(e)
		}
		got := encodedPCM(t, out, 2)
		if len(got) != (44000-123)*2 {
			t.Fatal("trim length", len(got))
		}
		for i, v := range got {
			expected := float64(want[i+246]) * math.Pow(10, -6.0/20)
			if math.Abs(float64(v)-expected) > 1.0/(1<<23) {
				t.Fatalf("gain/24-bit quantization at %d: %.10f want %.10f", i, v, expected)
			}
			if float64(v)*(1<<23) != math.Trunc(float64(v)*(1<<23)) {
				t.Fatal("more than 24 bits")
			}
		}
	}
}
func TestEncodeFailureCleanupReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove the output while it is open")
	}
	out := filepath.Join(t.TempDir(), "out.m4a")
	fault := errors.New("producer failed")
	e := withEncodedOutput(out, func(f *os.File) error {
		format, _ := bookPCMFormat(1)
		return encodeStream(context.Background(), f, format, EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1}, func(emit func([]float32) error) error {
			if e := os.Remove(out); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(out, []byte("replacement"), 0600); e != nil {
				t.Fatal(e)
			}
			return fault
		})
	})
	if !errors.Is(e, fault) {
		t.Fatal(e)
	}
	b, e := os.ReadFile(out)
	if e != nil || string(b) != "replacement" {
		t.Fatal("replacement removed", e)
	}
}
func TestEncodeMetadataSupport(t *testing.T) {
	opts := EncodeOptions{Format: "m4a", Metadata: meta.AudiobookMetadata{Subtitle: "not supported", Extra: map[string]string{"custom": "keep in project", "keywords": "supported"}}}
	warnings := MetadataWarnings(opts)
	if len(warnings) != 2 {
		t.Fatal(warnings)
	}
	opts.Format = "flac"
	if got := MetadataWarnings(opts); len(got) != 0 {
		t.Fatal(got)
	}
	opts.Format = "wav"
	opts.CoverPath = "cover.png"
	if len(MetadataWarnings(opts)) < 3 {
		t.Fatal("WAV omissions not explicit")
	}
}

func TestEncodeFailureCleanupNonfiniteGain(t *testing.T) {
	out := filepath.Join(t.TempDir(), "overflow.m4a")
	f, _ := bookPCMFormat(1)
	e := withEncodedOutput(out, func(file *os.File) error {
		return encodeStream(context.Background(), file, f, EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1, GainDB: 120}, func(emit func([]float32) error) error { return emit([]float32{math.MaxFloat32, math.MaxFloat32}) })
	})
	if e == nil {
		t.Fatal("nonfinite filtered samples accepted")
	}
	if _, e = os.Lstat(out); !os.IsNotExist(e) {
		t.Fatal("partial remains")
	}
}
func TestPreserveAACClearedTagsAndChapters(t *testing.T) {
	src, _ := encodingFixture(t, 1, 44117)
	tagged := filepath.Join(t.TempDir(), "tagged.m4a")
	opts := EncodeOptions{Format: "m4a", Channels: 1, BitRate: 96000, EndFrame: -1, Metadata: meta.AudiobookMetadata{Title: "Old", Artist: "Author", Composer: "Reader", Extra: map[string]string{"keywords": "Old"}}, Chapters: []ChapterMarker{{"Old chapter", 0, 44117}}}
	if e := EncodePCM(context.Background(), src, tagged, opts); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(t.TempDir(), "copy.m4a")
	opts.Metadata = meta.AudiobookMetadata{Extra: map[string]string{"keywords": "", "TITLE": "stale", "AUTHOR": "stale", "COMPOSER": "stale"}}
	opts.Chapters = nil
	opts.Track = "2/3"
	before := opts.Metadata.Extra
	if e := CopyAAC(context.Background(), tagged, out, opts); e != nil {
		t.Fatal(e)
	}
	got := audiotest.Inspect(t, out)
	for _, key := range []string{"title", "artist", "composer", "keywords"} {
		if got.Tags[key] != "" {
			t.Fatal("source tag leaked", key, got.Tags)
		}
	}
	if len(got.Chapters) != 0 || got.Tags["track"] != "2/3" {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(opts.Metadata.Extra, before) {
		t.Fatal("metadata mutated")
	}
}
func TestEncodeFailureCleanupTruncatedSource(t *testing.T) {
	src, _ := encodingFixture(t, 1, 44117)
	if e := os.Truncate(src, 1000); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(t.TempDir(), "out.m4a")
	if e := EncodePCM(context.Background(), src, out, EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1}); e == nil {
		t.Fatal("truncated source accepted")
	}
	if _, e := os.Lstat(out); !os.IsNotExist(e) {
		t.Fatal("partial remains")
	}
}

func TestEncoderTinyFinalFrames(t *testing.T) {
	for _, n := range []int{1, 17, 1023, 1024, 1025} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			src, _ := encodingFixture(t, 1, n)
			out := filepath.Join(t.TempDir(), "out.m4a")
			if e := EncodePCM(context.Background(), src, out, EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1}); e != nil {
				t.Fatal(e)
			}
			if got := len(encodedPCM(t, out, 1)); got != n {
				t.Fatalf("%d audible samples, want %d", got, n)
			}
			if got := audiotest.Inspect(t, out).DurationFrames; got != int64(n) {
				t.Fatalf("%d duration frames, want %d", got, n)
			}
		})
	}
}
func TestEncodeFailureCleanupSuccessfulReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.m4a")
	e := withEncodedOutput(path, func(file *os.File) error {
		if e := file.Close(); e != nil {
			t.Fatal(e)
		}
		if e := os.Remove(path); e != nil {
			t.Fatal(e)
		}
		return os.WriteFile(path, []byte("replacement"), 0600)
	})
	if e == nil {
		t.Fatal("claimed success for unowned replacement")
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "replacement" {
		t.Fatal("replacement removed", e)
	}
}

func TestPreserveAACSourceChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.m4a")
	audiotest.WriteAAC(t, path, 44117, 0)
	_, _, close, e := openCopySource(context.Background(), path, 1)
	if e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(path, st.ModTime(), st.ModTime().Add(time.Second)); e != nil {
		t.Fatal(e)
	}
	if e = close(); e == nil {
		t.Fatal("source mutation unreported")
	}
}

func TestProjectTagsTempoNativeKey(t *testing.T) {
	src, _ := encodingFixture(t, 1, 2000)
	out := filepath.Join(t.TempDir(), "out.m4a")
	opts := EncodeOptions{Format: "m4a", Channels: 1, BitRate: 64000, EndFrame: -1, Metadata: meta.AudiobookMetadata{Extra: map[string]string{"tmpo": "123", "tempo": "456"}}}
	warnings := MetadataWarnings(opts)
	if len(warnings) != 1 || !strings.Contains(warnings[0], `"tempo"`) {
		t.Fatal("wrong native support warning", warnings)
	}
	if e := EncodePCM(context.Background(), src, out, opts); e != nil {
		t.Fatal(e)
	}
	// n8.0's MOV demuxer does not expose tmpo. Inspect its standard iTunes
	// atom directly; the native muxer writes a 16-bit integer data payload.
	b, e := os.ReadFile(out)
	if e != nil {
		t.Fatal(e)
	}
	at := bytes.Index(b, []byte("tmpo"))
	if at < 4 || at+22 > len(b) || binary.BigEndian.Uint32(b[at-4:at]) != 26 || string(b[at+8:at+12]) != "data" || binary.BigEndian.Uint16(b[at+20:at+22]) != 123 {
		t.Fatal("native tempo atom lost or wrong")
	}

}
