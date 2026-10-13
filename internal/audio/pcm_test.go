package audio

import (
	"chaptersmith/internal/audiotest"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestPCMExactSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.wav")
	s := make([]int16, 44100)
	for i := range s {
		s[i] = int16(i%30000 - 15000)
	}
	audiotest.WriteWAV(t, path, 44100, 1, s)
	var got []float32
	e := DecodePCM(context.Background(), path, 1, 5444, 43999, func(b []float32) error { got = append(got, b...); return nil })
	if e != nil {
		t.Fatal(e)
	}
	want := make([]float32, 43999-5444)
	for i := range want {
		want[i] = float32(s[5444+i]) / 32768
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PCM mismatch %d/%d", len(got), len(want))
	}
}
func TestRF64(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.wav")
	want := []float32{0, .5, -.5, .125}
	e := WriteRF64(context.Background(), path, 1, func(emit func([]float32) error) error { return emit(want) })
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if string(b[:4]) != "RF64" || string(b[12:16]) != "ds64" || binary.LittleEndian.Uint64(b[20:]) != uint64(len(b)-8) {
		t.Fatal("bad RF64 header")
	}
	if !reflect.DeepEqual(audiotest.ReadPCM(t, path, 1), want) {
		t.Fatal("RF64 samples differ")
	}
}
func TestPCMFailures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.wav")
	audiotest.WriteWAV(t, src, 44100, 1, make([]int16, 100))
	before, _ := os.ReadFile(src)
	sentinel := errors.New("sink failure")
	if e := DecodePCM(ctx, src, 1, 0, -1, func([]float32) error { return sentinel }); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	if e := DecodePCM(ctx, src, 1, 0, 102, func([]float32) error { return nil }); e == nil {
		t.Fatal("early EOF accepted")
	}
	if e := WriteRF64(ctx, src, 1, func(func([]float32) error) error { return nil }); e == nil {
		t.Fatal("overwrite accepted")
	}
	after, _ := os.ReadFile(src)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source changed")
	}
	for _, kind := range []string{"producer", "cancel", "nan"} {
		t.Run(kind, func(t *testing.T) {
			out := filepath.Join(dir, kind+".wav")
			c, cancel := context.WithCancel(ctx)
			defer cancel()
			e := WriteRF64(c, out, 1, func(emit func([]float32) error) error {
				if kind == "producer" {
					return sentinel
				}
				if kind == "cancel" {
					cancel()
					return emit([]float32{0})
				}
				return emit([]float32{float32(math.NaN())})
			})
			if e == nil {
				t.Fatal("expected error")
			}
			if _, e = os.Lstat(out); !os.IsNotExist(e) {
				t.Fatal("partial remains")
			}
		})
	}
}

func TestPCMFailuresTruncated(t *testing.T) {
	p := filepath.Join(t.TempDir(), "broken.wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, 44100))
	if e := os.Truncate(p, 9001); e != nil {
		t.Fatal(e)
	}
	if e := DecodePCM(context.Background(), p, 1, 0, -1, func([]float32) error { return nil }); e == nil {
		t.Fatal("truncated WAV accepted")
	}
}
func TestPCMOneSampleTolerance(t *testing.T) {
	p := filepath.Join(t.TempDir(), "short.wav")
	audiotest.WriteWAV(t, p, 44100, 1, []int16{100, 200})
	var got []float32
	e := DecodePCM(context.Background(), p, 1, 0, 3, func(b []float32) error { got = append(got, b...); return nil })
	if e != nil || len(got) != 3 || got[2] != 0 {
		t.Fatal(got, e)
	}
}
func TestPCMFailuresCancelledDecode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, 44100))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := DecodePCM(ctx, p, 1, 0, -1, func([]float32) error { cancel(); return nil })
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestSourceOriginsAndPaddingAAC(t *testing.T) {
	for _, origin := range []int{0, 88200} {
		t.Run(fmt.Sprint(origin), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "primed.m4a")
			audiotest.WriteAAC(t, p, 44100, origin)
			var full []float32
			e := DecodePCM(context.Background(), p, 1, 0, -1, func(b []float32) error { full = append(full, b...); return nil })
			if e != nil {
				t.Fatal(e)
			}
			if len(full) != 44100 {
				t.Fatalf("audible AAC length %d, want 44100 (priming/tail)", len(full))
			}
			var errorPower, signalPower float64
			for i := 100; i < 44000; i++ {
				want := .4 * math.Sin(2*math.Pi*440*float64(i)/44100)
				delta := float64(full[i]) - want
				errorPower += delta * delta
				signalPower += want * want
			}
			if errorPower/signalPower > .01 {
				t.Fatalf("AAC onset/alignment signal error %g", errorPower/signalPower)
			}
			reference := audiotest.ReferenceAAC(t, p)
			if len(reference) < 44100 {
				t.Fatal("short independent AAC reference")
			}
			for _, start := range []int{0, 44100 - 1024} {
				if !reflect.DeepEqual(full[start:start+1024], reference[start:start+1024]) {
					t.Fatalf("AAC onset/tail differs from independent decoder at %d", start)
				}
			}
			var cut []float32
			e = DecodePCM(context.Background(), p, 1, 5444, 43579, func(b []float32) error { cut = append(cut, b...); return nil })
			if e != nil || !reflect.DeepEqual(cut, full[5444:43579]) {
				t.Fatal("AAC cut differs", e)
			}
		})
	}
}

