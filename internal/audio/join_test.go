package audio

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"chaptersmith/internal/audiotest"
	"github.com/asticode/go-astiav"
)

func TestJoinAACAudibleStaging(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.m4a"), filepath.Join(dir, "b.m4a")
	const an, bn = 22037, 23071
	audiotest.WriteAAC(t, a, an, 88200) // Nonzero source origin and encoder priming.
	samples := make([]float32, bn)
	for i := range samples {
		samples[i] = float32(.3 * math.Cos(2*math.Pi*670*float64(i)/44100))
	}
	audiotest.WriteAACSamples(t, b, samples, 128000)
	ap, bp := audiotest.ReferenceAAC(t, a)[:an], audiotest.ReferenceAAC(t, b)[:bn]
	want := append(append(append([]float32{}, bp...), ap...), bp...)
	paths := []string{b, a, b}
	hashes := map[string][32]byte{}
	for _, p := range paths {
		data, _ := os.ReadFile(p)
		hashes[p] = sha256.Sum256(data)
	}
	out := filepath.Join(dir, "join.m4a")
	calls := 0
	var progress []int
	result, e := joinAAC(context.Background(), paths, out, func(n, total int) {
		if total != 4 {
			t.Fatal(total)
		}
		progress = append(progress, n)
	}, func(ctx context.Context, in, out string, opts EncodeOptions, f pcmFormat) error {
		calls++
		if opts.BitRate != 128000 || opts.GainDB != 0 || f.rate != 44100 {
			t.Fatal(opts, f)
		}
		got := audiotest.ReadPCM(t, in, 1)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("audible staging mismatch: got %d want %d", len(got), len(want))
		}
		return encodePCM(ctx, in, out, opts, f)
	})
	if e != nil {
		t.Fatal(e)
	}
	if calls != 1 || result.SampleRate != 44100 || !reflect.DeepEqual(result.Frames, []int64{bn, an, bn}) || !reflect.DeepEqual(progress, []int{1, 2, 3, 4}) {
		t.Fatal(calls, result, progress)
	}
	ref := filepath.Join(dir, "reference.m4a")
	audiotest.WriteAACSamples(t, ref, want, 128000)
	got, reference := audiotest.ReferenceAAC(t, out), audiotest.ReferenceAAC(t, ref)
	for _, start := range []int{0, bn, bn + an, len(want) - 1024} {
		var power float64
		for i := start; i < start+512; i++ {
			v := float64(got[i] - reference[i])
			power += v * v
		}
		if math.Sqrt(power/512) > .000001 {
			t.Fatalf("onset/tail differs from direct single encoder at %d", start)
		}
	}
	for p, hash := range hashes {
		data, _ := os.ReadFile(p)
		if sha256.Sum256(data) != hash {
			t.Fatal("source changed")
		}
	}
	assertJoinClean(t, dir)
}

// Independent integer WAV fixture, with an explicit WAVEFORMATEXTENSIBLE mask
// for multichannel sources. Channel count alone never establishes identities.
func joinWAV(t *testing.T, path string, rate, channels, frames int, mask uint32, tones ...bool) []float32 {
	t.Helper()
	s := make([]int16, frames*channels)
	want := make([]float32, len(s))
	for i := range s {
		s[i] = int16(1000 * (i%channels + 1))
		if len(tones) > 0 && tones[0] {
			s[i] = int16(6500 * math.Cos(2*math.Pi*joinFrequencies[i%channels]*float64(i/channels)/float64(rate)))
		}
		want[i] = float32(s[i]) / 32768
	}
	audiotest.WriteWAV(t, path, rate, channels, s)
	if mask != 0 {
		b, _ := os.ReadFile(path)
		ext := make([]byte, 24)
		binary.LittleEndian.PutUint16(ext, 22)
		binary.LittleEndian.PutUint16(ext[2:], 16)
		binary.LittleEndian.PutUint32(ext[4:], mask)
		copy(ext[8:], []byte{1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71})
		b = append(append(append([]byte{}, b[:36]...), ext...), b[36:]...)
		binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
		binary.LittleEndian.PutUint32(b[16:], 40)
		binary.LittleEndian.PutUint16(b[20:], 0xfffe)
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	return want
}
func assertJoinClean(t *testing.T, dir string) {
	t.Helper()
	files, e := filepath.Glob(filepath.Join(dir, ".join-*"))
	if e != nil || len(files) != 0 {
		t.Fatal("staging leaked", files, e)
	}
}

var joinFrequencies = []float64{330, 550, 770, 80, 990, 1210}

func TestJoinAACFirstAudioTrack(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		firstRate, laterRate, defaultTrack int
	}{
		{"later-default", 44100, 48000, 1},
		{"first-default", 48000, 44100, 0},
		{"unsupported-unselected-default", 44100, 12345, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "first.wav"), filepath.Join(dir, "later.wav")
			want := joinWAV(t, a, tc.firstRate, 1, 2207, 0, true)
			joinWAV(t, b, tc.laterRate, 2, 3109, 0)
			src := filepath.Join(dir, "tracks.mka")
			audiotest.WriteAudioTracks(t, src, []string{a, b}, tc.defaultTrack)
			before, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			result, err := joinAAC(context.Background(), []string{src, src}, filepath.Join(dir, "out.m4a"), nil, func(ctx context.Context, in, out string, opts EncodeOptions, f pcmFormat) error {
				calls++
				if f.rate != tc.firstRate || !f.layout.Equal(astiav.ChannelLayoutMono) {
					t.Fatalf("target must describe first audio track: %d %s", f.rate, f.layout.String())
				}
				got := audiotest.ReadPCM(t, in, 1)
				if !reflect.DeepEqual(got, append(append([]float32{}, want...), want...)) {
					t.Fatalf("staged samples must be first track twice: got %d frames, want 4414", len(got))
				}
				return encodePCM(ctx, in, out, opts, f)
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.SampleRate != tc.firstRate || !reflect.DeepEqual(result.Frames, []int64{2207, 2207}) {
				t.Fatal(calls, result)
			}
			after, err := os.ReadFile(src)
			if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
				t.Fatal("source changed", err)
			}
			assertJoinClean(t, dir)
		})
	}
}

func TestJoinAACUnsupportedFirstTrackBeforeStaging(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "first.wav"), filepath.Join(dir, "later.wav")
	joinWAV(t, a, 12345, 1, 2207, 0)
	joinWAV(t, b, 48000, 2, 3109, 0, true)
	src := filepath.Join(dir, "tracks.mka")
	audiotest.WriteAudioTracks(t, src, []string{a, b}, 1)
	// A missing output parent makes any attempt to create staging fail. The
	// format error must win, proving validation precedes creation, not cleanup.
	out := filepath.Join(dir, "missing", "out.m4a")
	_, err := JoinAAC(context.Background(), []string{src}, out, nil)
	if err == nil || !strings.Contains(err.Error(), "AAC does not support sample rate 12345") {
		t.Fatalf("wanted selected-track rejection before staging: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(out)); !os.IsNotExist(err) {
		t.Fatal("created output directory", err)
	}
	assertJoinClean(t, dir)
}