func TestSourceOriginsAndPaddingOpus(t *testing.T) {
	p := filepath.Join(t.TempDir(), "primed.opus")
	audiotest.WriteOpusSilence(t, p)
	var got []float32
	e := DecodePCM(context.Background(), p, 1, 0, -1, func(b []float32) error { got = append(got, b...); return nil })
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 4410 {
		t.Fatalf("Opus audible samples %d want 4410 (312 preskip,648 end padding)", len(got))
	}
	for _, v := range got {
		if math.Abs(float64(v)) > 1e-12 {
			t.Fatal("DTX silence is not silent", v)
		}
	}
	var cut []float32
	e = DecodePCM(context.Background(), p, 1, 37, 4399, func(b []float32) error { cut = append(cut, b...); return nil })
	if e != nil || !reflect.DeepEqual(cut, got[37:4399]) {
		t.Fatal("Opus cut", e)
	}
}
func TestSourceOriginsMissingPTS(t *testing.T) {
	p := filepath.Join(t.TempDir(), "raw.aac")
	audiotest.WriteRawAAC(t, p, 44100)
	var full []float32
	e := DecodePCM(context.Background(), p, 1, 0, -1, func(b []float32) error { full = append(full, b...); return nil })
	if e != nil {
		t.Fatal(e)
	}
	if len(full) != 46080 {
		t.Fatalf("raw AAC sample count %d", len(full))
	}
	var power float64
	for i := 200; i < 43800; i++ {
		v := float64(full[i+1024]) - .4*math.Sin(2*math.Pi*440*float64(i)/44100)
		power += v * v
	}
	if power/43600 > .001 {
		t.Fatal("missing PTS signal alignment", power/43600)
	}
	var cut []float32
	e = DecodePCM(context.Background(), p, 1, 2345, 45001, func(b []float32) error { cut = append(cut, b...); return nil })
	if e != nil || !reflect.DeepEqual(cut, full[2345:45001]) {
		t.Fatal("missing PTS cuts", e)
	}
}

type failingInput struct {
	*os.File
	remaining int
	fault     error
	closed    bool
}