func TestPCMFirstAudioTrackMatchesProbe(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "first.wav"), filepath.Join(dir, "later.wav")
	want := joinWAV(t, a, 44100, 1, 2207, 0, true)
	joinWAV(t, b, 48000, 2, 3109, 0)
	src := filepath.Join(dir, "tracks.mka")
	audiotest.WriteAudioTracks(t, src, []string{a, b}, 1)
	info, err := ProbeSource(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRate != 44100 || info.Channels != 1 {
		t.Fatal(info)
	}
	var got []float32
	err = DecodePCM(context.Background(), src, 1, 0, -1, func(b []float32) error { got = append(got, b...); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PCM must decode probed first track: got %d frames, want %d", len(got), len(want))
	}
}

func TestJoinAACFormats(t *testing.T) {
	for _, rate := range []int{44100, 48000} {
		for _, ch := range []int{1, 2, 6} {
			t.Run(fmt.Sprintf("%d/%d", rate, ch), func(t *testing.T) {
				dir := t.TempDir()
				p := filepath.Join(dir, "in.wav")
				mask := uint32(0)
				layout := astiav.ChannelLayoutMono
				if ch == 2 {
					layout = astiav.ChannelLayoutStereo
				}
				if ch == 6 {
					mask = 0x3f
					layout = astiav.ChannelLayout5Point1Back
				}
				expected := joinWAV(t, p, rate, ch, rate/2+13, mask, true)
				out := filepath.Join(dir, "out.m4a")
				result, e := joinAAC(context.Background(), []string{p, p}, out, nil, func(ctx context.Context, in, out string, o EncodeOptions, f pcmFormat) error {
					if f.rate != rate || !f.layout.Equal(layout) {
						t.Fatal("target format", f)
					}
					staged := audiotest.ReadPCM(t, in, ch)
					if !reflect.DeepEqual(staged, append(append([]float32{}, expected...), expected...)) {
						t.Fatal("channel identity/order changed in staging")
					}
					return encodePCM(ctx, in, out, o, f)
				})
				if e != nil {
					t.Fatal(e)
				}
				if !reflect.DeepEqual(result.Frames, []int64{int64(rate/2 + 13), int64(rate/2 + 13)}) {
					t.Fatal(result)
				}
				e = probeJoinFormat(context.Background(), out, func(cp *astiav.CodecParameters) error {
					if cp.SampleRate() != rate || !cp.ChannelLayout().Equal(layout) {
						t.Fatal("output format", cp.SampleRate(), cp.ChannelLayout().String())
					}
					return nil
				})
				if e != nil {
					t.Fatal(e)
				}
				// Distinct frequencies prove channel identities after AAC encoding.
				d, e := openPCM(context.Background(), out, pcmFormat{rate, ch, layout})
				if e != nil {
					t.Fatal(e)
				}
				var decoded []float32
				e = d.Range(0, -1, func(b []float32) error { decoded = append(decoded, b...); return nil })
				e = errors.Join(e, d.Close())
				if e != nil {
					t.Fatal(e)
				}
				if len(decoded)/ch != 2*(rate/2+13) {
					t.Fatal("effective frames", len(decoded)/ch)
				}
				// Inspect the middle of the first occurrence; AAC resets no channel identities.
				for c := 0; c < ch; c++ {
					for tone := 0; tone < ch; tone++ {
						var re, im float64
						begin, end := rate/10, rate*4/10
						for frame := begin; frame < end; frame++ {
							phase := 2 * math.Pi * joinFrequencies[tone] * float64(frame) / float64(rate)
							v := float64(decoded[frame*ch+c])
							re += v * math.Cos(phase)
							im += v * math.Sin(phase)
						}
						amplitude := 2 * math.Hypot(re, im) / float64(end-begin)
						if c == tone && amplitude < .12 || c != tone && amplitude > .03 {
							t.Fatalf("channel %d tone %d amplitude %g", c, tone, amplitude)
						}
					}
				}

				assertJoinClean(t, dir)
			})
		}
	}
}

func TestJoinAACMixedConversion(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.wav"), filepath.Join(dir, "b.wav")
	joinWAV(t, a, 48000, 2, 24013, 0)
	joinWAV(t, b, 44100, 1, 22073, 0)
	result, e := joinAAC(context.Background(), []string{a, b}, filepath.Join(dir, "out.m4a"), nil, func(ctx context.Context, in, out string, o EncodeOptions, f pcmFormat) error {
		if f.rate != 48000 || !f.layout.Equal(astiav.ChannelLayoutStereo) {
			t.Fatal(f)
		}
		staged := audiotest.ReadPCM(t, in, 2)
		// Default libswresample mono -> stereo attenuation is sqrt(1/2).
		for i := 24013*2 + 100; i < len(staged)-100; i++ {
			if math.Abs(float64(staged[i])-1000.0/32768/math.Sqrt2) > 1e-6 {
				t.Fatalf("mixed channel conversion %d: %g", i, staged[i])
			}
		}
		return encodePCM(ctx, in, out, o, f)
	})
	if e != nil {
		t.Fatal(e)
	}
	expected := int64((22073*48000 + 44100 - 1) / 44100)
	if !reflect.DeepEqual(result.Frames, []int64{24013, expected}) {
		t.Fatal(result, expected)
	}
	assertJoinClean(t, dir)
}

func TestJoinAACRejectBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rate, ch int
		mask     uint32
	}{{"rate", 12345, 1, 0}, {"unknown", 44100, 3, 0}, {"unsupported", 44100, 8, 0x2d03f}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "in.wav")
			joinWAV(t, p, tc.rate, tc.ch, 100, tc.mask)
			out := filepath.Join(dir, "out.m4a")
			if _, e := JoinAAC(context.Background(), []string{p}, out, nil); e == nil {
				t.Fatal("accepted unsupported format")
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("created output", e)
			}
			assertJoinClean(t, dir)
		})
	}
}