func (f *failingInput) Read(b []byte) (int, error) {
	if f.remaining <= 0 {
		return 0, f.fault
	}
	if len(b) > f.remaining {
		b = b[:f.remaining]
	}
	n, e := f.File.Read(b)
	f.remaining -= n
	return n, e
}
func (f *failingInput) Close() error { f.closed = true; return f.File.Close() }
func TestPCMFailuresRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, 441000))
	file, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	fault := errors.New("injected source read failure")
	in := &failingInput{File: file, remaining: 300000, fault: fault}
	format, _ := bookPCMFormat(1)
	d, e := openPCMInput(context.Background(), p, format, in)
	if e == nil {
		e = d.Range(0, -1, func([]float32) error { return nil })
		d.Close()
	}
	if !errors.Is(e, fault) {
		t.Fatal("lost read error", e)
	}
	if !in.closed {
		t.Fatal("source handle not closed")
	}
}
func TestMemoryBound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "long.wav")
	chunk := make([]float32, 16384)
	e := WriteRF64(ctx, path, 1, func(emit func([]float32) error) error {
		for left := int64(44100 * 200); left > 0; {
			n := min(left, int64(len(chunk)))
			if e := emit(chunk[:n]); e != nil {
				return e
			}
			left -= n
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	measure := func(seconds int64) (uint64, uint64) {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		peak := audiotest.ResidentBytes()
		if peak == 0 {
			t.Fatal("native RSS unavailable")
		}
		var frames int64
		calls := 0
		e := DecodePCM(ctx, path, 1, 0, seconds*44100, func(b []float32) error {
			if len(b) > 16384 {
				t.Fatal("unbounded PCM chunk", len(b))
			}
			frames += int64(len(b))
			calls++
			if calls%256 == 0 {
				peak = max(peak, audiotest.ResidentBytes())
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		runtime.ReadMemStats(&after)
		if frames != seconds*44100 {
			t.Fatal("wrong frames", frames)
		}
		return peak, after.TotalAlloc - before.TotalAlloc
	}
	measure(1)
	shortRSS, shortGo := measure(20)
	longRSS, longGo := measure(200)
	t.Logf("20s RSS=%d GoAlloc=%d; 200s RSS=%d GoAlloc=%d", shortRSS, shortGo, longRSS, longGo)
	if longRSS > shortRSS+12*1024*1024 {
		t.Fatal("native-inclusive memory grew with stream length")
	}
	if longGo > shortGo+1024*1024 {
		t.Fatal("Go allocation volume grew with stream length")
	}
}
func TestPCMOneSampleToleranceOnlyOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "short.wav")
	audiotest.WriteWAV(t, p, 44100, 1, []int16{1, 2})
	d, e := OpenPCM(context.Background(), p, 1)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	if e = d.Range(0, 3, func([]float32) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if e = d.Range(3, 4, func([]float32) error { return nil }); e == nil {
		t.Fatal("repeated terminal padding accepted")
	}
}
func TestPCMChannels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stereo.wav")
	s := make([]int16, 44100*2)
	for i := 0; i < len(s); i += 2 {
		s[i] = 8192
		s[i+1] = -8192
	}
	audiotest.WriteWAV(t, path, 44100, 2, s)
	var count int
	e := DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error {
		count += len(b)
		for _, v := range b {
			if math.Abs(float64(v)) > 1e-7 {
				t.Fatal("stereo downmix incorrect", v)
			}
		}
		return nil
	})
	if e != nil || count != 44100 {
		t.Fatal(count, e)
	}
	e = DecodePCM(context.Background(), path, 2, 17, 44099, func(b []float32) error {
		for i, v := range b {
			want := float32(.25)
			if i%2 == 1 {
				want = -.25
			}
			if v != want {
				t.Fatal("stereo channel mismatch", i, v)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestResamplingBoundedDrain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "low-rate.wav")
	s := make([]int16, 10000)
	for i := range s {
		s[i] = int16(8192 * math.Sin(2*math.Pi*100*float64(i)/1000))
	}
	audiotest.WriteWAV(t, p, 1000, 1, s)
	var count int
	e := DecodePCM(context.Background(), p, 1, 0, -1, func(b []float32) error {
		for i, v := range b {
			if count+i > 1000 && count+i < 440000 && math.Abs(float64(v)-.25*math.Sin(2*math.Pi*100*float64(count+i)/44100)) > .005 {
				t.Fatalf("resampler introduced internal discontinuity at %d: %g", count+i, v)
			}
		}
		count += len(b)
		return nil
	})
	if e != nil || count != 441000 {
		t.Fatal("drain length", count, e)
	}
}
func TestPCMFailuresNonfiniteDiscardedInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nonfinite.wav")
	audiotest.WriteWAV(t, p, 44100, 1, make([]int16, 200))
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	binary.LittleEndian.PutUint16(b[20:], 3)
	binary.LittleEndian.PutUint32(b[28:], 44100*4)
	binary.LittleEndian.PutUint16(b[32:], 4)
	binary.LittleEndian.PutUint16(b[34:], 32)
	binary.LittleEndian.PutUint32(b[44:], math.Float32bits(float32(math.NaN())))
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	if e = DecodePCM(context.Background(), p, 1, 50, 100, func([]float32) error { return nil }); e == nil {
		t.Fatal("nonfinite discarded input accepted")
	}
}
func TestPCMFailuresPreserveReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove the output while it is open")
	}
	p := filepath.Join(t.TempDir(), "out.wav")
	fault := errors.New("producer failed")
	e := WriteRF64(context.Background(), p, 1, func(emit func([]float32) error) error {
		if e := os.Remove(p); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte("replacement"), 0600); e != nil {
			t.Fatal(e)
		}
		return fault
	})
	if !errors.Is(e, fault) {
		t.Fatal(e)
	}
	b, e := os.ReadFile(p)
	if e != nil || string(b) != "replacement" {
		t.Fatal("cleanup removed unowned replacement", e)
	}
}
func TestSourceOriginsCoarseTimestamps(t *testing.T) {
	dir := t.TempDir()
	raw, path := filepath.Join(dir, "raw.aac"), filepath.Join(dir, "coarse.mka")
	audiotest.WriteRawAAC(t, raw, 44100)
	audiotest.WrapAACMatroska(t, raw, path)
	reference := audiotest.ReferenceAAC(t, raw)
	var got []float32
	e := DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error { got = append(got, b...); return nil })
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(got, reference) {
		t.Fatalf("coarse timestamps changed audio: %d samples, want %d", len(got), len(reference))
	}
}
func TestResamplingLowRateDoesNotStall(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ten-hz.wav")
	audiotest.WriteWAV(t, p, 10, 1, make([]int16, 100))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var count int
	e := DecodePCM(ctx, p, 1, 0, -1, func(b []float32) error { count += len(b); return nil })
	if e != nil || count != 441000 {
		t.Fatal("low-rate resampler stalled or lost samples", count, e)
	}
}

func TestPCMFailuresPacketAlignedWAVTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aligned.wav")
	audiotest.WriteWAV(t, path, 44100, 1, make([]int16, 44100))
	if e := os.Truncate(path, 44+8192); e != nil {
		t.Fatal(e)
	}
	if e := DecodePCM(context.Background(), path, 1, 0, -1, func([]float32) error { return nil }); e == nil {
		t.Fatal("packet-aligned WAV truncation accepted as EOF")
	}
}

type closeFailureInput struct {
	io.ReadSeeker
	fault  error
	closed bool
}

func (in *closeFailureInput) Close() error { in.closed = true; return in.fault }
func TestPCMFailuresOpenJoinsCloseError(t *testing.T) {
	fault := errors.New("injected close failure")
	in := &closeFailureInput{ReadSeeker: bytes.NewReader([]byte("not audio")), fault: fault}
	format, _ := bookPCMFormat(1)
	d, e := openPCMInput(context.Background(), "broken.wav", format, in)
	if d != nil {
		d.Close()
		t.Fatal("invalid source accepted")
	}
	if !in.closed || !errors.Is(e, fault) {
		t.Fatalf("open cleanup lost close error: closed=%v err=%v", in.closed, e)
	}
}

func TestPCMFailuresWholeFLACFrameTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "complete.flac")
	audiotest.WriteFLAC(t, path, 44100)
	var count int
	e := DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error { count += len(b); return nil })
	if e != nil || count != 44100 {
		t.Fatalf("intact FLAC: %d frames, %v", count, e)
	}
	if e = os.Truncate(path, audiotest.LastPacketOffset(t, path)); e != nil {
		t.Fatal(e)
	}
	if e = DecodePCM(context.Background(), path, 1, 0, -1, func([]float32) error { return nil }); e == nil {
		t.Fatal("FLAC missing complete final frame accepted despite STREAMINFO count")
	}
}
func TestPCMUnknownWAVLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.wav")
	audiotest.WriteWAV(t, path, 44100, 1, make([]int16, 40960))
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	binary.LittleEndian.PutUint32(b[4:], math.MaxUint32)
	binary.LittleEndian.PutUint32(b[40:], math.MaxUint32)
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	var count int
	e = DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error { count += len(b); return nil })
	if e != nil || count != 40960 {
		t.Fatalf("unknown WAV bound must stay unknown: %d %v", count, e)
	}
}
func TestPCMUnknownFLACLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.flac")
	audiotest.WriteFLAC(t, path, 44100)
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if string(b[:4]) != "fLaC" || b[4]&0x7f != 0 {
		t.Fatal("fixture STREAMINFO missing")
	}
	packed := binary.BigEndian.Uint64(b[18:26])
	packed &= ^uint64((1 << 36) - 1)
	binary.BigEndian.PutUint64(b[18:26], packed)
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	var count int
	e = DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error { count += len(b); return nil })
	if e != nil || count != 44100 {
		t.Fatalf("unknown FLAC length: %d %v", count, e)
	}
}

func TestPCMFailuresWAVDeclaredDataBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data-bound.wav")
	audiotest.WriteWAV(t, path, 44100, 1, make([]int16, 44100))
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	binary.LittleEndian.PutUint32(b[4:8], math.MaxUint32)
	if e = os.WriteFile(path, b[:44+8192], 0600); e != nil {
		t.Fatal(e)
	}
	if e = DecodePCM(context.Background(), path, 1, 0, -1, func([]float32) error { return nil }); e == nil {
		t.Fatal("ignored data size when RIFF size is unknown")
	}
}
func TestPCMWAVTrailingMetadataOutsideRIFF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trailing.wav")
	audiotest.WriteWAV(t, path, 44100, 1, make([]int16, 44100))
	file, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	tag := make([]byte, 128)
	copy(tag, "TAGmetadata outside the declared RIFF form")
	_, e = file.Write(tag)
	closeErr := file.Close()
	if e != nil || closeErr != nil {
		t.Fatal(e, closeErr)
	}
	var count int
	e = DecodePCM(context.Background(), path, 1, 0, -1, func(b []float32) error { count += len(b); return nil })
	if e != nil || count != 44100 {
		t.Fatalf("trailing non-RIFF metadata affected audio: %d %v", count, e)
	}
}