func TestJoinAACFailures(t *testing.T) {
	for _, kind := range []string{"cancel-decode", "cancel-finish", "encode", "write", "trailer", "close"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "in.wav")
			joinWAV(t, p, 44100, 1, 44137, 0)
			original, _ := os.ReadFile(p)
			out := filepath.Join(dir, "out.m4a")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fault := errors.New("injected " + kind)
			encode := joinEncodeFunc(encodePCM)
			if kind == "encode" {
				encode = func(context.Context, string, string, EncodeOptions, pcmFormat) error { return fault }
			}
			if kind == "write" || kind == "trailer" || kind == "close" {
				encode = func(ctx context.Context, in, out string, o EncodeOptions, f pcmFormat) error {
					return withEncodedOutput(out, func(file *os.File) error {
						return encodeStream(ctx, &faultOutput{File: file, kind: kind, fault: fault}, f, o, func(emit func([]float32) error) error { return emit(make([]float32, 44137)) })
					})
				}
			}
			_, e := joinAAC(ctx, []string{p, p}, out, func(n, total int) {
				if kind == "cancel-decode" && n == 1 || kind == "cancel-finish" && n == total {
					cancel()
				}
			}, encode)
			want := fault
			if strings.HasPrefix(kind, "cancel") {
				want = context.Canceled
			}
			if !errors.Is(e, want) {
				t.Fatal("lost failure", e)
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("partial output left", e)
			}
			assertJoinClean(t, dir)
			after, _ := os.ReadFile(p)
			if sha256.Sum256(after) != sha256.Sum256(original) {
				t.Fatal("source modified")
			}
		})
	}
}
func TestConcatRejectsMultiple(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.m4a")
	e := Concat(context.Background(), []string{"a", "b"}, out, log.New(io.Discard, "", 0))
	if e == nil {
		t.Fatal("multi packet join accepted")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("output created")
	}
}

func TestJoinAACMixedLayoutsAndDownmix(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		rate, ch, otherRate, otherCh int
		mask, otherMask              uint32
	}{
		{"downmix", 44100, 1, 48000, 2, 0, 0},
		{"equal-count-distinct-layouts", 48000, 6, 48000, 6, 0x3f, 0x60f},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "a.wav"), filepath.Join(dir, "b.wav")
			first := joinWAV(t, a, tc.rate, tc.ch, tc.rate/2+13, tc.mask)
			second := joinWAV(t, b, tc.otherRate, tc.otherCh, tc.otherRate/2+13, tc.otherMask)
			result, e := joinAAC(context.Background(), []string{a, b}, filepath.Join(dir, "out.m4a"), nil, func(ctx context.Context, in, out string, o EncodeOptions, f pcmFormat) error {
				staged := audiotest.ReadPCM(t, in, tc.ch)
				if !reflect.DeepEqual(staged[:len(first)], first) {
					t.Fatal("first occurrence changed")
				}
				tail := staged[len(first):]
				if tc.ch == 6 {
					if !reflect.DeepEqual(tail, second) {
						t.Fatal("exact side/back layout conversion changed channel identity")
					}
				} else {
					for _, v := range tail[100 : len(tail)-100] {
						if math.Abs(float64(v)-3000.0/32768/math.Sqrt2) > 1e-6 {
							t.Fatal("downmix", v)
						}
					}
				}
				return encodePCM(ctx, in, out, o, f)
			})
			if e != nil {
				t.Fatal(e)
			}
			if result.SampleRate != tc.rate {
				t.Fatal("first rate lost", result)
			}
			assertJoinClean(t, dir)
		})
	}
}

func TestJoinAACDecoderFailureAndExistingOutput(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "in.wav")
	joinWAV(t, p, 44100, 1, 44137, 0)
	b, _ := os.ReadFile(p)
	if e := os.WriteFile(p, b[:44+8192], 0600); e != nil {
		t.Fatal(e)
	}
	out := filepath.Join(dir, "out.m4a")
	if _, e := JoinAAC(context.Background(), []string{p}, out, nil); e == nil {
		t.Fatal("truncated decoder succeeded")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("partial output")
	}
	assertJoinClean(t, dir)
	joinWAV(t, p, 44100, 1, 44137, 0)
	original, _ := os.ReadFile(p)
	if _, e := JoinAAC(context.Background(), []string{p}, p, nil); e == nil {
		t.Fatal("source overwrite")
	}
	after, _ := os.ReadFile(p)
	if !reflect.DeepEqual(after, original) {
		t.Fatal("source changed")
	}
	assertJoinClean(t, dir)
}
